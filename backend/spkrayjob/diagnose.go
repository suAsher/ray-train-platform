package spkrayjob

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/observability"
)

// Diagnose reads the same bounded evidence as Portal; the CLI never re-scans
// full logs or reclassifies an error independently of the control plane.
func (client *Client) Diagnose(ctx context.Context, jobID string) (json.RawMessage, error) {
	return client.request(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID)+"/diagnosis", nil, nil)
}

func runDiagnose(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	client, jobID, format, err := parseJobCommand("diagnose", arguments, getenv, stderr)
	if err != nil {
		return err
	}
	data, err := client.Diagnose(ctx, jobID)
	if err != nil {
		return err
	}
	if format.json {
		return writeJSON(stdout, data)
	}
	var diagnosis observability.JobDiagnosis
	if err := json.Unmarshal(data, &diagnosis); err != nil {
		return fmt.Errorf("decode job diagnosis")
	}
	return renderDiagnosis(stdout, diagnosis)
}

func renderDiagnosis(writer io.Writer, diagnosis observability.JobDiagnosis) error {
	if _, err := fmt.Fprintf(writer, "任务 %s · %s\n诊断分类: %s · 阶段线索: %s\n%s\n", diagnosis.JobID, diagnosis.ObservedState, diagnosis.Classification, diagnosis.FailurePhase, diagnosis.Summary); err != nil {
		return err
	}
	if diagnosis.StatusReason != "" || diagnosis.StatusMessage != "" {
		if _, err := fmt.Fprintf(writer, "平台状态原因: %s\n平台状态说明: %s\n", diagnosis.StatusReason, diagnosis.StatusMessage); err != nil {
			return err
		}
	}
	if err := renderSchedulingCapacity(writer, diagnosis.SchedulingCapacity); err != nil {
		return err
	}
	if first := diagnosis.FirstFailure; first != nil {
		if err := renderDiagnosisEvidence(writer, "保留日志中最早识别的错误（非已证明根因）", *first); err != nil {
			return err
		}
		for _, line := range first.Context {
			if _, err := fmt.Fprintf(writer, "  %s %s\n", line.Timestamp.Format("2006-01-02T15:04:05Z07:00"), line.Line); err != nil {
				return err
			}
		}
	}
	for _, evidence := range diagnosis.Followups {
		if err := renderDiagnosisEvidence(writer, "后续线索", evidence); err != nil {
			return err
		}
	}
	for _, evidence := range diagnosis.CompletionEvidence {
		if err := renderDiagnosisEvidence(writer, "完成/Checkpoint 文字（未验证）", evidence); err != nil {
			return err
		}
	}
	coverage := diagnosis.Coverage
	if _, err := fmt.Fprintf(writer, "覆盖范围: filtered=%t partial=%t truncated=%t logUnavailable=%t contextUnavailable=%t completionUnavailable=%t followupsUnavailable=%t\n", coverage.Filtered, coverage.Partial, coverage.Truncated, coverage.LogUnavailable, coverage.ContextUnavailable, coverage.CompletionUnavailable, coverage.FollowupsUnavailable); err != nil {
		return err
	}
	for _, note := range diagnosis.Notes {
		if _, err := fmt.Fprintf(writer, "提示: %s\n", note); err != nil {
			return err
		}
	}
	return nil
}

func renderDiagnosisEvidence(writer io.Writer, label string, evidence observability.DiagnosisEvidence) error {
	_, err := fmt.Fprintf(writer, "%s [%s] %s %s/%s\n  %s\n", label, evidence.Kind, evidence.Timestamp.Format("2006-01-02T15:04:05Z07:00"), evidence.Stream["pod"], evidence.Stream["container"], evidence.Line)
	return err
}

func renderSchedulingCapacity(writer io.Writer, capacity *domain.SchedulingCapacity) error {
	if capacity == nil {
		return nil
	}
	if !capacity.Available {
		_, err := fmt.Fprintf(writer, "调度容量未知: %s（观测时间 %s）\n", capacity.UnavailableReason, capacity.ObservedAt.Format("2006-01-02T15:04:05Z07:00"))
		return err
	}
	if capacity.Quota != nil {
		if _, err := fmt.Fprintf(writer, "团队额度: 上限 %d · 已占用 %d · 本任务可用额度 %d（已扣除本任务自身占额）\n", capacity.Quota.GPULimit, capacity.Quota.GPUUsed, capacity.Quota.GPUAvailable); err != nil {
			return err
		}
	}
	pool, fit := capacity.Pool, capacity.Fit
	if _, err := fmt.Fprintf(writer, "资源池 %s: 总卡 %d · 已分配 %d · 未落地预留 %d · 空闲 %d\n请求规格: %d Worker × %d GPU · 每 Worker %d CPU / %s\n规格匹配: %d 节点 · %d Worker 槽位 · 当前可放置 %t\n调度参考: %s · 观测时间 %s\n", capacity.AcceleratorClass, pool.TotalGPUs, pool.AllocatedGPUs, pool.ReservedGPUs, pool.FreeGPUs, fit.WorkerReplicas, fit.GPUsPerWorker, fit.CPUPerWorker, fit.MemoryPerWorker, fit.FittingNodes, fit.WorkerSlots, fit.CanFit, fit.Reason, capacity.ObservedAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
		return err
	}
	message := "容量为瞬时参考，不保证排队顺序或准入；启动错误请以上方平台状态为准。"
	if fit.Reason == "already_admitted_capacity_reference" {
		message = "任务已准入，当前池容量仅供参考，不能据此判定该任务缺卡；请检查启动、镜像和挂载状态。"
	} else if fit.Reason == "startup_status_capacity_reference" {
		message = "平台已记录具体启动错误，当前池容量仅供参考，请优先排查镜像、容器和挂载状态。"
	} else if fit.Reason == "admission_unknown_capacity_reference" {
		message = "任务准入状态暂不可确认，当前池容量仅供参考，不能作为排队或启动失败的已证明原因。"
	}
	_, err := fmt.Fprintf(writer, "提示: %s\n", message)
	return err
}
