package spkrayjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const completionHelpText = `用法：spk-rayjob completion bash|zsh
输出补全脚本，不修改任何 shell 配置。

Bash（包括 macOS Bash 3.2）：
  source <(spk-rayjob completion bash)
Zsh：
  autoload -Uz compinit && compinit
  source <(spk-rayjob completion zsh)

命令、参数、枚举与本地路径可离线补全。任务 ID 每次通过当前登录身份的
任务列表读取（最多 100 条，750ms 超时）；未登录、无权限或网络失败时静默返回。
不缓存任务列表，切换服务器、登录身份或团队后不会复用旧候选。
`

func runCompletion(arguments []string, stdout io.Writer) error {
	if len(arguments) != 1 {
		return errors.New("usage: spk-rayjob completion bash|zsh")
	}
	var script string
	switch arguments[0] {
	case "bash":
		script = bashCompletionScript
	case "zsh":
		script = zshCompletionScript
	default:
		return errors.New("completion supports bash or zsh")
	}
	_, err := io.WriteString(stdout, script)
	return err
}

type completionCandidate struct {
	value       string
	description string
}

type completionOption struct {
	name string
	kind string
}

var completionCommands = map[string]string{
	"login": "登录", "login-check": "检查当前登录", "upgrade": "升级客户端",
	"init": "生成项目配置", "package": "打包源码", "submit": "提交训练任务",
	"jobs": "列出可见任务", "images": "列出训练镜像", "datasets": "列出数据集",
	"dataset": "查询数据集版本", "source-artifact": "查询源码上传",
	"status": "查看任务", "logs": "查看日志", "diagnose": "诊断训练任务",
	"connect": "连接训练 Worker", "cancel": "停止任务",
	"completion": "生成 shell 补全脚本", "version": "客户端版本", "help": "命令帮助",
}

var completionEnums = map[string]string{
	"output": "text json", "engine": "ray-ddp ray-train",
	"data-mode": "mount cache ray-data-stage ray-data streaming",
	"accelerator": "rtx4090 a100 a800 h20", "priority": "production normal opportunistic",
	"dataset-cache-policy": "off auto bounded", "dataset-version": "latest",
	"execution-mode": "auto single_gpu torchrun ray_train", "cache-mode": "off runtime",
	"cache-preload": "input", "ray-data-format": "parquet images",
	"input-space": "my-storage my-files my-runs team-shared public idc-original idc-wellspiking idc-shared idc-spk-hybrid idc-spk-ssd",
	"checkpoint-space": "my-storage my-files my-runs team-shared public idc-original idc-wellspiking idc-shared idc-spk-hybrid idc-spk-ssd",
	"state": "SUBMITTED VALIDATING QUEUED ADMITTED PROVISIONING RUNNING RECOVERING SUCCEEDED FAILED CANCELING CANCELED TIMED_OUT UNKNOWN DELETING",
}

// This private protocol contains literal tab-separated values, never shell code.
// Query errors are deliberately silent: Tab must not interrupt an offline shell.
func runCompletionQuery(ctx context.Context, words []string, stdout io.Writer, getenv func(string) string) error {
	candidates := completeWords(ctx, words, getenv)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].value < candidates[j].value })
	last := ""
	for _, candidate := range candidates {
		if candidate.value == "" || candidate.value == last || !safeCompletionValue(candidate.value) {
			continue
		}
		if _, err := fmt.Fprintf(stdout, "%s\t%s\n", candidate.value, completionDescription(candidate.description)); err != nil {
			return err
		}
		last = candidate.value
	}
	return nil
}

