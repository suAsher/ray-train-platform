package spkrayjob

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"text/tabwriter"

	"ray-train-platform-backend/domain"
)

func renderSubmitCommand(engine domain.TrainingEngine, runtime PlatformRuntimeLimits) string {
	command := "spk-rayjob submit"
	if runtime.ManagedAvailable() {
		command += " --engine " + string(engine.Resolved())
	}
	return command + " --watch"
}

// jobView is the subset of a job the CLI renders. Decoding into a narrow struct
// rather than a map keeps the output stable when the API adds fields.
type jobView struct {
	ID               string `json:"id"`
	TenantID         string `json:"tenantId"`
	UserID           string `json:"userId"`
	ObservedState    string `json:"observedState"`
	StatusReason     string `json:"statusReason"`
	StatusMessage    string `json:"statusMessage"`
	SubmissionOrigin string `json:"submissionOrigin"`
	RayJobName       string `json:"rayJobName"`
	CreatedAt        string `json:"createdAt"`
	FinishedAt       string `json:"finishedAt"`
	Spec             struct {
		Name           string                `json:"name"`
		Image          string                `json:"image"`
		TrainingEngine domain.TrainingEngine `json:"trainingEngine"`
		Execution      struct {
			Mode string `json:"mode"`
		} `json:"execution"`
		Resources struct {
			WorkerReplicas int `json:"workerReplicas"`
			GPUsPerWorker  int `json:"gpusPerWorker"`
		} `json:"resources"`
		Output struct {
			Space        string `json:"space"`
			RelativePath string `json:"relativePath"`
		} `json:"output"`
	} `json:"spec"`
}

var platformJobID = regexp.MustCompile(`^job-[0-9a-f]{24}$`)

type resumeCheckpointSelection struct {
	Location     projectLocation
	CheckpointID string
}

type jobPage struct {
	Items []jobView `json:"items"`
	Total int64     `json:"total"`
}

type logPayload struct {
	JobID string     `json:"jobId"`
	Items []LogEntry `json:"items"`
}

func renderJobTable(writer io.Writer, payload json.RawMessage) error {
	var page jobPage
	if err := json.Unmarshal(payload, &page); err != nil {
		return fmt.Errorf("decode job list")
	}
	if len(page.Items) == 0 {
		_, err := fmt.Fprintln(writer, "没有任务。使用 spk-rayjob submit 提交第一个任务。")
		return err
	}
	table := tabwriter.NewWriter(writer, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "JOB ID\tNAME\tSTATE\tGPU\tORIGIN\tCREATED")
	for _, job := range page.Items {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n",
			job.ID, orDash(job.Spec.Name), orDash(job.ObservedState),
			gpuSummary(job), orDash(job.SubmissionOrigin), shortTime(job.CreatedAt))
	}
	if err := table.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(writer, "\n共 %d 个任务。查看详情：spk-rayjob status <JOB ID>\n", page.Total)
	return err
}

func renderJobDetail(writer io.Writer, payload json.RawMessage) error {
	var job jobView
	if err := json.Unmarshal(payload, &job); err != nil {
		return fmt.Errorf("decode job")
	}
	table := tabwriter.NewWriter(writer, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"任务 ID", job.ID},
		{"名称", orDash(job.Spec.Name)},
		{"状态", orDash(job.ObservedState)},
		{"训练引擎", string(job.Spec.TrainingEngine.Resolved())},
		{"执行方式", orDash(job.Spec.Execution.Mode)},
		{"规模", gpuSummary(job)},
		{"镜像", orDash(job.Spec.Image)},
		{"结果目录", outputSummary(job)},
		{"提交方式", orDash(job.SubmissionOrigin)},
		{"创建时间", shortTime(job.CreatedAt)},
		{"结束时间", shortTime(job.FinishedAt)},
	}
	if message := strings.TrimSpace(job.StatusMessage); message != "" {
		rows = append(rows, [2]string{"状态说明", message})
	}
	if reason := strings.TrimSpace(job.StatusReason); reason != "" {
		rows = append(rows, [2]string{"状态原因", reason})
	}
	for _, row := range rows {
		fmt.Fprintf(table, "%s\t%s\n", row[0], row[1])
	}
	if err := table.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(writer, "\n查看日志：spk-rayjob logs -f %s\n", job.ID)
	return err
}

// renderLogLines prints only lines newer than the cursor and returns the new
// cursor. Follow polls a bounded endpoint, so without this every poll would
// reprint the whole buffer.
func renderLogLines(writer io.Writer, payload json.RawMessage, cursor string) (string, error) {
	var logs logPayload
	if err := json.Unmarshal(payload, &logs); err != nil {
		return cursor, fmt.Errorf("decode job logs")
	}
	return renderLogEntries(writer, logs.Items, cursor)
}

