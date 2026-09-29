package observability

import (
	"sort"
	"time"
)

const (
	DiagnosisCandidateLimit = 200
	DiagnosisCompletionLimit = 20
	DiagnosisFollowupLimit = 12
	DiagnosisContextLimit = 24
)

// JobDiagnosis is shared by Portal and CLI. It describes a bounded sample of
// retained log evidence, never a transition in the authoritative job state.
type JobDiagnosis struct {
	JobID string `json:"jobId"`
	ObservedState string `json:"observedState"`
	StatusReason string `json:"statusReason"`
	StatusMessage string `json:"statusMessage"`
	Classification string `json:"classification"`
	FailurePhase string `json:"failurePhase"`
	Summary string `json:"summary"`
	FirstFailure *DiagnosisEvidence `json:"firstFailure"`
	Followups []DiagnosisEvidence `json:"followups"`
	CompletionEvidence []DiagnosisEvidence `json:"completionEvidence"`
	Coverage DiagnosisCoverage `json:"coverage"`
	Notes []string `json:"notes"`
}

type DiagnosisEvidence struct {
	Timestamp time.Time `json:"timestamp"`
	Line string `json:"line"`
	Stream map[string]string `json:"stream,omitempty"`
	Kind string `json:"kind"`
	Context []LogLine `json:"context,omitempty"`
}

type DiagnosisCoverage struct {
	WindowStart time.Time `json:"windowStart"`
	WindowEnd time.Time `json:"windowEnd"`
	Filtered bool `json:"filtered"`
	Partial bool `json:"partial"`
	Truncated bool `json:"truncated"`
	LogUnavailable bool `json:"logUnavailable"`
	ContextUnavailable bool `json:"contextUnavailable"`
	CompletionUnavailable bool `json:"completionUnavailable"`
	FollowupsUnavailable bool `json:"followupsUnavailable"`
	CandidateLines int `json:"candidateLines"`
	CandidateLimit int `json:"candidateLimit"`
}

// AnalyzeDiagnosis never changes its input lines/maps. Sorting and redaction
// happen on copies, preserving evidence owned by the log provider.
func AnalyzeDiagnosis(base JobDiagnosis, candidates, completions, followups []LogLine) JobDiagnosis {
	result := base
	result.FirstFailure = nil
	result.Followups = make([]DiagnosisEvidence, 0)
	result.CompletionEvidence = make([]DiagnosisEvidence, 0)
	result.Notes = []string{
		"这是保留日志中最早识别到的错误，不是已证明的根因；日志可能延迟、已捕获异常可能无害，也可能缺少更早日志。",
		"当前结果来自有条数与时间范围限制的筛选日志；未识别到错误不代表成功，请以平台任务状态为准。",
	}
	result.Coverage.Filtered, result.Coverage.Partial = true, true
	result.Coverage.CandidateLimit = DiagnosisCandidateLimit
	var reasonTruncated, messageTruncated bool
	result.StatusReason, reasonTruncated = RedactDiagnosisText(base.StatusReason)
	result.StatusMessage, messageTruncated = RedactDiagnosisText(base.StatusMessage)
	result.Coverage.Truncated = result.Coverage.Truncated || reasonTruncated || messageTruncated
	ordered := orderedDiagnosisLines(candidates)
	if len(ordered) >= DiagnosisCandidateLimit { result.Coverage.Truncated = true }
	if len(ordered) > DiagnosisCandidateLimit { ordered = ordered[:DiagnosisCandidateLimit] }
	result.Coverage.CandidateLines = len(ordered)
	for _, line := range ordered {
		kind := diagnosisKind(line.Line)
		if kind == "completion" || kind == "checkpoint" { completions = append(append([]LogLine(nil), completions...), line); continue }
		if kind != "" && result.FirstFailure == nil {
			evidence, truncated := diagnosisEvidence(line, kind)
			result.Coverage.Truncated = result.Coverage.Truncated || truncated
			result.FirstFailure = &evidence
		}
	}
	result = addDiagnosisEvidence(result, completions, append(append([]LogLine(nil), ordered...), followups...))
	return classifyDiagnosis(result)
}

func orderedDiagnosisLines(lines []LogLine) []LogLine {
	ordered := append([]LogLine(nil), lines...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Timestamp.Before(ordered[j].Timestamp) })
	return ordered
}

