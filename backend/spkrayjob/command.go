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
		return errors.New("command is required")
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	if stdin == nil {
		stdin = os.Stdin
	}
	switch arguments[0] {
	case "version":
		return runVersion(arguments[1:], stdout)
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
	case "datasets":
		return runDatasets(ctx, arguments[1:], stdout, stderr, getenv)
	case "dataset":
		return runDataset(ctx, arguments[1:], stdout, stderr, getenv)
	case "status":
		return runStatus(ctx, arguments[1:], stdout, stderr, getenv)
	case "logs":
		return runLogs(ctx, arguments[1:], stdout, stderr, getenv)
	case "cancel":
		return runCancel(ctx, arguments[1:], stdout, stderr, getenv)
	default:
		return fmt.Errorf("unknown command")
	}
}

const helpText = `spk-rayjob — 分布式训练任务命令行客户端

日常用法：
  spk-rayjob init                    在当前代码目录生成 .spk-rayjob.yaml 提交默认值
  spk-rayjob submit --watch          按默认值提交当前目录并等待结束
  spk-rayjob jobs                    列出我的任务
  spk-rayjob datasets                列出我有权使用的数据集
  spk-rayjob dataset versions <数据集>  列出不可变数据版本
  spk-rayjob status <JOB ID>         查看单个任务
  spk-rayjob logs -f <JOB ID>        实时跟随日志
  spk-rayjob cancel <JOB ID>         停止任务

首次使用：
  spk-rayjob login --server https://<平台地址> --username <账号> --password-stdin

断点续训（把上一次运行的结果目录作为只读 checkpoint 传入本次运行）：
  spk-rayjob submit --resume-from-job <上一次的 JOB ID> --watch

临时缓存（可选）：
  spk-rayjob submit --cache-mode runtime --cache-size 1Ti \
    --cache-preload input --input-space public --input-path <数据集目录>
  加上 --cache-preload input 后，平台会在每个 Worker 启动前把所选输入预热到双 NVMe；
  不加该参数时不会自动缓存 /mnt/storage/public，只加速 Ray 临时文件和训练代码主动写入缓存的内容。

训练引擎：
  --engine ray-ddp    默认；兼容现有 Actor + torchrun 单机/多机 DDP
  --engine ray-train  Ray Train 托管 workers、故障恢复和 Checkpoint；仅在平台开启后可用
  --data-mode mount            直接读取已授权数据挂载
  --data-mode cache            使用现有 NVMe 预热器
  --data-mode ray-data-stage   Ray Data 分布式读取并生成双 NVMe 本地视图
  --data-mode ray-data --ray-data-format images --ray-data-path images/train
                              直接把 Parquet/图片数据分片交给用户的 Ray Data 训练代码
  --data-mode streaming --dataset <数据集>:<版本> --dataset-cache-policy bounded
  --dataset-sites cnfzhjyg,cnzshytg（可选；留空使用完整版本，启动时校验场地及样本数）
                              固定不可变数据集版本，由 Ray Data 按需流式读取
  --max-failures 2                 ray-train Worker 最大恢复次数（0-10）
  --checkpoint-every-epochs 1      ray-train 每隔多少 Epoch 保存 Checkpoint
  --checkpoint-keep-latest 3       ray-train 保留最近 Checkpoint 数
  --checkpoint-keep-best 1         ray-train 保留最佳 Checkpoint 数
  客户端不接受 Ray 版本参数；版本由平台根据管理员登记的镜像固化。

通用参数：
  --output json    输出原始 JSON，供脚本使用（默认为可读文本）
  --server         平台地址；未提供时读取 SPK_RAYJOB_URL 或已保存的登录配置
  --config         指定仅属主可读的配置文件
  --debug          将脱敏后的请求诊断写入 stderr

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

提交前自检：
  先用 1 卡最小批量跑通几个 step，再扩到多卡多机；
  所有 rank 使用 DistributedSampler，只有 rank 0 写 checkpoint；
  输入目录先确认真实存在 —— 路径写错的多卡任务通常两分钟内就失败。

常见错误：
  python: not found              镜像只有 python3，入口命令改用 python3
  KeyError: RANK                 代码强制走分布式入口，但任务是单卡直接执行
  PermissionError 且路径像 /run_dir  entrypoint 里的 $PLATFORM_* 展开成了空值
  FileNotFoundError 指向数据目录     选中的输入路径不存在，先在 Portal 里确认
  No module named mmdet3d.ops    上传的源码覆盖了镜像里编译好的扩展
  任务一直排队                    GPU 配额或空闲卡不足；检查是否有调试环境空占卡

注意：单机多卡与多机多卡由平台负责启动 torchrun。entrypoint 里请写
      python tools/train.py ...，不要自己再写 torchrun 或 torchpack。
`

func runHelp(stdout io.Writer) error {
	_, err := fmt.Fprint(stdout, helpText)
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
		TrainingEngine: engine,
		DataMode:       dataMode,
		DatasetRef:     datasetRef,
		CachePolicy:    cachePolicy,
		Entrypoint:     domain.Entrypoint{Command: []string{"/bin/sh", "-lc", strings.TrimSpace(value.Entrypoint)}},
		Execution:      execution,
		Resources:      domain.Resources{WorkerReplicas: workers, GPUsPerWorker: gpus, CPUPerWorker: cpu, MemoryPerWorker: memory},
		Input:          input,
		Checkpoint:     checkpoint,
		Output:         output,
		Cache:          cacheRequest,
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