func renderLogEntries(writer io.Writer, entries []LogEntry, cursor string) (string, error) {
	latest := cursor
	for _, entry := range entries {
		if cursor != "" && entry.Timestamp <= cursor {
			continue
		}
		if _, err := fmt.Fprintln(writer, entry.Line); err != nil {
			return latest, err
		}
		if entry.Timestamp > latest {
			latest = entry.Timestamp
		}
	}
	return latest, nil
}

// isTerminalJobState decides when a watch stops. Anything else — including an
// empty state right after submission — means the platform is still working.
func isTerminalJobState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "SUCCEEDED", "FAILED", "CANCELED", "CANCELLED", "TIMED_OUT":
		return true
	default:
		return false
	}
}

// checkpointLocationForPreviousRun binds a resume to the first complete
// checkpoint returned by the owner-scoped API. ObjectPath is accepted only as
// proof of the exact managed checkpoint layout; the submitted logical path is
// derived from validated server identities rather than copied from that field.
func checkpointLocationForPreviousRun(payload json.RawMessage, page JobCheckpointPage) (resumeCheckpointSelection, error) {
	var job jobView
	if err := json.Unmarshal(payload, &job); err != nil {
		return resumeCheckpointSelection{}, fmt.Errorf("decode previous job")
	}
	job.ID = strings.TrimSpace(job.ID)
	job.TenantID = strings.TrimSpace(job.TenantID)
	job.UserID = strings.TrimSpace(job.UserID)
	if !platformJobID.MatchString(job.ID) || job.TenantID == "" || job.UserID == "" || page.JobID != job.ID {
		return resumeCheckpointSelection{}, fmt.Errorf("断点响应与父任务身份不一致")
	}
	if domain.DataSpaceID(strings.TrimSpace(job.Spec.Output.Space)) != domain.DataSpaceMyRuns {
		return resumeCheckpointSelection{}, fmt.Errorf("该任务没有平台管理的结果目录，无法作为断点来源")
	}
	outputRoot := strings.Trim(job.Spec.Output.RelativePath, "/")
	if outputRoot != "" {
		if _, err := domain.NewDataLocation(domain.DataSpaceMyRuns, outputRoot); err != nil {
			return resumeCheckpointSelection{}, fmt.Errorf("父任务结果目录无效")
		}
	}
	for _, checkpoint := range page.Items {
		if !checkpoint.Complete {
			continue
		}
		if checkpoint.JobID != job.ID || checkpoint.TenantID != job.TenantID || checkpoint.UserID != job.UserID || checkpoint.Validate() != nil {
			return resumeCheckpointSelection{}, fmt.Errorf("断点响应与父任务属主不一致或内容无效")
		}
		expectedObjectPath := path.Join(domain.DataMountOutputPath, ".platform", "ray-train", job.ID, "checkpoints", checkpoint.ID)
		if checkpoint.ObjectPath != expectedObjectPath {
			return resumeCheckpointSelection{}, fmt.Errorf("断点路径不属于父任务")
		}
		relativePath := path.Join(outputRoot, job.ID, ".platform", "ray-train", job.ID, "checkpoints", checkpoint.ID)
		location, err := domain.NewDataLocation(domain.DataSpaceMyRuns, relativePath)
		if err != nil {
			return resumeCheckpointSelection{}, fmt.Errorf("断点路径无效")
		}
		return resumeCheckpointSelection{
			Location:     projectLocation{Space: string(location.Space), Path: location.RelativePath},
			CheckpointID: checkpoint.ID,
		}, nil
	}
	return resumeCheckpointSelection{}, fmt.Errorf("父任务没有完整 checkpoint，无法恢复")
}

func gpuSummary(job jobView) string {
	workers, gpus := job.Spec.Resources.WorkerReplicas, job.Spec.Resources.GPUsPerWorker
	if workers <= 0 || gpus <= 0 {
		return "-"
	}
	if workers == 1 {
		return fmt.Sprintf("%d", gpus)
	}
	return fmt.Sprintf("%dx%d=%d", workers, gpus, workers*gpus)
}

func outputSummary(job jobView) string {
	if strings.TrimSpace(job.Spec.Output.Space) == "" {
		return "-"
	}
	path := strings.Trim(job.Spec.Output.RelativePath, "/")
	if path != "" {
		path += "/"
	}
	return job.Spec.Output.Space + "/" + path + job.ID
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// shortTime keeps the table narrow. The API returns RFC 3339; the date and
// minute are what a user scans for.
func shortTime(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	if len(trimmed) >= 16 {
		return strings.Replace(trimmed[:16], "T", " ", 1)
	}
	return trimmed
}
