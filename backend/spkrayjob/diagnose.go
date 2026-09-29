package spkrayjob

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"ray-train-platform-backend/observability"
)

// Diagnose reads the same bounded evidence as Portal; the CLI never re-scans
// full logs or reclassifies an error independently of the control plane.
func (client *Client) Diagnose(ctx context.Context, jobID string) (json.RawMessage, error) {
	return client.request(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID)+"/diagnosis", nil, nil)
}

func runDiagnose(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	client, jobID, format, err := parseJobCommand("diagnose", arguments, getenv, stderr)
	if err != nil { return err }
	data, err := client.Diagnose(ctx, jobID)
	if err != nil { return err }
	if format.json { return writeJSON(stdout, data) }
	var diagnosis observability.JobDiagnosis
	if err := json.Unmarshal(data, &diagnosis); err != nil { return fmt.Errorf("decode job diagnosis") }
	return renderDiagnosis(stdout, diagnosis)
}

func renderDiagnosis(writer io.Writer, diagnosis observability.JobDiagnosis) error {
	if _, err := fmt.Fprintf(writer, "任务 %s · %s\n诊断分类: %s · 阶段线索: %s\n%s\n", diagnosis.JobID, diagnosis.ObservedState, diagnosis.Classification, diagnosis.FailurePhase, diagnosis.Summary); err != nil { return err }
	if diagnosis.StatusReason != "" || diagnosis.StatusMessage != "" {
		if _, err := fmt.Fprintf(writer, "平台状态原因: %s\n平台状态说明: %s\n", diagnosis.StatusReason, diagnosis.StatusMessage); err != nil { return err }
	}
	if first := diagnosis.FirstFailure; first != nil {
		if err := renderDiagnosisEvidence(writer, "保留日志中最早识别的错误（非已证明根因）", *first); err != nil { return err }
		for _, line := range first.Context {
			if _, err := fmt.Fprintf(writer, "  %s %s\n", line.Timestamp.Format("2006-01-02T15:04:05Z07:00"), line.Line); err != nil { return err }
		}
	}
	for _, evidence := range diagnosis.Followups { if err := renderDiagnosisEvidence(writer, "后续线索", evidence); err != nil { return err } }
	for _, evidence := range diagnosis.CompletionEvidence { if err := renderDiagnosisEvidence(writer, "完成/Checkpoint 文字（未验证）", evidence); err != nil { return err } }
	coverage := diagnosis.Coverage
	if _, err := fmt.Fprintf(writer, "覆盖范围: filtered=%t partial=%t truncated=%t logUnavailable=%t contextUnavailable=%t completionUnavailable=%t followupsUnavailable=%t\n", coverage.Filtered, coverage.Partial, coverage.Truncated, coverage.LogUnavailable, coverage.ContextUnavailable, coverage.CompletionUnavailable, coverage.FollowupsUnavailable); err != nil { return err }
	for _, note := range diagnosis.Notes { if _, err := fmt.Fprintf(writer, "提示: %s\n", note); err != nil { return err } }
	return nil
}

func renderDiagnosisEvidence(writer io.Writer, label string, evidence observability.DiagnosisEvidence) error {
	_, err := fmt.Fprintf(writer, "%s [%s] %s %s/%s\n  %s\n", label, evidence.Kind, evidence.Timestamp.Format("2006-01-02T15:04:05Z07:00"), evidence.Stream["pod"], evidence.Stream["container"], evidence.Line)
	return err
}