func completeWords(ctx context.Context, words []string, getenv func(string) string) []completionCandidate {
	if len(words) == 0 {
		words = []string{""}
	}
	if len(words) == 1 {
		return completionCommandNames(words[0])
	}
	command, arguments := words[0], words[1:]
	if command == "help" {
		if len(arguments) == 1 {
			return completionCommandNames(arguments[0])
		}
		if len(arguments) == 2 {
			subcommand := map[string]string{"dataset": "versions", "source-artifact": "resolve"}[arguments[0]]
			if subcommand != "" {
				return completionChoices([]string{subcommand}, arguments[1], "")
			}
		}
		return nil
	}
	if command == "completion" && len(arguments) == 1 && !strings.HasPrefix(arguments[0], "-") {
		return completionChoices([]string{"bash", "zsh"}, arguments[0], "")
	}
	if subcommand := map[string]string{"dataset": "versions", "source-artifact": "resolve"}[command]; subcommand != "" {
		if len(arguments) == 1 {
			return completionChoices([]string{subcommand}, arguments[0], "")
		}
		if arguments[0] != subcommand {
			return nil
		}
		command, arguments = command+" "+subcommand, arguments[1:]
	}
	options := completionOptions(command)
	if len(options) == 0 {
		return nil
	}
	state := parseCompletionArguments(command, arguments[:len(arguments)-1], options)
	return completeArgument(ctx, command, arguments[len(arguments)-1], state, options, getenv)
}

func completionCommandNames(prefix string) []completionCandidate {
	var candidates []completionCandidate
	for name, description := range completionCommands {
		if strings.HasPrefix(name, prefix) {
			candidates = append(candidates, completionCandidate{name, description})
		}
	}
	if strings.HasPrefix("--help", prefix) {
		candidates = append(candidates, completionCandidate{"--help", "命令帮助"})
	}
	return candidates
}

func completionOptions(command string) []completionOption {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	if _, exists := completionCommands[parts[0]]; !exists {
		return nil
	}
	options := []completionOption{{"help", "bool"}, {"h", "bool"}}
	switch command {
	case "login", "login-check", "submit", "source-artifact resolve", "jobs", "images", "datasets", "dataset versions", "status", "logs", "connect", "cancel", "diagnose":
		options = addCompletionOptions(options, "", "server")
		options = addCompletionOptions(options, "file", "config ca-file")
		options = addCompletionOptions(options, "bool", "debug")
	case "upgrade":
		options = addCompletionOptions(options, "", "server")
		options = addCompletionOptions(options, "file", "config ca-file")
	}
	switch command {
	case "submit", "source-artifact resolve", "jobs", "images", "datasets", "dataset versions", "status", "logs", "cancel", "diagnose":
		options = addCompletionOptions(options, "", "output")
	}
	switch command {
	case "init", "submit":
		options = addCompletionOptions(options, "dir", "dir")
		options = addCompletionOptions(options, "", "name image entrypoint engine data-mode accelerator priority gpus-per-worker workers")
		options = addCompletionOptions(options, "bool", "preemptible")
	case "package":
		options = addCompletionOptions(options, "dir", "dir")
		options = addCompletionOptions(options, "file", "output")
	case "login":
		options = addCompletionOptions(options, "", "username")
		options = addCompletionOptions(options, "bool", "token-stdin password-stdin")
	case "jobs":
		options = addCompletionOptions(options, "", "state limit")
	case "logs":
		options = addCompletionOptions(options, "", "limit")
		options = addCompletionOptions(options, "bool", "follow f")
	case "connect":
		options = addCompletionOptions(options, "", "worker")
	case "source-artifact resolve":
		options = addCompletionOptions(options, "", "request-id")
	}
	if command == "submit" {
		options = addCompletionOptions(options, "", "source-request-id dataset dataset-version dataset-cache-policy dataset-sites max-failures checkpoint-every-epochs checkpoint-keep-latest checkpoint-keep-best cpu-per-worker memory-per-worker execution-mode cache-mode cache-size cache-preload ray-data-format ray-data-path input-space input-path checkpoint-space checkpoint-path output-path")
		options = addCompletionOptions(options, "job", "resume-from-job")
		options = addCompletionOptions(options, "bool", "watch")
	}
	return options
}

func addCompletionOptions(options []completionOption, kind, names string) []completionOption {
	for _, name := range strings.Fields(names) {
		options = append(options, completionOption{name, kind})
	}
	return options
}

type completionArgumentState struct {
	connection   connectionFlags
	pending      *completionOption
	positionals  int
	flagsAllowed bool
	invalid      bool
}

