package observability

import (
	"sort"
	"strings"
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
		"First recognized error in retained logs is evidence, not a proven root cause; logging may be delayed, caught exceptions may be benign, and earlier logs may be absent.",
		"This is a filtered, bounded sample; no recognized failure does not establish success. Job state remains authoritative.",
	}
	result.Coverage.Filtered, result.Coverage.Partial = true, true
	result.Coverage.CandidateLimit = DiagnosisCandidateLimit
	result.StatusReason, _ = RedactDiagnosisText(base.StatusReason)
	result.StatusMessage, _ = RedactDiagnosisText(base.StatusMessage)
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
	result.Summary = "No recognized failure in this filtered retained-log sample; this does not establish success."
	if result.Coverage.LogUnavailable {
		result.Classification = "unavailable"
		result.Summary = "Retained log evidence is unavailable; use the authoritative job state and retry diagnosis later."
		return result
	}
	if len(result.CompletionEvidence) > 0 { result.Notes = append(result.Notes, "Completion and checkpoint text is unverified log evidence; it does not verify a usable artifact or successful process exit.") }
	if result.FirstFailure == nil { return result }
	result.Summary = "First recognized error in retained logs; not a proven root cause."
	switch result.FirstFailure.Kind {
	case "numerical_error", "python_exception", "out_of_memory":
		result.Classification, result.FailurePhase = "application_failure", "application"
		result.Notes = append(result.Notes, "Application phase is inferred from the error text; it may be loading or preprocessing rather than training computation.")
	case "communication_error":
		result.Classification = "communication_failure"
		result.Notes = append(result.Notes, "Communication errors can be downstream symptoms; no earlier recognized concrete error was found in this sample.")
	case "fatal_signal":
		result.Classification = "runtime_failure"
		for _, completion := range result.CompletionEvidence {
			if completion.Kind == "completion" && completion.Timestamp.Before(result.FirstFailure.Timestamp) {
				result.Classification, result.FailurePhase = "possible_runtime_teardown", "launcher"
				result.Summary = "First recognized error in retained logs follows completion text: possible runtime teardown failure, not a proven root cause or successful training."
				result.Notes = append(result.Notes, "Launcher/teardown phase is inferred from ordering only; completion text is unverified and may come from another worker.")
				break
			}
		}
	}
	if len(result.Followups) > 0 { result.Notes = append(result.Notes, "Later communication/fatal messages are follow-up evidence; temporal ordering does not prove causality.") }
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
	if len(lines) >= DiagnosisContextLimit { result.Coverage.Truncated = true }
	for _, line := range orderedDiagnosisLines(lines) {
		if len(context) >= DiagnosisContextLimit-1 { break }
		if line.Timestamp.Equal(first.Timestamp) && strings.TrimSpace(line.Line) == first.Line { continue }
		clean, truncated := RedactDiagnosisLogLine(line)
		result.Coverage.Truncated = result.Coverage.Truncated || truncated
		context = append(context, clean)
	}
	context = append(context, LogLine{Timestamp: first.Timestamp, Line: first.Line, Stream: first.Stream})
	first.Context = orderedDiagnosisLines(context)
	result.FirstFailure = &first
	return result
}
