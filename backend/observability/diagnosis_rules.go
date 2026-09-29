package observability

import (
	"regexp"
	"strings"
)

// The Loki filter is deliberately broader than diagnosisKind: only concrete
// recognized error text becomes evidence. Routine checkpoints have a separate
// budget so they cannot hide an early failure from the forward query.
const diagnosisFailureFilter = `(?i)([A-Za-z]+Error:|[A-Za-z]+Exception:|loss[ :=]+[+-]?(nan|inf)|contains (nan|inf)|not be NaN|non[- ]finite|out of memory|NCCL.*(error|fail|timeout|timed out|watchdog|closed|abort)|(?:watchdog|ALLREDUCE).*(timeout|timed out)|connection (closed|reset) by peer|fatal python error|segmentation fault|SIGSEGV|SIGABRT|core dumped)`
const diagnosisCompletionFilter = `(?i)(training (completed|finished)|training loop completed|all epochs completed|训练进程正常结束|训练完成，找到目标 checkpoint|Fusion训练完成|全流程完成)`
const diagnosisCheckpointFilter = `(?i)(saving checkpoint|checkpoint saved|saved checkpoint)`
const diagnosisFollowupFilter = `(?i)(NCCL.*(error|fail|timeout|timed out|watchdog|closed|abort)|(?:watchdog|ALLREDUCE).*(timeout|timed out)|connection (closed|reset) by peer|fatal python error|segmentation fault|SIGSEGV|SIGABRT|core dumped)`

var (
	diagnosisBenign = regexp.MustCompile(`(?i)(\bexcept\s+|\b(?:caught|handled|expected)\s+[A-Za-z.]*Error\b|\b(?:example|documentation)\s*:)`)
	diagnosisNumerical = regexp.MustCompile(`(?i)(\bloss\s*[:=]\s*[+-]?(?:nan|inf)(?:\b|$)|\bnot be nan\b|\bnon[- ]finite (?:loss|gradient)|\b(?:loss|gradient) (?:is|became) (?:nan|inf)\b|\b[A-Za-z_]\w* contains (?:nan|inf)\b)`)
	diagnosisPython = regexp.MustCompile(`(?:^|\s|\])(?:[A-Za-z_][A-Za-z0-9_]*\.)*[A-Za-z_][A-Za-z0-9_]*(?:Error|Exception):`)
	diagnosisCommunication = regexp.MustCompile(`(?i)(\bNCCL\b.*\b(?:error|failed|failure|timeout|timed out|watchdog|closed|abort)\b|\b(?:watchdog|ALLREDUCE)\b.*\b(?:timeout|timed out)\b|\bconnection (?:closed|reset) by peer\b)`)
	diagnosisFatal = regexp.MustCompile(`(?i)(\bfatal python error:|\bsegmentation fault\b|\bSIGSEGV\b|\bSIGABRT\b|\bcore dumped\b)`)
	diagnosisCompleted = regexp.MustCompile(`(?i)\b(?:training (?:completed|finished)|training loop completed|all epochs completed)\b`)
	diagnosisChineseCompleted = regexp.MustCompile(`训练进程正常结束|训练完成，找到目标 checkpoint|Fusion训练完成|全流程完成`)
	diagnosisCheckpoint = regexp.MustCompile(`(?i)\b(?:saving checkpoint|checkpoint saved|saved checkpoint)\b`)
)

func diagnosisKind(value string) string {
	value = cleanDiagnosisControlText(value)
	if diagnosisBenign.MatchString(value) { return "" }
	if diagnosisFatal.MatchString(value) { return "fatal_signal" }
	lower := strings.ToLower(value)
	if strings.Contains(lower, "outofmemoryerror:") || strings.Contains(lower, "cuda out of memory") || strings.Contains(lower, "out of memory: killed process") { return "out_of_memory" }
	if diagnosisCommunication.MatchString(value) { return "communication_error" }
	if diagnosisNumerical.MatchString(value) { return "numerical_error" }
	if diagnosisPython.MatchString(value) { return "python_exception" }
	if diagnosisCompleted.MatchString(value) || diagnosisChineseCompleted.MatchString(value) { return "completion" }
	if diagnosisCheckpoint.MatchString(value) { return "checkpoint" }
	return ""
}