func parseCompletionArguments(command string, arguments []string, options []completionOption) completionArgumentState {
	state := completionArgumentState{flagsAllowed: true}
	for index, argument := range arguments {
		if state.pending != nil {
			state.connection = completionConnection(state.connection, state.pending.name, argument)
			state.pending = nil
			continue
		}
		if argument == "--" && state.flagsAllowed {
			state.flagsAllowed = false
			continue
		}
		if !strings.HasPrefix(argument, "-") || !state.flagsAllowed {
			state.positionals++
			state.flagsAllowed = command == "connect" && index == 0
			continue
		}
		name, value, assigned := strings.Cut(strings.TrimLeft(argument, "-"), "=")
		option, found := findCompletionOption(options, name)
		if !found {
			state.invalid = true
			return state
		}
		if option.kind == "bool" {
			if assigned {
				if _, err := strconv.ParseBool(value); err != nil {
					state.invalid = true
				}
			}
		} else if assigned {
			state.connection = completionConnection(state.connection, name, value)
		} else {
			state.pending = &option
		}
	}
	return state
}

func completionConnection(connection connectionFlags, name, value string) connectionFlags {
	switch name {
	case "server":
		connection.server = value
	case "config":
		connection.config = value
	case "ca-file":
		connection.caFile = value
	}
	// --debug is intentionally ignored so completion never prints diagnostics.
	return connection
}

func findCompletionOption(options []completionOption, name string) (completionOption, bool) {
	for _, option := range options {
		if option.name == name {
			return option, true
		}
	}
	return completionOption{}, false
}

func completeArgument(ctx context.Context, command, current string, state completionArgumentState, options []completionOption, getenv func(string) string) []completionCandidate {
	if state.invalid || state.positionals > 1 {
		return nil
	}
	if state.pending != nil {
		return completeOptionValue(ctx, *state.pending, current, state.connection, getenv)
	}
	if state.flagsAllowed && strings.HasPrefix(current, "-") {
		if name, value, assigned := strings.Cut(current, "="); assigned {
			option, found := findCompletionOption(options, strings.TrimLeft(name, "-"))
			if !found {
				return nil
			}
			candidates := completeOptionValue(ctx, option, value, state.connection, getenv)
			for index, candidate := range candidates {
				candidates[index] = completionCandidate{name + "=" + candidate.value, candidate.description}
			}
			return candidates
		}
		return completionOptionNames(options, current)
	}
	if state.positionals != 0 {
		return nil
	}
	switch command {
	case "status", "logs", "cancel", "connect", "diagnose":
		return completeVisibleJobs(ctx, state.connection, current, getenv)
	}
	if state.flagsAllowed && current == "" {
		return completionOptionNames(options, "")
	}
	return nil
}

func completionOptionNames(options []completionOption, prefix string) []completionCandidate {
	var candidates []completionCandidate
	for _, option := range options {
		name := "--" + option.name
		if len(option.name) == 1 {
			name = "-" + option.name
		}
		if strings.HasPrefix(name, prefix) {
			candidates = append(candidates, completionCandidate{value: name})
		}
	}
	return candidates
}

func completeOptionValue(ctx context.Context, option completionOption, prefix string, connection connectionFlags, getenv func(string) string) []completionCandidate {
	switch option.kind {
	case "file", "dir":
		return completeLocalFiles(prefix, option.kind == "dir")
	case "job":
		return completeVisibleJobs(ctx, connection, prefix, getenv)
	case "bool":
		return completionChoices([]string{"true", "false"}, prefix, "")
	}
	return completionChoices(strings.Fields(completionEnums[option.name]), prefix, "")
}

func completionChoices(choices []string, prefix, description string) []completionCandidate {
	var candidates []completionCandidate
	for _, choice := range choices {
		if strings.HasPrefix(choice, prefix) {
			candidates = append(candidates, completionCandidate{choice, description})
		}
	}
	return candidates
}

