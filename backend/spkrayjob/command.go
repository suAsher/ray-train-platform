package spkrayjob

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
	"ray-train-platform-backend/domain"
)

// Version is set by the release build. It defaults to dev for local builds.
var Version = "dev"

// Run executes one spk-rayjob subcommand. Credentials are read only from the
// environment or an owner-only configuration file, never command arguments.
func Run(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	return RunWithInput(ctx, arguments, os.Stdin, stdout, stderr, getenv)
}

// RunWithInput executes a command while allowing tests and callers to provide
// credentials through a non-terminal reader. A token is never accepted as a
// command-line argument, which keeps it out of shell history and process lists.
func RunWithInput(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) error {
	if len(arguments) == 0 {
		return errors.New("command is required; run spk-rayjob --help")
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	if stdin == nil {
		stdin = os.Stdin
	}
	if topic, requested := requestedHelpTopic(arguments); requested {
		return runCommandHelp(topic, stdout)
	}
	switch arguments[0] {
	case "version":
		return runVersion(arguments[1:], stdout)
	case "upgrade":
		return runUpgrade(ctx, arguments[1:], stdout, stderr)
	case "help", "-h", "--help":
		return runHelp(stdout)
	case "init":
		return runInit(arguments[1:], stdout)
	case "login":
		return runLogin(ctx, arguments[1:], stdin, stdout, stderr)
	case "login-check":
		return runLoginCheck(ctx, arguments[1:], stdout, stderr, getenv)
	case "package":
		return runPackage(arguments[1:], stdout)
	case "submit":
		return runSubmit(ctx, arguments[1:], stdout, stderr, getenv)
	case "source-artifact":
		return runSourceArtifact(ctx, arguments[1:], stdout, stderr, getenv)
	case "jobs":
		return runJobs(ctx, arguments[1:], stdout, stderr, getenv)
	case "images":
		return runImages(ctx, arguments[1:], stdout, stderr, getenv)
	case "datasets":
		return runDatasets(ctx, arguments[1:], stdout, stderr, getenv)
	case "dataset":
		return runDataset(ctx, arguments[1:], stdout, stderr, getenv)
	case "status":
		return runStatus(ctx, arguments[1:], stdout, stderr, getenv)
	case "logs":
		return runLogs(ctx, arguments[1:], stdout, stderr, getenv)
	case "connect":
		return runConnect(ctx, arguments[1:], stdin, stdout, stderr, getenv)
	case "cancel":
		return runCancel(ctx, arguments[1:], stdout, stderr, getenv)
	default:
		return fmt.Errorf("unknown command %q; run spk-rayjob --help", arguments[0])
	}
}

const helpText = `spk-rayjob — 分布式训练任务命令行客户端

用法：
  spk-rayjob <命令> [参数]
  spk-rayjob <命令> --help

首次使用（推荐 PAT）：
  1. 在 Portal「账户与安全 → 个人访问令牌」为目标团队创建 PAT；PAT 决定任务归属团队。
  2. 不要把 PAT 写进命令、Git 或脚本，使用标准输入登录：
       read -rs SPK_PAT && echo
       printf '%s\n' "$SPK_PAT" | spk-rayjob login \
         --server https://raytrain.wellspiking.ai --token-stdin
       unset SPK_PAT
  3. 执行 spk-rayjob login-check。INVALID_AUTHENTICATION 表示 PAT 已过期、撤销或复制错误。
  过渡期本地账号可用 --username <账号> --password-stdin；统一认证用户应使用 PAT。

最短提交路径：
  cd <代码目录>
  spk-rayjob init
  # 编辑 .spk-rayjob.yaml 中的 image、entrypoint、资源与数据路径
  spk-rayjob images
  spk-rayjob submit --watch

.spk-rayjob.yaml 示例（Ray Train 两个 Worker）：
  name: my-training
  image: harbor.wellspiking.ai/<project>/<image>:<tag>
  engine: ray-train
  dataMode: mount
  executionMode: ray_train
  workers: 2
  gpusPerWorker: 1
  cpuPerWorker: 8
  memoryPerWorker: 32Gi
  input:
    space: public
    path: <数据目录>
  output:
    path: experiments/my-training
  entrypoint: python3 train.py

资源与调度：
  GPU 总数 = workers × gpusPerWorker；Ray Head 不占 GPU。
  队列与 GPU 卡型由当前 PAT 所属团队策略自动选择，用户无需填写 queue 或卡型。
  ray-train 托管模式至少需要 2 个 Worker，每个 Worker 至少 1 GPU。
  ray-ddp 单机多卡示例：1 Worker × 8 GPU；ray-train 多机示例：2 Worker × 8 GPU。
  在每台 8 卡的集群中，2 Worker × 8 GPU 可作为多机强制验收，每个 Worker 必须独占一台机器。
  普通任务使用 normal；opportunistic 仅在管理员开启抢占后可用，且必须可从 Checkpoint 恢复。
  GPU_QUOTA_EXCEEDED 表示团队已用量加本次申请超过配额；等待资源释放或联系管理员。

训练引擎与数据：
  --engine ray-ddp    兼容已有 Actor + torchrun；平台负责启动，不要在 entrypoint 再写 torchrun。
  --engine ray-train  Ray Train 托管 Worker、分布式上下文、故障恢复与 Checkpoint。
  --data-mode mount           直接读取授权数据挂载。
  --data-mode cache           使用已有 NVMe 运行时缓存。
  --data-mode ray-data-stage  Ray Data 分布式读取源数据，生成 Worker 本地 NVMe 视图。
  --data-mode ray-data        训练代码直接消费 Ray Data 的 Parquet/图片分片。
  --data-mode streaming       固定不可变数据集版本并按需流式读取。
  流式示例：--data-mode streaming --dataset <数据集>:<版本> --dataset-cache-policy bounded
  Parquet 负责列式索引、分区和批量扫描；NVMe 是可丢弃缓存，不是数据真相。
  --max-failures 2                 Worker 最大恢复次数，0-10。
  --checkpoint-every-epochs 1      每隔多少 Epoch 保存 Checkpoint。
  --checkpoint-keep-latest 3       保留最近的 Checkpoint 数。
  --checkpoint-keep-best 1         保留最佳 Checkpoint 数。
  Ray Train、Ray Data 与闲时抢占只在平台开启后可用；提交前会读取平台能力并明确拒绝。

临时 NVMe 预热（可选）：
  spk-rayjob submit --cache-mode runtime --cache-size 1Ti \
    --cache-preload input --input-space public --input-path <数据集目录>
  加上 --cache-preload input 后，平台会在每个 Worker 启动前把所选输入预热到双 NVMe；
  不加该参数时不会自动缓存 /mnt/storage/public，只加速 Ray 临时文件和训练代码主动写入缓存的内容。

平台注入的环境变量（训练代码只依赖这些，不要写死 TOS 地址、桶名或节点路径）：
  PLATFORM_DATASET_PATH      只读；本次任务选中的输入数据目录
  PLATFORM_OUTPUT_PATH       读写；本次任务独占的输出目录，权重和结果写这里
  PLATFORM_CHECKPOINT_PATH   只读，可为空；续训时选中的历史任务结果
  PLATFORM_CACHE_PATH        临时读写，可为空；本地 NVMe 缓存，任务结束即回收
  PLATFORM_JOB_ID            只读；本次任务 ID，可用于命名实验或日志

  在代码里用 os.environ 读取，不要依赖 entrypoint 里的 shell 展开：
      dataset = Path(os.environ["PLATFORM_DATASET_PATH"])
      output  = Path(os.environ["PLATFORM_OUTPUT_PATH"])
  写在 entrypoint 里的 $PLATFORM_* 可能在提交侧就被求值成空字符串，训练随后
  会向类似 /run_dir 的根路径写入并报 PermissionError。

任务观察与操作：
  spk-rayjob jobs --state RUNNING
  spk-rayjob status <JOB ID>
  spk-rayjob logs -f <JOB ID>                         实时跟随日志
  spk-rayjob logs --limit 0 <JOB ID> > job.log        日志完整导出
  spk-rayjob connect <JOB ID>                         连接运行中的第 1 个 Worker
  spk-rayjob connect <JOB ID> --worker 1              连接指定 Worker；不需要 --ssh
  spk-rayjob cancel <JOB ID>                          请求停止任务
  Ctrl-C 只停止 --watch 或日志显示，不会自动停止平台任务。

断点续训：
  spk-rayjob submit --resume-from-job <上一次 JOB ID> --watch
  训练代码必须把 Checkpoint 写入 PLATFORM_OUTPUT_PATH，并显式从 PLATFORM_CHECKPOINT_PATH 读取。

提交前自检：
  先用 1 卡最小批量跑通几个 step，再扩到多卡多机；
  所有 rank 使用 DistributedSampler，只有 rank 0 写 checkpoint；
  用 spk-rayjob images 确认镜像已登记且支持所选 engine；
  输入目录先确认真实存在 —— 路径写错的多卡任务通常两分钟内就失败。

常见错误：
  INVALID_AUTHENTICATION         重新创建/登录有效平台 PAT，不要使用 GitLab Token
  GPU_QUOTA_EXCEEDED             团队 GPU 配额不足或已有任务占满配额
  IMAGE_NOT_ALLOWED              镜像未登记、当前团队不可见或不支持所选引擎
  python: not found              镜像只有 python3，入口命令改用 python3
  KeyError: RANK                 代码错误地假定单卡任务也有 torchrun 环境
  PermissionError 且路径像 /run_dir  entrypoint 里的 $PLATFORM_* 展开成了空值
  FileNotFoundError 指向数据目录     选中的输入路径不存在，先在 Portal 里确认
  No module named mmdet3d.ops    上传的源码覆盖了镜像里编译好的扩展
  任务一直排队                    GPU 配额或空闲卡不足；检查是否有调试环境空占卡

常用命令：
  login, login-check, upgrade, init, submit, jobs, images, datasets,
  dataset versions, status, logs, connect, cancel, version

运行 spk-rayjob submit --help 查看全部提交参数和组合示例；其他命令也支持 --help。
`

const submitHelpText = `spk-rayjob submit — 打包当前代码目录并提交训练

用法：
  spk-rayjob submit [参数]

常用参数：
  --dir DIR                       代码目录，默认当前目录
  --name NAME                     任务名；默认读取 .spk-rayjob.yaml
  --image IMAGE                   已登记且当前团队可见的镜像 tag 或 digest
  --entrypoint COMMAND             用户训练命令，不要重复写 torchrun
  --engine ray-ddp|ray-train       训练引擎
  --execution-mode MODE            auto|single_gpu|torchrun|ray_train
  --workers N                      Worker 数量
  --gpus-per-worker N              每个 Worker 的 GPU 数
  --cpu-per-worker N               每个 Worker 的 CPU
  --memory-per-worker SIZE         每个 Worker 的内存，例如 32Gi
  --priority LEVEL                 production|normal|opportunistic
  --preemptible                    声明闲时任务可被抢占；仅管理员开启后有效
  --watch                          等到终态；Ctrl-C 只停止等待，不停止任务

数据与结果：
  --input-space SPACE              public、team、my-files 等逻辑空间
  --input-path PATH                输入空间内相对路径
  --output-path PATH               my-runs 下相对路径
  --checkpoint-space SPACE         直接指定只读 Checkpoint 空间
  --checkpoint-path PATH           Checkpoint 相对路径
  --resume-from-job JOB_ID         使用历史托管任务最新完整 Checkpoint
  --data-mode MODE                 mount|cache|ray-data-stage|ray-data|streaming
  --ray-data-format FORMAT         parquet|images
  --ray-data-path PATH             所选输入内的 Ray Data 相对路径
  --dataset DATASET[:VERSION]      不可变数据集与可选版本
  --dataset-version VERSION        不可变版本 ID 或 latest
  --dataset-sites A,B              只选择给定场地；留空使用完整版本
  --dataset-cache-policy POLICY    streaming 的 off|auto|bounded
  --cache-mode runtime             启用 Worker 本地 NVMe
  --cache-size SIZE                平台允许的缓存容量
  --cache-preload input            训练前分布式预热输入

Ray Train 恢复与保留：
  --max-failures N                 Worker 最大恢复次数，0-10
  --checkpoint-every-epochs N      每 N 个 Epoch 保存一次
  --checkpoint-keep-latest N       保留最近 Checkpoint 数
  --checkpoint-keep-best N         保留最佳 Checkpoint 数

连接与输出：
  --server URL                     覆盖已登录的平台地址
  --config FILE                    使用另一份 0600 登录配置
  --ca-file FILE                   私有 CA PEM
  --output text|json               输出格式
  --debug                          只向 stderr 写脱敏请求诊断

示例：
  # 读取项目文件中的全部默认值
  spk-rayjob submit --watch

  # 两个 Ray Train Worker；平台按团队自动选择队列和 GPU 卡型
  spk-rayjob submit --engine ray-train --workers 2 --gpus-per-worker 1 --watch

  # 多机强制验收：在每台 8 卡节点上，每个 Worker 独占一台
  spk-rayjob submit --engine ray-train --workers 2 --gpus-per-worker 8 --watch

  # 不可变数据集按场地流式训练
  spk-rayjob submit --engine ray-train --data-mode streaming \
    --dataset <数据集>:<版本> --dataset-sites <场地A>,<场地B> \
    --dataset-cache-policy bounded --watch

注意：命令行显式参数覆盖 .spk-rayjob.yaml；没有显式填写的值继续使用项目文件。
`

const loginHelpText = `spk-rayjob login — 安全保存平台会话或 PAT

用法：
  spk-rayjob login --server URL --token-stdin
  spk-rayjob login --server URL --username USER --password-stdin

推荐在 Portal「账户与安全」创建绑定目标团队的 PAT，再通过 --token-stdin 输入。
PAT 和密码不会作为命令参数保存；配置文件权限固定为 0600。
--config FILE 可指定另一份配置，--ca-file FILE 可指定私有 CA。
`

const logsHelpText = `spk-rayjob logs — 查看或导出训练日志

用法：
  spk-rayjob logs [--limit N] <JOB ID>
  spk-rayjob logs -f [--limit N] <JOB ID>

-f/--follow 实时跟随；--limit 0 表示文本日志完整导出，最大 250000 行。
日志完整导出示例：spk-rayjob logs --limit 0 <JOB ID> > job.log
JSON 输出必须使用 --output json --limit 1..10000，所有 flags 放在 JOB ID 前。
`

const connectHelpText = `spk-rayjob connect — 进入自己的运行中训练 Worker

用法：
  spk-rayjob connect JOB_ID
  spk-rayjob connect JOB_ID --worker N

默认连接第 1 个 Worker（序号 0）。任务必须正在运行且属于当前用户；不需要 --ssh。
输入 exit 只退出连接，不会停止训练。平台使用短期连接票据，不向用户暴露 Kubernetes 凭据。
`

var simpleCommandHelp = map[string]string{
	"upgrade":          "用法：spk-rayjob upgrade\n校验 SHA256 后升级当前客户端。\n",
	"init":             "用法：spk-rayjob init [--dir DIR] [--name NAME] [--image IMAGE] [--entrypoint COMMAND] [--engine ray-ddp|ray-train] [--workers N] [--gpus-per-worker N]\n在代码目录创建 .spk-rayjob.yaml，不会提交任务。\n",
	"login-check":      "用法：spk-rayjob login-check\n验证当前配置中的会话或 PAT。\n",
	"jobs":             "用法：spk-rayjob jobs [--state STATE] [--limit 1..500] [--output text|json]\n列出当前用户在当前团队可见的任务。\n",
	"images":           "用法：spk-rayjob images [--output text|json]\n列出当前团队可用的已登记训练镜像及支持的引擎。\n",
	"datasets":         "用法：spk-rayjob datasets [--output text|json]\n列出当前用户可用的数据集。\n",
	"dataset versions": "用法：spk-rayjob dataset versions [--output text|json] <数据集 ID 或 slug>\n列出可训练的不可变数据版本。\n",
	"status":           "用法：spk-rayjob status [--output text|json] <JOB ID>\n查看任务状态、规模、镜像与结果目录；flags 必须放在 JOB ID 前。\n",
	"cancel":           "用法：spk-rayjob cancel [--output text|json] <JOB ID>\n请求停止自己的任务；停止是异步操作。\n",
	"version":          "用法：spk-rayjob version\n显示客户端发布版本。\n",
	"package":          "用法：spk-rayjob package [--dir DIR] [--output FILE]\n仅为高级自动化生成源码包，不提交任务。\n",
}

func requestedHelpTopic(arguments []string) (string, bool) {
	if len(arguments) == 0 {
		return "", false
	}
	if arguments[0] == "help" {
		return strings.Join(arguments[1:], " "), true
	}
	last := arguments[len(arguments)-1]
	if last != "-h" && last != "--help" {
		return "", false
	}
	if len(arguments) == 1 {
		return "", true
	}
	return strings.Join(arguments[:len(arguments)-1], " "), true
}

func runCommandHelp(topic string, stdout io.Writer) error {
	topic = strings.TrimSpace(topic)
	var text string
	switch topic {
	case "", "help":
		text = helpText
	case "submit":
		text = submitHelpText
	case "login":
		text = loginHelpText
	case "logs":
		text = logsHelpText
	case "connect":
		text = connectHelpText
	default:
		text = simpleCommandHelp[topic]
	}
	if text == "" {
		return fmt.Errorf("unknown help topic %q; run spk-rayjob --help", topic)
	}
	_, err := io.WriteString(stdout, text)
	return err
}

func runHelp(stdout io.Writer) error {
	_, err := io.WriteString(stdout, helpText)
	return err
}

func runVersion(arguments []string, stdout io.Writer) error {
	if len(arguments) != 0 {
		return errors.New("version does not accept arguments")
	}
	_, err := fmt.Fprintln(stdout, "spk-rayjob "+Version)
	return err
}

type connectionFlags struct {
	server string
	caFile string
	config string
	debug  bool
}

func bindConnectionFlags(set *flag.FlagSet, flags *connectionFlags) {
	set.StringVar(&flags.server, "server", "", "platform API base URL")
	set.StringVar(&flags.caFile, "ca-file", "", "PEM file containing the private CA")
	set.StringVar(&flags.config, "config", "", "owner-only spk-rayjob config file")
	set.BoolVar(&flags.debug, "debug", false, "write redacted request diagnostics to stderr")
}

// firstEnv prefers the current variable name and falls back to the one used
// before the client was renamed, so existing automation keeps working.
func firstEnv(getenv func(string) string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func newCommandClient(flags connectionFlags, getenv func(string) string, stderr io.Writer) (*Client, error) {
	server := strings.TrimSpace(flags.server)
	if server == "" {
		server = strings.TrimSpace(firstEnv(getenv, "SPK_RAYJOB_URL", "RAY_PLATFORM_URL"))
	}
	envToken := strings.TrimSpace(firstEnv(getenv, "SPK_RAYJOB_TOKEN", "RAY_PLATFORM_TOKEN"))
	needsConfig := server == "" || envToken == ""
	var config configFile
	if needsConfig {
		loaded, err := loadConfig(flags.config)
		if err != nil {
			return nil, err
		}
		config = loaded
		if server == "" {
			server = config.Server
		}
	}
	token, err := LoadToken(envToken, flags.config)
	if err != nil {
		return nil, err
	}
	caFile := strings.TrimSpace(flags.caFile)
	if caFile == "" {
		caFile = strings.TrimSpace(getenv("SSL_CERT_FILE"))
	}
	var debugWriter io.Writer
	if flags.debug {
		debugWriter = stderr
	}
	return NewClient(ClientOptions{ServerURL: server, Token: token, CAFile: caFile, DebugWriter: debugWriter})
}

func runLogin(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("login", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	tokenStdin := set.Bool("token-stdin", false, "read a personal access token from standard input")
	username := set.String("username", "", "platform username")
	passwordStdin := set.Bool("password-stdin", false, "read the password from standard input")
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 {
		return errors.New("usage: spk-rayjob login --server https://<平台地址> [--username <账号>]")
	}
	if strings.TrimSpace(connection.server) == "" {
		return errors.New("login requires --server, for example: spk-rayjob login --server https://raytrain.wellspiking.ai")
	}
	if *tokenStdin && *passwordStdin {
		return errors.New("login accepts either --token-stdin or --password-stdin, not both")
	}

	var token, confirmedUsername string
	switch {
	case *tokenStdin:
		if strings.TrimSpace(*username) != "" {
			return errors.New("--username is only valid for password login")
		}
		loaded, err := promptSecret("个人访问令牌: ", stdin, stdout)
		if err != nil {
			return err
		}
		client, err := NewClient(ClientOptions{ServerURL: connection.server, Token: loaded, CAFile: connection.caFile})
		if err != nil {
			return err
		}
		if _, err := client.LoginCheck(ctx); err != nil {
			return err
		}
		token = loaded
	default:
		// Password is the default flow. Without --password-stdin the user is
		// prompted, which is what someone typing this command by hand expects;
		// requiring a shell pipeline made the command appear to hang.
		name := strings.TrimSpace(*username)
		if name == "" {
			if !isInteractive(stdin) {
				return errors.New("--username is required when standard input is not a terminal")
			}
			prompted, err := promptLine("平台用户名: ", stdin, stdout)
			if err != nil {
				return err
			}
			name = prompted
		}
		password, err := promptSecret("密码: ", stdin, stdout)
		if err != nil {
			return err
		}
		login, err := LoginWithLocalCredentials(ctx, connection.server, name, password, connection.caFile, nil)
		if err != nil {
			return err
		}
		token = login.Token
		confirmedUsername = login.Username
	}
	if err := writeConfig(connection.config, configFile{Server: strings.TrimSpace(connection.server), Token: token}); err != nil {
		return err
	}
	message := "登录成功"
	if confirmedUsername != "" {
		message += "：" + confirmedUsername
	}
	message += "\n下一步：进入代码目录执行 spk-rayjob submit --watch"
	_, err := fmt.Fprintln(stdout, message)
	if err == nil {
		if client, clientErr := NewClient(ClientOptions{ServerURL: connection.server, Token: token, CAFile: connection.caFile}); clientErr == nil {
			_ = client.checkRelease(ctx, stderr, false)
		}
	}
	return err
}

func runLoginCheck(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	set := flag.NewFlagSet("login-check", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 {
		return errors.New("invalid login-check arguments")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	_, err = client.LoginCheck(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "login ok")
	return err
}

func runPackage(arguments []string, stdout io.Writer) error {
	set := flag.NewFlagSet("package", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	directory := set.String("dir", ".", "source directory")
	output := set.String("output", "", "archive output path")
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 || strings.TrimSpace(*output) == "" {
		return errors.New("package requires --output")
	}
	if _, err := os.Lstat(*output); err == nil {
		return errors.New("package output already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect package output: %w", err)
	}
	archive, err := BuildArchive(*directory)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o700); err != nil {
		_ = os.Remove(archive.Path)
		return fmt.Errorf("create package output directory: %w", err)
	}
	if err := os.Rename(archive.Path, *output); err != nil {
		_ = os.Remove(archive.Path)
		return fmt.Errorf("write package: %w", err)
	}
	archive.Path = *output
	return writeJSON(stdout, archive)
}

func runInit(arguments []string, stdout io.Writer) error {
	set := flag.NewFlagSet("init", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	directory := set.String("dir", ".", "source directory")
	name := set.String("name", "", "default job name; defaults to the directory name")
	image := set.String("image", "", "catalogued training image with an explicit tag or sha256 digest")
	entrypoint := set.String("entrypoint", "python train.py", "training command, without torchrun")
	engine := set.String("engine", string(domain.TrainingEngineRayDDP), "training engine: ray-ddp or ray-train")
	dataMode := set.String("data-mode", "", "data mode: mount, cache, ray-data-stage, ray-data, streaming")
	accelerator := set.String("accelerator", string(domain.AcceleratorRTX4090), "GPU class: rtx4090, a100, a800, h20")
	priority := set.String("priority", string(domain.WorkloadPriorityNormal), "workload priority: production, normal, opportunistic")
	preemptible := set.Bool("preemptible", false, "allow checkpoint-safe opportunistic preemption")
	gpus := set.Int("gpus-per-worker", 1, "GPUs per worker")
	workers := set.Int("workers", 1, "worker replicas")
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 {
		return errors.New("invalid init arguments")
	}
	resolvedEngine, err := parseTrainingEngine(*engine)
	if err != nil {
		return err
	}
	resolvedDataMode, err := parseDataMode(*dataMode, projectCache{})
	if err != nil {
		return err
	}
	if (resolvedDataMode == domain.DataModeRayData || resolvedDataMode == domain.DataModeRayDataStage || resolvedDataMode == domain.DataModeStreaming) && resolvedEngine != domain.TrainingEngineRayTrain {
		return fmt.Errorf("%s 需要 --engine ray-train", resolvedDataMode)
	}
	jobName := sanitizeJobName(*name)
	if jobName == "" {
		jobName = projectRelativeName(*directory)
	}
	execution, err := executionProfileForFlags("auto", *workers, *gpus)
	if err != nil {
		return err
	}
	starter := project{
		Name: jobName, Image: strings.TrimSpace(*image), Entrypoint: strings.TrimSpace(*entrypoint),
		Engine: string(resolvedEngine), DataMode: string(resolvedDataMode),
		AcceleratorClass: domain.AcceleratorClass(*accelerator), Priority: *priority, Preemptible: *preemptible,
		Workers: *workers, GPUsPerWorker: *gpus, CPUPerWorker: 8, MemoryPerWorker: "32Gi",
		ExecutionMode: string(execution.Mode), Output: projectLocation{Path: jobName},
	}
	if err := writeProject(*directory, starter); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "已创建 %s。请填写 image 与 entrypoint，然后运行：spk-rayjob submit --watch\n", projectFileName)
	return err
}

func runSubmit(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	set := flag.NewFlagSet("submit", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	var format outputFormatFlag
	bindOutputFormatFlag(set, &format)
	directory := set.String("dir", ".", "source directory")
	sourceRequestID := set.String("source-request-id", "", "owner-scoped source request identity for recoverable automation")
	name := set.String("name", "", "job DNS name")
	image := set.String("image", "", "catalogued training image with an explicit tag or sha256 digest")
	entrypoint := set.String("entrypoint", "", "shell command to run")
	engine := set.String("engine", string(domain.TrainingEngineRayDDP), "training engine: ray-ddp or ray-train")
	dataMode := set.String("data-mode", "", "data mode: mount, cache, ray-data-stage, ray-data, streaming")
	accelerator := set.String("accelerator", string(domain.AcceleratorRTX4090), "GPU class: rtx4090, a100, a800, h20")
	priority := set.String("priority", string(domain.WorkloadPriorityNormal), "workload priority: production, normal, opportunistic")
	preemptible := set.Bool("preemptible", false, "allow checkpoint-safe opportunistic preemption")
	dataset := set.String("dataset", "", "public dataset ID/slug, optionally DATASET:VERSION")
	datasetVersion := set.String("dataset-version", "", "immutable dataset version ID or latest")
	cachePolicy := set.String("dataset-cache-policy", "", "streaming dataset cache policy: off, auto, bounded")
	datasetSites := set.String("dataset-sites", "", "comma-separated site IDs; empty selects the full version")
	maxFailures := set.Int("max-failures", 2, "ray-train worker recovery limit (0-10)")
	checkpointEveryEpochs := set.Int("checkpoint-every-epochs", 1, "ray-train checkpoint interval in epochs")
	checkpointKeepLatest := set.Int("checkpoint-keep-latest", 3, "ray-train latest checkpoint retention")
	checkpointKeepBest := set.Int("checkpoint-keep-best", 1, "ray-train best checkpoint retention")
	workers := set.Int("workers", 1, "worker replicas")
	gpus := set.Int("gpus-per-worker", 1, "GPUs per worker")
	cpu := set.Int64("cpu-per-worker", 8, "CPUs per worker")
	memory := set.String("memory-per-worker", "32Gi", "memory per worker")
	executionMode := set.String("execution-mode", "auto", "execution mode: auto, single_gpu, torchrun, ray_train")
	cacheMode := set.String("cache-mode", "", "cache mode: off, runtime")
	cacheSize := set.String("cache-size", "", "runtime cache size allowed by the platform")
	cachePreload := set.String("cache-preload", "", "automatic cache preload: input")
	rayDataFormat := set.String("ray-data-format", "", "Ray Data streaming format: parquet or images")
	rayDataPath := set.String("ray-data-path", "", "path relative to the selected input")
	inputSpace := set.String("input-space", "", "logical input data space")
	inputPath := set.String("input-path", "", "path relative to the input data space")
	checkpointSpace := set.String("checkpoint-space", "", "logical checkpoint data space")
	checkpointPath := set.String("checkpoint-path", "", "path relative to the checkpoint data space")
	outputPath := set.String("output-path", "", "path relative to My runs")
	resumeFromJob := set.String("resume-from-job", "", "select the latest complete checkpoint from a previous managed job")
	watch := set.Bool("watch", false, "wait until the job reaches a terminal state")
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 {
		return errors.New("invalid submit arguments; run spk-rayjob help")
	}
	if *sourceRequestID != "" && !stableSourceRequestID.MatchString(*sourceRequestID) {
		return errors.New("--source-request-id must match source-request- followed by 24 lowercase hexadecimal characters")
	}
	// Committed defaults make "edit code, submit" a single command. A flag the
	// user actually typed still wins over the file.
	defaults, err := loadProject(*directory)
	if err != nil {
		return err
	}
	provided := providedFlags(set)
	datasetOverride, err := parseDatasetFlag(*dataset, *datasetVersion, provided["dataset"], provided["dataset-version"])
	if err != nil {
		return err
	}
	var siteIDs []string
	if strings.TrimSpace(*datasetSites) != "" {
		for _, site := range strings.Split(*datasetSites, ",") {
			siteIDs = append(siteIDs, strings.TrimSpace(site))
		}
	}
	datasetOverride.Reference.Sites, err = domain.NewDatasetSites(siteIDs)
	if err != nil {
		return err
	}
	resolved := defaults.merge(submitOverrides{
		Name: *name, Image: *image, Entrypoint: *entrypoint, Engine: *engine, DataMode: *dataMode, Workers: *workers, GPUsPerWorker: *gpus,
		AcceleratorClass: domain.AcceleratorClass(*accelerator), Priority: *priority, Preemptible: *preemptible,
		DatasetRef: datasetOverride.Reference, CachePolicy: domain.DatasetCachePolicy(strings.TrimSpace(*cachePolicy)),
		CPUPerWorker: *cpu, MemoryPerWorker: *memory, ExecutionMode: *executionMode,
		Cache:                  projectCache{Mode: *cacheMode, Size: *cacheSize, Preload: *cachePreload},
		RayData:                projectRayData{Format: *rayDataFormat, Path: *rayDataPath},
		Input:                  projectLocation{Space: *inputSpace, Path: *inputPath},
		Checkpoint:             projectLocation{Space: *checkpointSpace, Path: *checkpointPath},
		Output:                 projectLocation{Path: *outputPath},
		providedName:           provided["name"],
		providedImage:          provided["image"],
		providedEntrypoint:     provided["entrypoint"],
		providedEngine:         provided["engine"],
		providedDataMode:       provided["data-mode"],
		providedAccelerator:    provided["accelerator"],
		providedPriority:       provided["priority"],
		providedPreemptible:    provided["preemptible"],
		providedDataset:        datasetOverride.DatasetProvided,
		providedDatasetVersion: datasetOverride.VersionProvided,
		providedCachePolicy:    provided["dataset-cache-policy"],
		providedDatasetSites:   provided["dataset-sites"],
		providedWorkers:        provided["workers"],
		providedGPUs:           provided["gpus-per-worker"],
		providedCPU:            provided["cpu-per-worker"],
		providedMemory:         provided["memory-per-worker"],
		providedMode:           provided["execution-mode"],
		providedCacheMode:      provided["cache-mode"],
		providedCacheSize:      provided["cache-size"],
		providedCachePreload:   provided["cache-preload"],
		providedRayData:        provided["ray-data-format"] || provided["ray-data-path"],
		providedInput:          provided["input-space"] || provided["input-path"],
		providedCheckpoint:     provided["checkpoint-space"] || provided["checkpoint-path"],
		providedOutput:         provided["output-path"],
	})
	if resolved.DataMode == string(domain.DataModeStreaming) {
		if provided["cache-mode"] || provided["cache-size"] || provided["cache-preload"] {
			return errors.New("streaming 使用 --dataset-cache-policy off|auto|bounded，不能同时使用 --cache-mode/--cache-size/--cache-preload")
		}
		// Streaming uses the versioned dataset cache policy. Clear legacy cache
		// defaults inherited from an older project file so they cannot leak into
		// the public JobSpec or trigger a whole-dataset preload.
		resolved.Cache = projectCache{}
	}
	if resolved.DataMode == string(domain.DataModeRayDataStage) {
		if strings.TrimSpace(resolved.Cache.Mode) == "" {
			resolved.Cache.Mode = string(domain.CacheModeRuntime)
		}
		// The distributed Ray Data stage replaces the legacy init-container
		// preloader.  Clear stale project defaults when a user switches modes.
		resolved.Cache.Preload = ""
	}
	cacheDraft, err := newProjectCacheDraft(resolved.Cache)
	if err != nil {
		return err
	}
	previousJobID := strings.TrimSpace(*resumeFromJob)
	checkpointProvided := provided["checkpoint-space"] || provided["checkpoint-path"]
	if previousJobID != "" && checkpointProvided {
		return errors.New("--resume-from-job cannot be combined with --checkpoint-space or --checkpoint-path")
	}
	if previousJobID != "" && !platformJobID.MatchString(previousJobID) {
		return errors.New("--resume-from-job 必须是有效的平台 job ID")
	}
	resolvedEngine, err := parseTrainingEngine(resolved.Engine)
	if err != nil {
		return err
	}
	if previousJobID != "" && resolvedEngine != domain.TrainingEngineRayTrain {
		return errors.New("--resume-from-job 仅支持 --engine ray-train")
	}
	managedFlagsProvided := provided["max-failures"] || provided["checkpoint-every-epochs"] || provided["checkpoint-keep-latest"] || provided["checkpoint-keep-best"]
	if resolvedEngine != domain.TrainingEngineRayTrain && managedFlagsProvided {
		return errors.New("--max-failures 与 --checkpoint-* 参数仅支持 --engine ray-train，不能用于 ray-ddp")
	}
	managedPolicy := domain.ManagedTrainingPolicy{
		MaxFailures: *maxFailures,
		Checkpoint: domain.CheckpointPolicy{
			EveryEpochs: *checkpointEveryEpochs,
			KeepLatest:  *checkpointKeepLatest,
			KeepBest:    *checkpointKeepBest,
		},
	}
	if resolvedEngine == domain.TrainingEngineRayTrain {
		if err := managedPolicy.Validate(); err != nil {
			return fmt.Errorf("无效的 Ray Train 托管策略：%w", err)
		}
	}
	if err := validateLocalSubmit(resolved, previousJobID, checkpointProvided); err != nil {
		return err
	}
	draft, err := newLocalSubmitDraft(resolved, *directory, stdout)
	if err != nil {
		return err
	}
	if draft.spec.TrainingEngine == domain.TrainingEngineRayTrain {
		managedPolicy.RayData = draft.spec.Managed.RayData
		draft.spec.Managed = managedPolicy
	}
	var archive Archive
	if draft.spec.TrainingEngine != domain.TrainingEngineRayTrain {
		archive, err = BuildArchive(*directory)
		if err != nil {
			return err
		}
		defer os.Remove(archive.Path)
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	if err := client.checkRelease(ctx, stderr, true); err != nil {
		return err
	}
	runtimeCapabilities := PlatformRuntimeLimits{}
	var limitsSnapshot *PlatformLimits
	if draft.spec.TrainingEngine == domain.TrainingEngineRayTrain || cacheDraft.mode == domain.CacheModeRuntime {
		limits, limitsErr := client.PlatformLimits(ctx)
		if limitsErr != nil {
			return fmt.Errorf("读取平台提交能力失败：%w", limitsErr)
		}
		limitsSnapshot = &limits
		runtimeCapabilities = limits.Runtime
	}
	if draft.spec.TrainingEngine == domain.TrainingEngineRayTrain {
		if !runtimeCapabilities.ManagedAvailable() {
			return errors.New("当前平台未开启 Ray Train 托管引擎，请改用 --engine ray-ddp")
		}
	}
	resolvedCache, err := resolveProjectCache(cacheDraft, limitsSnapshot)
	if err != nil {
		return err
	}
	if draft.spec.TrainingEngine == domain.TrainingEngineRayTrain {
		if err := applyManagedImage(ctx, &draft.values, client, runtimeCapabilities, stdout); err != nil {
			return err
		}
	} else if err := applyPlatformDerivedDefaults(ctx, &draft.values, client, stdout); err != nil {
		return err
	}
	if err := draft.values.validateForSubmit(); err != nil {
		return err
	}
	// Resume is bound to one complete checkpoint returned by the owner-scoped
	// API. Neither an object path nor a checkpoint ID is accepted from flags.
	if previousJobID != "" {
		previous, statusErr := client.Status(ctx, previousJobID)
		if statusErr != nil {
			return fmt.Errorf("read the previous job: %w", statusErr)
		}
		if previous.ID != previousJobID {
			return errors.New("父任务响应与请求的 job ID 不一致")
		}
		checkpoints, checkpointErr := client.Checkpoints(ctx, previousJobID)
		if checkpointErr != nil {
			return fmt.Errorf("read the previous job checkpoints: %w", checkpointErr)
		}
		selection, resolveErr := checkpointLocationForPreviousRun(previous.Raw, checkpoints)
		if resolveErr != nil {
			return resolveErr
		}
		if err := draft.setCheckpoint(selection.Location); err != nil {
			return err
		}
		draft.spec.ParentJobID = previousJobID
	}
	spec := draft.finalSpec(resolvedCache)
	if err := validateArchiveJobSpec(spec); err != nil {
		return err
	}
	if spec.DataMode == domain.DataModeStreaming {
		resolvedSpec, preflight, preflightErr := client.PreflightStreaming(ctx, spec)
		if preflightErr != nil {
			return fmt.Errorf("提交前检查失败：%w", preflightErr)
		}
		spec = resolvedSpec
		if !format.json {
			if err := renderStreamingPreflight(stdout, preflight); err != nil {
				return err
			}
		}
	}
	if archive.Path == "" {
		archive, err = BuildArchive(*directory)
		if err != nil {
			return err
		}
		defer os.Remove(archive.Path)
	}
	job, err := client.submitArchiveWithRequestID(ctx, archive, spec, *sourceRequestID)
	if err != nil {
		return err
	}
	if format.json {
		if err := writeJSON(stdout, job.Raw); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(stdout, "已提交 %s（%s）。查看日志：spk-rayjob logs -f %s\n下一步示例（不会复现本次临时参数）：%s\n", job.ID, draft.values.Name, job.ID, renderSubmitCommand(spec.TrainingEngine, runtimeCapabilities)); err != nil {
		return err
	}
	if !*watch {
		return nil
	}
	return watchJob(ctx, client, job.ID, stdout, format.json)
}

func runDatasets(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	set := flag.NewFlagSet("datasets", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	var format outputFormatFlag
	bindOutputFormatFlag(set, &format)
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 {
		return errors.New("datasets does not accept positional arguments")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	items, err := client.Datasets(ctx)
	if err != nil {
		return err
	}
	if format.json {
		return writeJSON(stdout, items)
	}
	return renderDatasets(stdout, items)
}

func runDataset(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	if len(arguments) == 0 || arguments[0] != "versions" {
		return errors.New("dataset requires: dataset versions <dataset ID or slug>")
	}
	set := flag.NewFlagSet("dataset versions", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	var format outputFormatFlag
	bindOutputFormatFlag(set, &format)
	if err := set.Parse(arguments[1:]); err != nil || set.NArg() != 1 || strings.TrimSpace(set.Arg(0)) == "" {
		return errors.New("dataset versions requires one dataset ID or slug")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	dataset, versions, err := client.DatasetVersions(ctx, set.Arg(0))
	if err != nil {
		return err
	}
	if format.json {
		return writeJSON(stdout, struct {
			Dataset  DatasetCatalogItem          `json:"dataset"`
			Versions []DatasetVersionCatalogItem `json:"versions"`
		}{Dataset: dataset, Versions: versions})
	}
	return renderDatasetVersions(stdout, dataset, versions)
}

func runSourceArtifact(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	if len(arguments) == 0 || arguments[0] != "resolve" {
		return errors.New("source-artifact requires the resolve subcommand")
	}
	set := flag.NewFlagSet("source-artifact resolve", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	var format outputFormatFlag
	bindOutputFormatFlag(set, &format)
	requestID := set.String("request-id", "", "owner-scoped source request identity")
	if err := set.Parse(arguments[1:]); err != nil || set.NArg() != 0 {
		return errors.New("invalid source-artifact resolve arguments")
	}
	if !stableSourceRequestID.MatchString(*requestID) {
		return errors.New("--request-id must match source-request- followed by 24 lowercase hexadecimal characters")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	artifact, err := client.ResolveArtifactRequest(ctx, *requestID)
	if err != nil {
		return err
	}
	if format.json {
		return writeJSON(stdout, artifact)
	}
	_, err = fmt.Fprintln(stdout, artifact.ArtifactID)
	return err
}

func validateLocalSubmit(value project, previousJobID string, checkpointProvided bool) error {
	if strings.TrimSpace(value.Entrypoint) == "" {
		return value.validateForSubmit()
	}
	if previousJobID != "" && checkpointProvided {
		return errors.New("--resume-from-job cannot be combined with --checkpoint-space or --checkpoint-path")
	}
	return nil
}

type localSubmitDraft struct {
	values project
	spec   domain.JobSpec
}

func newLocalSubmitDraft(value project, directory string, stdout io.Writer) (localSubmitDraft, error) {
	if err := applyLocalDerivedDefaults(&value, directory, stdout); err != nil {
		return localSubmitDraft{}, err
	}
	spec, err := value.jobSpec()
	if err != nil {
		return localSubmitDraft{}, err
	}
	if err := validatePreflightJobSpec(spec); err != nil {
		return localSubmitDraft{}, err
	}
	return localSubmitDraft{values: value, spec: spec}, nil
}

func (draft *localSubmitDraft) setCheckpoint(location projectLocation) error {
	checkpoint, err := commandDataLocation(location.Space, location.Path, "checkpoint")
	if err != nil {
		return err
	}
	draft.values.Checkpoint = location
	draft.spec.Checkpoint = checkpoint
	return nil
}

func (draft localSubmitDraft) finalSpec(cache domain.CacheRequest) domain.JobSpec {
	spec := draft.spec
	spec.Image = strings.TrimSpace(draft.values.Image)
	spec.Cache = cache
	return spec
}

// validateForSubmit fails before any network call so a missing value is
// reported as one clear message rather than a rejected API request.
// Local defaults are normalized into the draft before client configuration;
// the platform-derived image is applied only after cache policy resolution.
func applyLocalDerivedDefaults(value *project, directory string, stdout io.Writer) error {
	if strings.TrimSpace(value.Name) == "" {
		name, err := defaultJobName(directory, time.Now)
		if err != nil {
			return err
		}
		value.Name = name
		fmt.Fprintf(stdout, "任务名称：%s（来自目录名，可用 --name 覆盖）\n", name)
	}
	// The output directory defaults to the job name so results are easy to find.
	if strings.TrimSpace(value.Output.Path) == "" {
		value.Output.Path = value.Name
	}
	return nil
}

func applyPlatformDerivedDefaults(ctx context.Context, value *project, client *Client, stdout io.Writer) error {
	if strings.TrimSpace(value.Image) == "" {
		images, err := client.TrainingImages(ctx)
		if err != nil {
			return fmt.Errorf("读取镜像目录失败，请用 --image 指定：%w", err)
		}
		reference, err := defaultImage(images)
		if err != nil {
			return err
		}
		value.Image = reference
		fmt.Fprintf(stdout, "训练镜像：%s（平台默认，可用 --image 覆盖）\n", reference)
	}
	return nil
}

func applyManagedImage(ctx context.Context, value *project, client *Client, runtime PlatformRuntimeLimits, stdout io.Writer) error {
	images, err := client.TrainingImages(ctx)
	if err != nil {
		return fmt.Errorf("读取镜像目录失败：%w", err)
	}
	requested := strings.TrimSpace(value.Image)
	selected, err := managedImageForDataMode(images, requested, runtime, domain.DataMode(strings.TrimSpace(value.DataMode)))
	if err != nil {
		return err
	}
	value.Image = selected.Reference
	if requested == "" {
		fmt.Fprintf(stdout, "训练镜像：%s（平台默认的 ray-train 兼容镜像，可用 --image 覆盖）\n", selected.Reference)
	}
	return nil
}

func (value project) validateForSubmit() error {
	missing := make([]string, 0, 3)
	if strings.TrimSpace(value.Name) == "" {
		missing = append(missing, "--name")
	}
	if strings.TrimSpace(value.Image) == "" {
		missing = append(missing, "--image")
	}
	if strings.TrimSpace(value.Entrypoint) == "" {
		missing = append(missing, "--entrypoint")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("缺少 %s。\n启动命令无法自动推断，请直接传参，例如：\n  spk-rayjob submit --entrypoint 'python tools/train.py configs/x.yaml'\n经常重复同一条命令时，可运行 spk-rayjob init 把它存进 %s，之后 submit 就不用再带参数。",
		strings.Join(missing, "、"), projectFileName)
}

func (value project) jobSpec() (domain.JobSpec, error) {
	engine, err := parseTrainingEngine(value.Engine)
	if err != nil {
		return domain.JobSpec{}, err
	}
	dataMode, err := parseDataMode(value.DataMode, value.Cache)
	if err != nil {
		return domain.JobSpec{}, err
	}
	if (dataMode == domain.DataModeRayData || dataMode == domain.DataModeRayDataStage || dataMode == domain.DataModeStreaming) && engine != domain.TrainingEngineRayTrain {
		return domain.JobSpec{}, fmt.Errorf("%s 需要 --engine ray-train", dataMode)
	}
	var input domain.DataLocation
	if dataMode != domain.DataModeStreaming {
		input, err = commandDataLocation(value.Input.Space, value.Input.Path, "input")
		if err != nil {
			return domain.JobSpec{}, err
		}
	}
	if dataMode == domain.DataModeRayDataStage && (input.Space == "" || strings.TrimSpace(input.RelativePath) == "") {
		return domain.JobSpec{}, errors.New("ray-data-stage requires a governed input data space with a non-empty input path")
	}
	checkpoint, err := commandDataLocation(value.Checkpoint.Space, value.Checkpoint.Path, "checkpoint")
	if err != nil {
		return domain.JobSpec{}, err
	}
	outputPath := strings.TrimSpace(value.Output.Path)
	if outputPath == "" {
		outputPath = value.Name
	}
	output, err := domain.NewDataLocation(domain.DataSpaceMyRuns, outputPath)
	if err != nil {
		return domain.JobSpec{}, fmt.Errorf("output path: %w", err)
	}
	workers, gpus := oneIfZero(value.Workers), oneIfZero(value.GPUsPerWorker)
	execution, err := executionProfileForFlags(value.ExecutionMode, workers, gpus)
	if err != nil {
		return domain.JobSpec{}, err
	}
	cpu := value.CPUPerWorker
	if cpu == 0 {
		cpu = 8
	}
	memory := strings.TrimSpace(value.MemoryPerWorker)
	if memory == "" {
		memory = "32Gi"
	}
	datasetRef := domain.DatasetReference{
		Dataset: strings.TrimSpace(value.DatasetRef.Dataset),
		Version: strings.TrimSpace(value.DatasetRef.Version),
	}
	cachePolicy := domain.DatasetCachePolicy(strings.TrimSpace(string(value.CachePolicy)))
	if dataMode == domain.DataModeStreaming && cachePolicy == "" {
		cachePolicy = domain.DatasetCachePolicyAuto
	}
	if err := datasetRef.Validate(); err != nil {
		return domain.JobSpec{}, err
	}
	if err := cachePolicy.Validate(); err != nil {
		return domain.JobSpec{}, err
	}
	if dataMode == domain.DataModeStreaming && datasetRef.IsZero() {
		return domain.JobSpec{}, errors.New("streaming requires datasetRef")
	}
	cacheRequest := domain.CacheRequest{
		Mode: domain.CacheMode(strings.TrimSpace(value.Cache.Mode)), Size: strings.TrimSpace(value.Cache.Size),
		Preload: domain.CachePreloadMode(strings.TrimSpace(value.Cache.Preload)),
	}
	if dataMode == domain.DataModeStreaming {
		cacheRequest = domain.CacheRequest{}
	}
	spec := domain.JobSpec{
		Name: strings.TrimSpace(value.Name), Image: strings.TrimSpace(value.Image),
		AcceleratorClass: value.AcceleratorClass,
		Priority:         strings.TrimSpace(value.Priority),
		Preemptible:      value.Preemptible,
		TrainingEngine:   engine,
		DataMode:         dataMode,
		DatasetRef:       datasetRef,
		CachePolicy:      cachePolicy,
		Entrypoint:       domain.Entrypoint{Command: []string{"/bin/sh", "-lc", strings.TrimSpace(value.Entrypoint)}},
		Execution:        execution,
		Resources:        domain.Resources{WorkerReplicas: workers, GPUsPerWorker: gpus, CPUPerWorker: cpu, MemoryPerWorker: memory},
		Input:            input,
		Checkpoint:       checkpoint,
		Output:           output,
		Cache:            cacheRequest,
	}
	if engine == domain.TrainingEngineRayTrain {
		spec.Managed = defaultManagedTrainingPolicy()
		if dataMode == domain.DataModeRayDataStage {
			rayData, rayDataErr := domain.NewRayDataDatasetConfig(domain.RayDataFormatFiles, ".")
			if rayDataErr != nil {
				return domain.JobSpec{}, rayDataErr
			}
			spec.Managed.RayData = rayData
		}
		if dataMode == domain.DataModeRayData {
			rayData, rayDataErr := domain.NewRayDataDatasetConfig(
				domain.RayDataFormat(strings.TrimSpace(value.RayData.Format)),
				strings.TrimSpace(value.RayData.Path),
			)
			if rayDataErr != nil {
				return domain.JobSpec{}, rayDataErr
			}
			spec.Managed.RayData = rayData
		}
	}
	return spec, nil
}

func defaultManagedTrainingPolicy() domain.ManagedTrainingPolicy {
	return domain.ManagedTrainingPolicy{
		MaxFailures: 2,
		Checkpoint:  domain.CheckpointPolicy{EveryEpochs: 1, KeepLatest: 3, KeepBest: 1},
	}
}

func oneIfZero(value int) int {
	if value == 0 {
		return 1
	}
	return value
}

// providedFlags records which flags the caller actually typed. A committed
// default must survive a flag left at its zero value.
func providedFlags(set *flag.FlagSet) map[string]bool {
	provided := make(map[string]bool)
	set.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	return provided
}

type outputFormatFlag struct {
	json bool
}

func bindOutputFormatFlag(set *flag.FlagSet, flags *outputFormatFlag) {
	set.Func("output", "output format: text (default) or json", func(value string) error {
		switch strings.TrimSpace(strings.ToLower(value)) {
		case "json":
			flags.json = true
		case "text", "":
			flags.json = false
		default:
			return fmt.Errorf("output must be text or json")
		}
		return nil
	})
}

func runJobs(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	set := flag.NewFlagSet("jobs", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	var format outputFormatFlag
	bindOutputFormatFlag(set, &format)
	state := set.String("state", "", "filter by state, for example RUNNING or FAILED")
	limit := set.Int("limit", 50, "maximum jobs to list")
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 || *limit < 1 || *limit > 500 {
		return errors.New("jobs accepts --state and a --limit between 1 and 500")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	page, err := client.ListJobs(ctx, *state, *limit)
	if err != nil {
		return err
	}
	if format.json {
		return writeJSON(stdout, page)
	}
	return renderJobTable(stdout, page)
}

// watchJob polls until the run finishes. Interrupting the watch only stops the
// display: the job keeps running on the platform, which is why the message
// says so explicitly.
func watchJob(ctx context.Context, client *Client, jobID string, stdout io.Writer, asJSON bool) error {
	const pollInterval = 5 * time.Second
	lastState := ""
	for {
		job, err := client.Status(ctx, jobID)
		if err != nil {
			return err
		}
		state := string(job.ObservedState)
		if state != lastState && !asJSON {
			if _, err := fmt.Fprintf(stdout, "[%s] %s\n", time.Now().Format("15:04:05"), orDash(state)); err != nil {
				return err
			}
			lastState = state
		}
		if isTerminalJobState(state) {
			if asJSON {
				return writeJSON(stdout, job.Raw)
			}
			if err := renderJobDetail(stdout, job.Raw); err != nil {
				return err
			}
			if state == "SUCCEEDED" {
				return nil
			}
			return fmt.Errorf("任务以 %s 结束", state)
		}
		select {
		case <-ctx.Done():
			_, _ = fmt.Fprintf(stdout, "已停止显示；任务 %s 仍在平台上运行。\n", jobID)
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func executionProfileForFlags(value string, workers, gpus int) (domain.ExecutionProfile, error) {
	mode := strings.TrimSpace(value)
	if mode == "" || mode == "auto" {
		switch {
		case workers == 1 && gpus == 1:
			mode = string(domain.ExecutionModeSingleGPU)
		case workers == 1:
			mode = string(domain.ExecutionModeTorchrun)
		default:
			mode = string(domain.ExecutionModeRayTrain)
		}
	}
	profile := domain.ExecutionProfile{Mode: domain.ExecutionMode(mode)}
	if err := profile.Validate(domain.Resources{WorkerReplicas: workers, GPUsPerWorker: gpus}); err != nil {
		return domain.ExecutionProfile{}, fmt.Errorf("execution mode: %w", err)
	}
	return profile, nil
}

func commandDataLocation(space, relativePath, label string) (domain.DataLocation, error) {
	space = strings.TrimSpace(space)
	if space == "" && strings.TrimSpace(relativePath) == "" {
		return domain.DataLocation{}, nil
	}
	if space == "" {
		return domain.DataLocation{}, fmt.Errorf("%s path requires --%s-space", label, label)
	}
	location, err := domain.NewDataLocation(domain.DataSpaceID(space), relativePath)
	if err != nil {
		return domain.DataLocation{}, fmt.Errorf("%s data space: %w", label, err)
	}
	return location, nil
}

func runStatus(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	client, jobID, format, err := parseJobCommand("status", arguments, getenv, stderr)
	if err != nil {
		return err
	}
	job, err := client.Status(ctx, jobID)
	if err != nil {
		return err
	}
	if format.json {
		return writeJSON(stdout, job.Raw)
	}
	return renderJobDetail(stdout, job.Raw)
}

func runLogs(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	set := flag.NewFlagSet("logs", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	var format outputFormatFlag
	bindOutputFormatFlag(set, &format)
	limit := set.Int("limit", 0, "maximum total log lines; 0 prints complete history")
	follow := set.Bool("follow", false, "keep printing new lines until the job finishes")
	shortFollow := set.Bool("f", false, "alias for --follow")
	if err := set.Parse(arguments); err != nil || set.NArg() != 1 || *limit < 0 || *limit > maxCLIHistoryLines {
		return fmt.Errorf("logs requires a job ID and a limit between 0 and %d", maxCLIHistoryLines)
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	jobID := set.Arg(0)
	if !*follow && !*shortFollow {
		if !format.json {
			return writeJobLogHistory(ctx, client, jobID, *limit, stdout)
		}
		if *limit == 0 || *limit > maxCLIJSONLogLines {
			return fmt.Errorf("JSON log output requires --limit between 1 and %d", maxCLIJSONLogLines)
		}
		logs, logErr := collectJobLogs(ctx, client, jobID, *limit)
		if logErr != nil {
			return logErr
		}
		if format.json {
			return writeJSON(stdout, logs)
		}
		return nil
	}
	return followLogs(ctx, client, jobID, *limit, stdout)
}

const (
	cliLogPageSize       = 2000
	maxCLIHistoryLines   = 250000
	maxCLIJSONLogLines   = 10000
	followDrainPageLimit = 5
)

func writeJobLogHistory(ctx context.Context, client *Client, jobID string, maximum int, stdout io.Writer) error {
	cursor := ""
	written := 0
	for {
		pageSize := cliLogPageSize
		effectiveMaximum := maximum
		if effectiveMaximum == 0 {
			effectiveMaximum = maxCLIHistoryLines
		}
		if effectiveMaximum-written < pageSize {
			pageSize = effectiveMaximum - written
		}
		if pageSize <= 0 {
			return fmt.Errorf("log history exceeds the %d-line safety ceiling", maxCLIHistoryLines)
		}
		page, err := client.LogsPage(ctx, jobID, LogPageOptions{Limit: pageSize, Direction: "forward", Cursor: cursor})
		if err != nil {
			return err
		}
		if !page.PaginationAvailable && (maximum == 0 || maximum > pageSize) {
			return fmt.Errorf("platform backend does not support complete log pagination yet; retry after the platform upgrade finishes")
		}
		if _, err := renderLogEntries(stdout, page.Items, ""); err != nil {
			return err
		}
		written += len(page.Items)
		if !page.Page.HasMore || len(page.Items) == 0 {
			return nil
		}
		if maximum > 0 && written >= maximum {
			return nil
		}
		if maximum == 0 && written >= maxCLIHistoryLines {
			return fmt.Errorf("log history exceeds the %d-line safety ceiling", maxCLIHistoryLines)
		}
		next := strings.TrimSpace(page.Page.NextCursor)
		if next == "" || next == cursor {
			return fmt.Errorf("platform log cursor did not advance")
		}
		cursor = next
	}
}

func collectJobLogs(ctx context.Context, client *Client, jobID string, maximum int) (LogPage, error) {
	result := LogPage{
		JobID: jobID,
		Items: make([]LogEntry, 0),
		Page:  LogPageMeta{Direction: "forward", Limit: maximum},
	}
	cursor := ""
	for {
		pageSize := cliLogPageSize
		if maximum > 0 && maximum-len(result.Items) < pageSize {
			pageSize = maximum - len(result.Items)
		}
		if pageSize <= 0 {
			break
		}
		page, err := client.LogsPage(ctx, jobID, LogPageOptions{Limit: pageSize, Direction: "forward", Cursor: cursor})
		if err != nil {
			return LogPage{}, err
		}
		if !page.PaginationAvailable && maximum > pageSize {
			return LogPage{}, fmt.Errorf("platform backend does not support the requested paginated log limit yet; retry after the platform upgrade finishes")
		}
		result.Items = append(result.Items, page.Items...)
		result.Page.HasMore = page.Page.HasMore
		result.Page.NextCursor = page.Page.NextCursor
		if !page.Page.HasMore || len(page.Items) == 0 {
			break
		}
		next := strings.TrimSpace(page.Page.NextCursor)
		if next == "" || next == cursor {
			return LogPage{}, fmt.Errorf("platform log cursor did not advance")
		}
		cursor = next
	}
	return result, nil
}

// followLogs polls the same bounded log endpoint and prints only lines newer
// than the last one shown. It stops once the job reaches a terminal state so a
// finished run does not leave the terminal blocked.
func followLogs(ctx context.Context, client *Client, jobID string, limit int, stdout io.Writer) error {
	const pollInterval = 3 * time.Second
	if limit == 0 {
		limit = 1000
	}
	cursor := ""
	finishing := false
	initial := true
	drainedPages := 0
	for {
		wasInitial := initial
		previousCursor := cursor
		direction := "forward"
		if initial {
			direction = "backward"
		}
		logs, err := client.LogsPage(ctx, jobID, LogPageOptions{Limit: min(limit, cliLogPageSize), Direction: direction, Cursor: cursor})
		if err != nil {
			return err
		}
		renderCursor := ""
		if !logs.PaginationAvailable {
			renderCursor = cursor
		}
		next, err := renderLogEntries(stdout, logs.Items, renderCursor)
		if err != nil {
			return err
		}
		if logs.PaginationAvailable && wasInitial {
			cursor, err = forwardCursorAfterEntries(logs.Items)
			if err != nil {
				return err
			}
		} else if logs.PaginationAvailable && logs.Page.NextCursor != "" {
			cursor = logs.Page.NextCursor
		} else if next != "" {
			cursor = next
		}
		initial = false
		hasBacklog := !wasInitial && logs.Page.HasMore
		if hasBacklog {
			if cursor == "" || cursor == previousCursor {
				return fmt.Errorf("platform log cursor did not advance")
			}
			drainedPages++
			if drainedPages < followDrainPageLimit {
				continue
			}
		}
		if finishing && !hasBacklog {
			return nil
		}
		job, err := client.Status(ctx, jobID)
		if err != nil {
			return err
		}
		if isTerminalJobState(string(job.ObservedState)) {
			// One more pass collects lines written between the log read and the
			// status read, so the tail of a finished run is never truncated.
			finishing = true
		}
		drainedPages = 0
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func forwardCursorAfterEntries(entries []LogEntry) (string, error) {
	if len(entries) == 0 {
		return "", nil
	}
	latest, err := time.Parse(time.RFC3339Nano, entries[len(entries)-1].Timestamp)
	if err != nil {
		return "", fmt.Errorf("platform returned an invalid log timestamp")
	}
	// The initial page is a backward tail snapshot. Starting the forward poll
	// strictly after its newest timestamp avoids depending on Loki using the
	// same tie order for backward and forward queries.
	return latest.UTC().Add(time.Nanosecond).Format(time.RFC3339Nano), nil
}

func runCancel(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	client, jobID, format, err := parseJobCommand("cancel", arguments, getenv, stderr)
	if err != nil {
		return err
	}
	result, err := client.Cancel(ctx, jobID)
	if err != nil {
		return err
	}
	if format.json {
		return writeJSON(stdout, result)
	}
	_, err = fmt.Fprintf(stdout, "已请求停止 %s。\n", jobID)
	return err
}

func runConnect(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) error {
	// The documented form keeps the job ID immediately after the command:
	// `connect JOB_ID --worker 1`. Go's flag package stops parsing at the first
	// positional argument, so move that one trusted positional value behind the
	// remaining flags before parsing. The flags-first form remains supported.
	if len(arguments) > 1 && !strings.HasPrefix(arguments[0], "-") {
		reordered := append([]string(nil), arguments[1:]...)
		arguments = append(reordered, arguments[0])
	}
	set := flag.NewFlagSet("connect", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	worker := set.Int("worker", 0, "zero-based worker ordinal")
	if err := set.Parse(arguments); errors.Is(err, flag.ErrHelp) {
		_, writeErr := io.WriteString(stdout, "用法：spk-rayjob connect JOB_ID [--worker N]\n\n--worker 使用从 0 开始的 Worker 序号，默认 0。输入 exit 只退出连接，不会停止训练。\n")
		return writeErr
	} else if err != nil || set.NArg() != 1 || strings.TrimSpace(set.Arg(0)) == "" || *worker < 0 || *worker > 999 {
		return errors.New("connect requires a job ID and --worker between 0 and 999")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "正在连接 %s 的 Worker %d；输入 exit 退出，不会停止训练。\n", set.Arg(0), *worker)
	if inputFile, inputOK := stdin.(*os.File); inputOK && term.IsTerminal(int(inputFile.Fd())) {
		if outputFile, outputOK := stdout.(*os.File); outputOK && term.IsTerminal(int(outputFile.Fd())) {
			state, rawErr := term.MakeRaw(int(inputFile.Fd()))
			if rawErr != nil {
				return fmt.Errorf("prepare interactive terminal: %w", rawErr)
			}
			defer term.Restore(int(inputFile.Fd()), state)
		}
	}
	return client.ConnectWorker(ctx, set.Arg(0), *worker, stdin, stdout)
}

func parseJobCommand(command string, arguments []string, getenv func(string) string, stderr io.Writer) (*Client, string, outputFormatFlag, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	var format outputFormatFlag
	bindOutputFormatFlag(set, &format)
	if err := set.Parse(arguments); err != nil || set.NArg() != 1 || strings.TrimSpace(set.Arg(0)) == "" {
		return nil, "", format, fmt.Errorf("%s requires a job ID", command)
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return nil, "", format, err
	}
	return client, set.Arg(0), format, nil
}

func writeJSON(writer io.Writer, value any) error {
	if raw, ok := value.(json.RawMessage); ok {
		_, err := fmt.Fprintln(writer, string(raw))
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(writer, string(encoded))
	return err
}