func addDiagnosisEvidence(result JobDiagnosis, completions, followups []LogLine) JobDiagnosis {
	seen := make(map[string]bool)
	for _, line := range orderedDiagnosisLines(completions) {
		kind := diagnosisKind(line.Line)
		if kind != "completion" && kind != "checkpoint" { continue }
		if len(result.CompletionEvidence) >= DiagnosisCompletionLimit { result.Coverage.Truncated = true; break }
		evidence, truncated := diagnosisEvidence(line, kind)
		result.Coverage.Truncated = result.Coverage.Truncated || truncated
		key := evidence.Timestamp.String() + evidence.Line
		if !seen[key] { result.CompletionEvidence = append(result.CompletionEvidence, evidence); seen[key] = true }
	}
	if len(completions) >= DiagnosisCompletionLimit { result.Coverage.Truncated = true }
	if result.FirstFailure == nil { return result }
	for _, line := range orderedDiagnosisLines(followups) {
		kind := diagnosisKind(line.Line)
		if kind != "communication_error" && kind != "fatal_signal" { continue }
		if !line.Timestamp.After(result.FirstFailure.Timestamp) { continue }
		evidence, truncated := diagnosisEvidence(line, kind)
		result.Coverage.Truncated = result.Coverage.Truncated || truncated
		key := evidence.Timestamp.String() + evidence.Line
		if seen[key] { continue }
		seen[key] = true
		if len(result.Followups) >= DiagnosisFollowupLimit { result.Coverage.Truncated = true; break }
		result.Followups = append(result.Followups, evidence)
	}
	return result
}

func classifyDiagnosis(result JobDiagnosis) JobDiagnosis {
	result.Classification, result.FailurePhase = "no_failure_evidence", "unknown"
	result.Summary = "当前筛选的保留日志中未识别到错误；这不能证明任务成功。"
	if result.Coverage.LogUnavailable {
		result.Classification = "unavailable"
		result.Summary = "日志证据暂不可用，请以平台任务状态为准，稍后重试诊断。"
		return result
	}
	if len(result.CompletionEvidence) > 0 { result.Notes = append(result.Notes, "完成或 Checkpoint 文字仅为未经验证的日志证据，不能证明产物可用或进程成功退出。") }
	if result.FirstFailure == nil { return result }
	result.Summary = "以下为保留日志中最早识别到的错误线索，尚未证明它是根因。"
	switch result.FirstFailure.Kind {
	case "numerical_error", "python_exception", "out_of_memory":
		result.Classification, result.FailurePhase = "application_failure", "application"
		result.Notes = append(result.Notes, "应用阶段由错误文字推断，可能发生在加载或预处理阶段，不一定在训练计算期间。")
	case "communication_error":
		result.Classification = "communication_failure"
		result.Notes = append(result.Notes, "通信错误可能为后续现象；当前样本未发现更早的具体错误。")
	case "fatal_signal":
		result.Classification = "runtime_failure"
		for _, completion := range result.CompletionEvidence {
			if completion.Kind == "completion" && completion.Timestamp.Before(result.FirstFailure.Timestamp) {
				result.Classification, result.FailurePhase = "possible_runtime_teardown", "launcher"
				result.Summary = "完成文字之后出现最早识别的错误，可能发生在运行时退出清理阶段；尚未证明根因或训练成功。"
				result.Notes = append(result.Notes, "退出清理阶段仅根据时间顺序推断；完成文字未经验证，且可能来自其他 Worker。")
				break
			}
		}
	}
	if len(result.Followups) > 0 { result.Notes = append(result.Notes, "后续通信或致命错误仅为后续线索，时间先后不证明因果关系。") }
	return result
}

func diagnosisEvidence(line LogLine, kind string) (DiagnosisEvidence, bool) {
	clean, truncated := RedactDiagnosisLogLine(line)
	return DiagnosisEvidence{Timestamp: clean.Timestamp, Line: clean.Line, Stream: clean.Stream, Kind: kind}, truncated
}

// WithDiagnosisContext attaches at most 24 redacted context lines, including
// the first failure itself even when other workers flood the same timestamp.
func WithDiagnosisContext(result JobDiagnosis, lines []LogLine) JobDiagnosis {
	if result.FirstFailure == nil { return result }
	first := *result.FirstFailure
	context := make([]LogLine, 0, DiagnosisContextLimit)
	seen := map[string]bool{first.Timestamp.String() + first.Line: true}
	if len(lines) >= DiagnosisContextLimit { result.Coverage.Truncated = true }
	for _, line := range orderedDiagnosisLines(lines) {
		if len(context) >= DiagnosisContextLimit-1 { break }
		clean, truncated := RedactDiagnosisLogLine(line)
		result.Coverage.Truncated = result.Coverage.Truncated || truncated
		key := clean.Timestamp.String() + clean.Line
		if seen[key] { continue }
		seen[key] = true
		context = append(context, clean)
	}
	context = append(context, LogLine{Timestamp: first.Timestamp, Line: first.Line, Stream: first.Stream})
	first.Context = orderedDiagnosisLines(context)
	result.FirstFailure = &first
	return result
}
