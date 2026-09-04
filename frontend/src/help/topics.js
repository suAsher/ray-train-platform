// The platform's usage knowledge lives in docs/ as thousands of lines across
// twenty manuals that users have no access to. The parts that decide whether a
// job succeeds are restated here and rendered in-app.
//
// Every entry is here because it caused a real failure or answers a question
// users actually asked, not because it was available to copy from a manual.

import {
  CONTRACT_SNIPPET,
  DEBUG_DEPS,
  DEBUG_SELFCHECK,
  MLFLOW_CODE,
  NATIVE_RAY_SUBMIT,
  RAY_DATA_CODE,
  RESUME_CODE,
  SMOKE_SCRIPT,
  SUBMIT_CACHE,
  SUBMIT_MULTINODE,
  SUBMIT_RAY_DATA,
  SUBMIT_RAY_DATA_STAGE,
  SUBMIT_RESUME,
  SUBMIT_SMOKE,
  SUBMIT_STREAMING,
} from './snippets.js'

export const helpSections = [
  {
    id: 'quickstart',
    group: '入门',
    title: '第一次跑通',
    summary: '先用下面这个最小示例确认平台链路是通的：环境变量能解析、输入目录读得到、GPU 可见、产物写得出去。这四件事是后面所有失败的根源，几分钟就能排除掉。确认之后再接你自己的代码。',
    blocks: [
      {
        kind: 'steps',
        items: [
          { title: '把这个脚本放进你的代码目录', body: '它不训练任何东西，只验证平台和代码之间的四个接口。', code: SMOKE_SCRIPT, codeLabel: 'train.py', codeLang: 'python' },
          { title: '在提交表单里填这条启动命令', body: '先选 1 卡。多卡由平台启动 torchrun，你不需要也不应该自己写。', code: 'python3 train.py', codeLabel: '训练启动命令', codeLang: 'bash' },
          { title: '选输入数据和运行环境', body: '输入目录先在「我的数据」里逐级点开确认真实存在——路径写错是最常见的失败，任务通常在两分钟内挂掉。运行环境选一个已登记的镜像即可。' },
          { title: '提交后看这三处', body: '日志里应出现输入目录和 CUDA 信息；任务详情的「训练产物」标签页应出现 hello.txt。三处都对，说明链路通了，可以换成你自己的训练脚本。' },
        ],
      },
    ],
  },
  {
    id: 'debug',
    group: '入门',
    title: '交互式调试环境',
    summary: '在带 GPU 的机器上先把代码和数据路径调通，再提交正式训练。省下的是每次改一行都要排队等一个任务的时间。',
    blocks: [
      {
        kind: 'steps',
        items: [
          { title: '选择与训练相同的镜像', body: '调试和训练用同一个环境，才能保证调通的东西提交后也能跑。' },
          { title: '启动单卡调试环境', body: '状态变为运行中后，从页面打开 JupyterLab、VS Code 或终端。它们连接的是带 GPU 的 Worker，不是 Ray Head。' },
          { title: '在 /workspace 写代码，在 /mnt/storage/* 看数据', body: '工作区内容和 .venv 会保留，调试 Pod 本身不是永久实例。', code: DEBUG_SELFCHECK, codeLabel: '进去先自检', codeLang: 'bash' },
          { title: '用完立刻停止', body: '调试环境会一直占着 GPU。每位用户同时只能有一个，换镜像也要先停掉当前的。' },
        ],
      },
      { kind: 'code', label: '临时装依赖', lang: 'bash', text: DEBUG_DEPS },
      {
        kind: 'warning',
        title: '不要把「运行时 apt 安装」当作生产方案',
        text: '容器以非 root 运行，apt 不可用；pip --user 和工作区虚拟环境只适合临时调试。需要 CUDA、系统库或可复现的团队环境时，构建镜像并让管理员按固定摘要登记。',
      },
      {
        kind: 'note',
        text: '多机问题不要用调试环境排查——编辑器只会连到其中一个不确定的 Worker。直接提交一个多机多卡任务去验证。',
      },
    ],
  },
  {
    id: 'storage',
    group: '数据',
    title: '数据从哪里读、写到哪里',
    summary: '容器里唯一稳定的东西是这几个环境变量。不要写死 TOS 地址、桶名、PVC 名或节点路径——它们在不同任务、不同数据模式下会解析到不同位置。',
    blocks: [
      {
        kind: 'table',
        headers: ['变量', '权限', '用途'],
        rows: [
          ['PLATFORM_DATASET_PATH', '只读', '本次任务选中的输入数据目录'],
          ['PLATFORM_OUTPUT_PATH', '读写', '本次任务独占的输出目录，权重和结果写这里'],
          ['PLATFORM_CHECKPOINT_PATH', '只读，可为空', '续训时选中的历史任务结果'],
          ['PLATFORM_CACHE_PATH', '临时读写，可为空', '本地 NVMe 缓存，任务结束即回收，不能作为唯一副本'],
          ['PLATFORM_JOB_ID', '只读', '本次任务 ID，可用于命名实验或日志'],
        ],
      },
      { kind: 'code', label: '最小 Python 改造', lang: 'python', text: CONTRACT_SNIPPET },
      {
        kind: 'table',
        headers: ['数据空间', '权限', '用途'],
        rows: [
          ['我的文件', '读写', '你的个人持久空间，训练结果和权重默认写在这里'],
          ['团队共享数据', '只读', '租户内已发布的数据，由管理员维护'],
          ['公共数据', '只读', '平台发布的公共数据集'],
          ['IDC 数据', '只读', '管理员登记的 IDC 只读导出'],
        ],
      },
      {
        kind: 'warning',
        title: '在 Python 里读，不要靠启动命令里的 shell 展开',
        text: '启动命令中的 $PLATFORM_OUTPUT_PATH 有可能在提交侧就被求值，展开成空字符串，训练随后会向类似 /run_dir 的根路径写入并报 PermissionError。用 os.environ 在训练进程内读取则不受影响。',
      },
      {
        kind: 'note',
        text: '数据真相始终在对象存储；GPU 节点的本地 NVMe 只是可丢弃缓存，Pod 回收即消失。权重和结果必须写 PLATFORM_OUTPUT_PATH，不能只留在缓存里。',
      },
    ],
  },
  {
    id: 'code',
    group: '训练',
    title: '代码怎么进来',
    summary: '镜像只提供环境，你的代码不进镜像——所以改完代码不需要重新构建镜像，换一次提交就行。代码有三条路进平台。',
    blocks: [
      {
        kind: 'table',
        headers: ['方式', '适合谁', '怎么用'],
        rows: [
          ['Git 提交', '代码已在仓库里（推荐）', '填仓库地址和 commit。改完 push，重新提交时换 commit 即可，最适合日常迭代'],
          ['个人工作区快照', '不用 Git 的人', '先把代码传到「我的数据」的工作区，创建一次快照，提交时选它'],
          ['上传代码 ZIP', '一次性试跑', '在「我的数据」直接上传打包好的代码'],
        ],
      },
      {
        kind: 'list',
        items: [
          '代码包按 .gitignore 和 .rayignore 排除文件。注意别把编译产物打进去覆盖镜像里已编译的扩展，那会得到 No module named ... 之类的报错。',
          '也别把数据、权重和密钥打进代码包——它们该待在数据空间里。',
          '运行环境缺少依赖时，联系管理员登记新镜像。用户不能自行登记镜像，这是有意的：镜像是共享的、不可变的环境基线。',
        ],
      },
    ],
  },
  {
    id: 'submit',
    group: '训练',
    title: '提交任务与分布式训练',
    summary: '网页提交把这些选项做成了表单；命令行提交需要自己写。两边是同一套参数。',
    blocks: [
      { kind: 'code', label: '① 单卡冒烟', lang: 'bash', text: SUBMIT_SMOKE },
      { kind: 'code', label: '② 多机多卡', lang: 'bash', text: SUBMIT_MULTINODE },
      { kind: 'code', label: '③ 续训', lang: 'bash', text: SUBMIT_RESUME },
      {
        kind: 'table',
        headers: ['参数', '含义'],
        rows: [
          ['--name', '任务显示名，可重复，实际身份由平台分配的 ID 决定'],
          ['--image', '运行环境镜像；不写则用项目默认值或平台默认镜像'],
          ['--entrypoint', '训练启动命令，一条普通 python3 命令，不要写 torchrun'],
          ['--engine', 'ray-ddp（默认）或 ray-train（托管 Worker、故障恢复、Checkpoint）'],
          ['--input-space / --input-path', '输入数据的空间与子目录，对应 PLATFORM_DATASET_PATH'],
          ['--data-mode', 'mount / cache / ray-data-stage / ray-data / streaming，见「五种数据模式」'],
          ['--resume-from-job', '把某次运行的结果目录作为只读 checkpoint 传入本次'],
          ['--max-failures', 'ray-train 下 Worker 最大恢复次数（0-10）'],
          ['--checkpoint-every-epochs', 'ray-train 下每隔多少 Epoch 保存一次'],
          ['--cache-mode / --cache-size / --cache-preload', '本地 NVMe 缓存，见「缓存加速」'],
          ['--watch', '阻塞显示排队 → 运行 → 结束'],
          ['--output json', '输出原始 JSON，供脚本解析'],
        ],
      },
      {
        kind: 'table',
        headers: ['规模字段', '取值约束'],
        rows: [
          ['executionMode: single_gpu', '1 个 Worker × 1 卡。先用它跑通。'],
          ['executionMode: torchrun', '1 个 Worker × 至少 2 卡，单机多卡。'],
          ['executionMode: ray_train', '至少 2 个 Worker × 每个至少 1 卡，多机多卡。'],
          ['workers', 'Worker 数，等于参与训练的物理节点数。'],
          ['gpusPerWorker', '每个 Worker 独占的 GPU 数。总卡数 = workers × gpusPerWorker。'],
        ],
      },
      {
        kind: 'list',
        items: [
          '分布式由平台负责：它按所选规模在每个 Worker 内启动 torchrun，并注入 RANK、WORLD_SIZE 等变量。你的代码只需正常使用 DDP 与 DistributedSampler。',
          '所有 rank 都要用 DistributedSampler，但只让 rank 0 写 checkpoint 和日志文件——多个 rank 同时写同一个文件在对象存储上会失败。',
          '逐级放大：先 1 卡，再单机多卡，最后多机。每一级都核对 loss、样本数和 checkpoint 再往上走。',
        ],
      },
      { kind: 'code', label: '原生 ray job submit', lang: 'bash', text: NATIVE_RAY_SUBMIT },
      {
        kind: 'warning',
        title: '原生提交不写元数据就是 1 卡',
        text: '不带 ray-platform 元数据时，平台按默认的 1 卡环境执行。一旦写了其中任意一项，就必须把这一组写全，避免打字错误产生一个半配置的任务。多机多卡、数据目录选择和续训建议优先用 spk-rayjob。',
      },
    ],
  },
  {
    id: 'data-mode',
    group: '数据',
    title: '五种数据模式怎么选',
    summary: '默认用 mount。只有当你确认瓶颈在数据读取上时，才需要换其他模式——换错了不会更快，只会多一层复杂度。注意最后一列：有两种模式要求你改训练代码。',
    blocks: [
      {
        kind: 'table',
        headers: ['模式', '什么时候用它', '要改代码吗'],
        rows: [
          ['mount', '默认。直接读已授权的数据挂载，不做任何缓存。先用它跑通，再谈优化。', '不用'],
          ['cache', '同一份数据要被反复读很多轮。启动时预热到本地 NVMe，之后从本地读。', '不用'],
          ['ray-data-stage', '海量小文件、读取延迟是瓶颈。Ray Data 分布式读取并在本地建立完整视图，再交给你原来的 DataLoader。', '不用'],
          ['ray-data', 'Ray Data 直接把分片交给每个 Train Worker，不再经过本地文件。', '要改'],
          ['streaming', '固定一个不可变数据集版本，按需流式读取，保证多次训练读到完全相同的数据。', '要改'],
        ],
      },
      {
        kind: 'note',
        text: '不确定就选 mount。先测出数据读取确实是瓶颈（比如训练日志里 data_time 明显高于 0），再考虑换模式。前三种模式对训练代码是透明的，可以随时切换对比。',
      },
    ],
  },
  {
    id: 'cache',
    group: '数据',
    title: '如何使用缓存加速',
    summary: '把输入数据预热到 GPU 节点的两块本地 NVMe，之后训练从本地盘读。训练代码一行都不用改。',
    blocks: [
      { kind: 'code', label: '提交命令', lang: 'bash', text: SUBMIT_CACHE },
      {
        kind: 'warning',
        title: '不加 --cache-preload input 就不会预热数据',
        text: '只写 --cache-mode runtime 时，缓存盘只用于 Ray 临时文件和训练代码主动写入的内容，输入数据仍然从对象存储直接读。要预热输入必须显式加 --cache-preload input，并指定具体的输入目录。',
      },
      {
        kind: 'list',
        items: [
          '预热本身要花时间。短任务不一定收回这个成本，长训练和多 epoch 才是它的主场——同一份数据读得越多轮越划算。',
          '缓存是可丢弃的临时卷，随 Pod 回收。数据真相仍在对象存储，权重和结果必须写 PLATFORM_OUTPUT_PATH。',
          '代码仍然读 PLATFORM_DATASET_PATH，不要在代码里写 /data1、/data2 这类节点路径。',
        ],
      },
    ],
  },
  {
    id: 'ray-data',
    group: '数据',
    title: '如何使用 Ray Data',
    summary: 'Ray Data 有两种用法，差别很大：一种对你的代码透明，另一种要求你改训练循环。',
    blocks: [
      {
        kind: 'steps',
        items: [
          {
            title: 'ray-data-stage：不改代码',
            body: 'Ray Data 只负责把数据分布式读取到每个训练节点的本地盘，建立完整视图后，仍然由你原来的 DataLoader 读取。适合海量小文件被远端延迟拖慢的场景。必须选择具体的数据子目录，不能选整个根目录——那会把多 TB 数据复制到每个节点。',
            code: SUBMIT_RAY_DATA_STAGE,
            codeLabel: '提交命令',
            codeLang: 'bash',
          },
          {
            title: 'ray-data：要改代码',
            body: 'Ray Data 直接把分片交给每个 Train Worker，不再落地成本地文件。只能配合 ray-train 引擎使用，且不会自动开启 NVMe。',
            code: SUBMIT_RAY_DATA,
            codeLabel: '提交命令',
            codeLang: 'bash',
          },
        ],
      },
      { kind: 'code', label: '训练代码怎么消费分片', lang: 'python', text: RAY_DATA_CODE },
      {
        kind: 'warning',
        title: '用 ray-data 时必须去掉手工分片',
        text: '分片已经由 Ray Data 完成。如果代码里还留着 DistributedSampler 或自己按 rank 切分数据，两层分片会叠加，每张卡实际只看到应有数据的一小部分，loss 看起来正常但训练是错的。',
      },
    ],
  },
  {
    id: 'streaming',
    group: '数据',
    title: 'Ray Train 托管 + Ray Data + Parquet + NVMe',
    summary: '把小文件预先打包成 Parquet 分片、固定成不可变数据版本，由 Ray Train 托管的 Worker 通过 Ray Data 按需流式读取，本地 NVMe 作为有界工作集。这是为超出单机容量的数据集准备的路径。',
    blocks: [
      {
        kind: 'warning',
        title: '这条链路尚未完成全量验证',
        text: '各个环节已分别验证，但多机全量训练的端到端验证还没有完成，正在等待新节点。这里先给出用法，便于后续验证时对照；在验证通过之前，正式训练请使用 mount、cache 或 ray-data-stage。',
      },
      { kind: 'code', label: '提交命令', lang: 'bash', text: SUBMIT_STREAMING },
      {
        kind: 'list',
        items: [
          '为什么要打包 Parquet：对象存储是按延迟计价的，读一个文件的固定开销远大于读它的内容。把大量小文件合并成分片后，同样的数据量所需的请求次数下降一个数量级。',
          '为什么要版本化：每个版本是不可变的，内容用摘要固定。多次训练引用同一版本，读到的数据保证完全一致，实验才可比。',
          'NVMe 在这里的角色是有界工作集，不是全量副本——数据集比本地盘大得多，所以只缓存当前用到的分片。',
          '训练代码的消费方式与 ray-data 相同：从 Ray Train 拿分片，不要自己再分片。',
        ],
      },
    ],
  },
  {
    id: 'observability',
    group: '排查',
    title: '可观测性与训练诊断',
    summary: '任务详情页有四个标签页，分别回答"在跑什么、跑得怎么样、产出了什么、跑在哪里"。',
    blocks: [
      {
        kind: 'table',
        headers: ['看什么', '在哪看'],
        rows: [
          ['实时日志', '任务详情 →「实时聚合日志」，可跟随输出'],
          ['Loss 曲线与训练指标', '任务详情 →「Loss 收敛曲线与指标」，含每张卡的 GPU 利用率'],
          ['权重和结果文件', '任务详情 →「训练产物」，可直接下载 checkpoint'],
          ['Pod 与节点分布', '任务详情 →「Pod 副本与节点拓扑」，确认卡真的分散到了预期节点'],
          ['Ray 原生视图', '任务详情页顶部的「Ray Dashboard」按钮，仅在本任务集群运行期间可用'],
          ['跨任务实验对比', '左侧「实验中心」，MLflow 记录的参数与指标'],
        ],
      },
      { kind: 'code', label: '记录 MLflow 指标', lang: 'python', text: MLFLOW_CODE },
      {
        kind: 'list',
        items: [
          '日志要加 flush=True，否则缓冲区可能迟迟不刷新，界面上看着像卡住了。',
          '训练慢时先看 GPU 利用率曲线：每张卡都在忙说明是算力上限；有卡长期空闲说明卡在数据读取或同步上。',
          '数据读取是不是瓶颈，看训练日志里的 data_time——明显高于 0 才需要考虑换数据模式。',
          'Ray Dashboard 只在任务的集群存活期间可用；任务结束后集群被回收，改看日志和指标。',
        ],
      },
    ],
  },
  {
    id: 'resume',
    group: '训练',
    title: '断点续训',
    summary: '平台不会替你猜哪个 checkpoint 可以恢复。可靠的续训需要两件事：训练脚本支持 resume，以及提交时显式选择要恢复的结果。',
    blocks: [
      { kind: 'code', label: '训练代码', lang: 'python', text: RESUME_CODE },
      { kind: 'code', label: '提交命令', lang: 'bash', text: SUBMIT_RESUME },
      {
        kind: 'warning',
        title: '「提交失败重试」不是续训',
        text: '页面上的重试只会重新创建一次提交流程，用来对付镜像拉取失败、节点中断这类提交期故障，它会从头重跑。只有训练脚本支持 resume、并且你显式选了 checkpoint，才是真正的断点续训。',
      },
      {
        kind: 'list',
        items: [
          'checkpoint 必须写进 PLATFORM_OUTPUT_PATH，否则下次任务读不到——本地缓存盘随 Pod 一起消失。',
          '只让 rank 0 写 checkpoint。多个 rank 同时写同一个文件在对象存储上会失败。',
          '用 ray-train 引擎时可以让平台按 Epoch 周期保存并控制保留数量，见 --checkpoint-every-epochs 与 --checkpoint-keep-latest。',
        ],
      },
    ],
  },
  {
    id: 'artifacts',
    group: '训练',
    title: '取回训练结果与权重',
    summary: '训练写进输出目录的东西，有两个地方可以拿到，都可以直接下载。',
    blocks: [
      {
        kind: 'table',
        headers: ['入口', '适合什么时候'],
        rows: [
          ['任务详情 →「训练产物」', '知道是哪次任务，按任务追溯它的输出'],
          ['「我的数据」浏览个人空间', '只记得大概路径，想按目录翻找'],
        ],
      },
      {
        kind: 'list',
        items: [
          '权重文件（.pth / .pt / .ckpt / .onnx / .safetensors）可以直接下载到本地。',
          '文本和图片可以在页面上直接预览，不用先下载。',
          '平台不会暴露底层对象存储地址或访问凭据——你拿到的是文件本身。',
          '训练结果不会自动复制到实验中心。实验中心记录的是参数和指标，权重仍在你的输出目录里。',
        ],
      },
    ],
  },
  {
    id: 'datasets',
    group: '数据',
    title: '版本化数据集',
    summary: '把一份数据固定成不可变版本，训练记录版本号与摘要。之后数据再更新，也不会改变已经提交过的任务读到的内容。',
    blocks: [
      {
        kind: 'list',
        items: [
          '解决的问题是实验可比性：两次训练如果读到的数据悄悄变了，指标差异就无法归因。',
          '在「版本化数据集」页选择已发布的逻辑数据集和它的某个版本；提交时引用这个版本。',
          '原始同步区和版本化训练集不是一回事——前者会持续变化，后者一经发布就固定。',
          '该功能按团队开放。没有开放时，页面会说明，你仍可以照常用现有数据目录提交训练。',
        ],
      },
    ],
  },
  {
    id: 'access',
    group: '进阶',
    title: '令牌与私有仓库凭据',
    summary: '在「账户与安全」里管理。集群外提交和拉取私有代码各需要一样东西。',
    blocks: [
      {
        kind: 'table',
        headers: ['要做什么', '需要什么'],
        rows: [
          ['从自己的机器用 spk-rayjob 提交', '个人访问令牌（PAT）'],
          ['用原生 ray job submit 提交', '同一个 PAT，放进请求头'],
          ['拉取你自己的私有 Git 仓库', '个人 Git 凭据'],
        ],
      },
      {
        kind: 'list',
        items: [
          'PAT 只在创建时显示一次，关掉窗口就看不到了，创建后立刻复制走。',
          'PAT 的权限是最小集：提交、查看任务、上传代码。请设置过期时间，并在设备不再使用时撤销。',
          '撤销后该设备立即不能提交、查看或取消任务。',
          'Git 凭据只写入本团队的 Kubernetes Secret，不会在页面或数据库里回显；你自己的凭据优先于团队公共凭据。',
        ],
      },
    ],
  },
  {
    id: 'quota',
    group: '进阶',
    title: '配额与排队',
    summary: 'GPU 是共享的。任务提交后先进队列，拿到卡才开始跑，所以"提交成功"不等于"已经在训练"。',
    blocks: [
      {
        kind: 'list',
        items: [
          '左下角显示你当前的 GPU 配额。任务详情里的状态原因会说明它在等什么。',
          '一直排队通常是两种情况：团队配额已用满，或者集群暂时没有满足规模要求的空闲卡。',
          '最常见的浪费是没停掉的调试环境——它会一直占着卡，却没有在训练。先去看看还有没有开着的。',
          '多机任务需要同时拿到多个节点的卡，比单机任务更容易排队。先用小规模验证代码，再申请大规模。',
        ],
      },
    ],
  },
  {
    id: 'errors',
    group: '排查',
    title: '常见错误速查',
    summary: '',
    blocks: [
      {
        kind: 'table',
        headers: ['错误', '含义', '处理'],
        rows: [
          ['python: not found', '镜像里只有 python3', '启动命令统一用 python3'],
          ['KeyError: RANK', '代码强制走分布式入口，但任务是单卡直接执行', '单卡走非分布式分支，或改用多卡执行模式'],
          ['PermissionError，路径像 /run_dir', '启动命令里的 $PLATFORM_* 展开成了空值', '改成在 Python 里用 os.environ 读取'],
          ['FileNotFoundError，指向数据目录', '选中的输入路径不存在', '先在「我的数据」里逐级点开确认目录真实存在'],
          ['No module named mmdet3d.ops', '上传的源码覆盖了镜像里编译好的扩展', '不要把编译产物打进代码包'],
          ['每张卡看到的数据变少了', 'ray-data 分片和手工 rank 分片叠加', '去掉 DistributedSampler 和自己的 rank 切分'],
          ['任务一直排队', 'GPU 配额或空闲卡不足', '看任务状态原因；检查是否有没停掉的调试环境占着卡'],
          ['日志看不到末尾 loss', '模型结构日志过长把后面顶掉了', '用日志流跟随，或调大返回条数'],
          ['写文件报 EPERM 或权限错误', '多个 rank 同时写同一个文件', '只让 rank 0 写，或给每个 rank 用不同文件名'],
        ],
      },
    ],
  },
  {
    id: 'preflight',
    group: '排查',
    title: '提交前自检',
    summary: '八卡任务因为一个路径写错而在两分钟内失败，是这套平台上最常见的浪费。',
    blocks: [
      {
        kind: 'checklist',
        items: [
          '输入目录已在「我的数据」里点开确认存在，不是凭记忆写的路径。',
          '代码读 PLATFORM_DATASET_PATH、写 PLATFORM_OUTPUT_PATH，没有写死任何绝对路径。',
          '先用 1 卡最小批量跑通至少几个 step，再扩到多卡多机。',
          '所有 rank 都用 DistributedSampler，只有 rank 0 写 checkpoint 和日志文件。',
          '启动命令里没有 torchrun。',
          '如果用了 ray-data，已经去掉手工分片。',
          '调试环境用完已经停掉，不要空占 GPU。',
        ],
      },
    ],
  },
]