func completeLocalFiles(prefix string, directoriesOnly bool) []completionCandidate {
	if !safeCompletionValue(prefix) {
		return nil
	}
	if strings.HasPrefix(prefix, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		prefix = home + string(filepath.Separator) + strings.TrimPrefix(prefix, "~/")
	}
	directory, base := filepath.Split(prefix)
	searchDirectory := directory
	if searchDirectory == "" {
		searchDirectory = "."
	}
	entries, err := os.ReadDir(searchDirectory)
	if err != nil {
		return nil
	}
	var candidates []completionCandidate
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), base) || !safeCompletionValue(entry.Name()) {
			continue
		}
		value := directory + entry.Name()
		info, err := os.Stat(value)
		if err != nil || (directoriesOnly && !info.IsDir()) {
			continue
		}
		if info.IsDir() {
			value += string(filepath.Separator)
		}
		candidates = append(candidates, completionCandidate{value: value})
		if len(candidates) == 100 {
			break
		}
	}
	return candidates
}

func completeVisibleJobs(ctx context.Context, connection connectionFlags, prefix string, getenv func(string) string) []completionCandidate {
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	client, err := newCommandClient(connection, getenv, io.Discard)
	if err != nil {
		return nil
	}
	data, err := client.ListJobs(ctx, "", 100)
	if err != nil {
		return nil
	}
	var page jobPage
	if err := json.Unmarshal(data, &page); err != nil {
		return nil
	}
	var candidates []completionCandidate
	for _, job := range page.Items {
		if platformJobID.MatchString(job.ID) && strings.HasPrefix(job.ID, prefix) {
			description := strings.TrimSpace(job.ObservedState + " " + job.Spec.Name)
			description = strings.ReplaceAll(description, client.token, "[redacted]")
			candidates = append(candidates, completionCandidate{job.ID, description})
		}
		if len(candidates) == 100 {
			break
		}
	}
	return candidates
}

func safeCompletionValue(value string) bool {
	return !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

func completionDescription(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > 160 {
		runes = runes[:160]
	}
	return string(runes)
}

const bashCompletionScript = `# spk-rayjob completion; Bash 3.2 or newer. No persistent cache.
_spk_rayjob() {
    local value description word previous trim join_next i last
    local -a arguments
    arguments=()
    COMPREPLY=()
    trim=''
    join_next=''
    # Bash splits '=' and ':' (including HTTPS URLs) in COMP_WORDS.
    # Rejoin these boundaries without parsing or evaluating shell text.
    for ((i=1; i<=COMP_CWORD; i++)); do
        word="${COMP_WORDS[i]}"
        last=$((${#arguments[@]} - 1))
        previous=''
        if ((last >= 0)); then previous="${arguments[last]}"; fi
        if [[ "$word" == '=' && "$previous" == -* && "$previous" != *=* ]]; then
            arguments[last]="$previous="
            join_next=1
        elif [[ "$word" == ':' && $last -ge 0 ]]; then
            arguments[last]="$previous:"
            join_next=1
        elif [[ -n "$join_next" || "$previous" == -*= ]]; then
            arguments[last]="$previous$word"
            if ((i == COMP_CWORD)); then trim="$previous"; fi
            join_next=''
        else
            arguments+=("$word")
        fi
    done
    while IFS=$'\t' read -r value description; do
        [[ -n "$value" ]] || continue
        if [[ -n "$trim" && "$value" == "$trim"* ]]; then value="${value#"$trim"}"; fi
        COMPREPLY+=("$value")
    done < <(command "${COMP_WORDS[0]}" __complete "${arguments[@]}" 2>/dev/null)
}
complete -o filenames -F _spk_rayjob spk-rayjob
`

const zshCompletionScript = `#compdef spk-rayjob
# Literal candidates only; each query uses the current login, with no cache.
_spk_rayjob() {
    local value description
    local -a candidates descriptions directories
    candidates=()
    descriptions=()
    directories=()
    while IFS=$'\t' read -r value description; do
        [[ -n "$value" ]] || continue
        if [[ "$value" == */ ]]; then
            directories+=("$value")
            continue
        fi
        candidates+=("$value")
        descriptions+=("$description")
    done < <(command "${words[1]}" __complete "${(@)words[2,CURRENT]}" 2>/dev/null)
    if ((${#candidates[@]})); then
        compadd -d descriptions -- "${candidates[@]}"
    fi
    if ((${#directories[@]})); then
        compadd -S '' -- "${directories[@]}"
    fi
}
if (( $+functions[compdef] )); then
    compdef _spk_rayjob spk-rayjob
fi
`
