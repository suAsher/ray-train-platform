package observability

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDiagnosisFiltersAndKindsRecognizeConcreteIncidentEvidence(t *testing.T) {
	filter := regexp.MustCompile(diagnosisFailureFilter)
	for _, test := range []struct{ line, kind string }{
		{"AssertionError: Translation may not be NaN!", "numerical_error"},
		{"KeyError: Qtractor", "python_exception"},
		{"\x1b[31mKeyError: Qtractor\x1b[0m", "python_exception"},
		{"torch.OutOfMemoryError: CUDA out of memory", "out_of_memory"},
		{"cls_score contains NaN", "numerical_error"},
		{"Fatal Python error: Segmentation fault", "fatal_signal"},
		{"NCCL peer closed", "communication_error"},
		{"ALLREDUCE timed out", "communication_error"},
		{"Watchdog caught collective operation timeout", "communication_error"},
	} {
		if !filter.MatchString(test.line) || diagnosisKind(test.line) != test.kind {
			t.Fatalf("missed concrete evidence %q: %q", test.line, diagnosisKind(test.line))
		}
	}
	if filter.MatchString("Saving checkpoint to epoch_100.pth") {
		t.Fatal("checkpoint can starve error budget")
	}
}

func TestDiagnosisPreservesFailedStateAndDoesNotInferTeardownFromCheckpointAlone(t *testing.T) {
	start := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	base := JobDiagnosis{ObservedState: "FAILED", StatusReason: "RuntimeFailure", StatusMessage: "exit code 139 token=fixture-secret"}
	fatal := []LogLine{{Timestamp: start.Add(time.Minute), Line: "Fatal Python error: Segmentation fault"}}
	for _, test := range []struct{ text, class string }{{"Training completed", "possible_runtime_teardown"}, {"Saving checkpoint to epoch_1.pth", "runtime_failure"}} {
		result := AnalyzeDiagnosis(base, fatal, []LogLine{{Timestamp: start, Line: test.text}}, nil)
		if result.ObservedState != "FAILED" || result.Classification != test.class || result.StatusReason != base.StatusReason || strings.Contains(result.StatusMessage, "fixture-secret") {
			t.Fatalf("incorrect status/phase: %+v", result)
		}
	}
}

func TestDiagnosisRedactsSecretsAndBoundsWorstCaseJSON(t *testing.T) {
	secret := `KeyError: {"password":"fixture-password", "api_key":"fixture-api"} Authorization: Bearer fixture-bearer https://x/y?X-Tos-Signature=fixture-signature Authorization: Basic dXNlcjpwYXNz https://fixture-user:fixture-password@host/path`
	redacted, _ := RedactDiagnosisText(secret)
	for _, value := range []string{"fixture-password", "fixture-api", "fixture-bearer", "fixture-signature", "dXNlcjpwYXNz", "fixture-user"} {
		if strings.Contains(redacted, value) {
			t.Fatalf("secret leak: %s", redacted)
		}
	}
	start := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	labels := make(map[string]string)
	for _, key := range []string{"pod", "container", "stream", "namespace", "node", "platform_job_id"} {
		labels[key] = strings.Repeat("<", 1000)
	}
	var candidates, completions, followups, context []LogLine
	for index := 0; index < 200; index++ {
		entry := LogLine{Timestamp: start.Add(time.Duration(index) * time.Second), Line: "KeyError: " + strings.Repeat("<", 6000), Stream: labels}
		candidates = append(candidates, entry)
		context = append(context, entry)
		completions = append(completions, LogLine{Timestamp: entry.Timestamp, Line: "Saving checkpoint " + strings.Repeat("<", 6000), Stream: labels})
		followups = append(followups, LogLine{Timestamp: entry.Timestamp.Add(time.Hour), Line: "NCCL timeout " + strings.Repeat("<", 6000), Stream: labels})
	}
	result := AnalyzeDiagnosis(JobDiagnosis{StatusMessage: strings.Repeat("<", 6000), StatusReason: strings.Repeat("<", 6000)}, candidates, completions, followups)
	result = WithDiagnosisContext(result, context)
	encoded, err := json.Marshal(map[string]any{"success": true, "data": result})
	if err != nil || len(encoded) >= 1<<20 {
		t.Fatalf("generic API limit exceeded: %d (%v)", len(encoded), err)
	}
}

func TestDiagnosisChineseCompletionBeforeFatalMatchesIncidentWithoutChangingFailedState(t *testing.T) {
	completed := time.Date(2026, 9, 29, 12, 20, 52, 0, time.UTC)
	filter := regexp.MustCompile(diagnosisCompletionFilter)
	var completions []LogLine
	for _, text := range []string{"训练进程正常结束", "训练完成，找到目标 checkpoint", "Fusion训练完成", "全流程完成"} {
		if !filter.MatchString(text) || diagnosisKind(text) != "completion" {
			t.Fatalf("actual completion evidence missed: %s", text)
		}
		completions = append(completions, LogLine{Timestamp: completed, Line: text})
	}
	for _, text := range []string{"预处理完成", "数据加载完成", "第一个 epoch 完成", "准备训练完成标志"} {
		if filter.MatchString(text) || diagnosisKind(text) != "" {
			t.Fatalf("routine progress promoted to completion: %s", text)
		}
	}
	fatal := []LogLine{{Timestamp: completed.Add(4 * time.Second), Line: "Fatal Python error: Segmentation fault"}}
	result := AnalyzeDiagnosis(JobDiagnosis{JobID: "job-303f-fixture", ObservedState: "FAILED", StatusReason: "JobFailed", StatusMessage: "exited with code 139"}, fatal, completions, nil)
	if result.ObservedState != "FAILED" || result.Classification != "possible_runtime_teardown" || result.FailurePhase != "launcher" || len(result.CompletionEvidence) != 4 || result.FirstFailure == nil || result.FirstFailure.Kind != "fatal_signal" {
		t.Fatalf("incorrect incident diagnosis: %+v", result)
	}
	if !strings.Contains(result.Summary, "可能") || !strings.Contains(result.Summary, "根因") || !strings.Contains(strings.Join(result.Notes, " "), "未经验证") {
		t.Fatalf("uncertainty must remain explicit: %+v", result)
	}
}
