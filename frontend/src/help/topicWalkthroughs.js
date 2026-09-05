// Task-specific checkpoints complement the detailed examples in topics.js.
const link = (label, to) => ({ label, to })

export const walkthroughs = {
  quickstart: {
    prerequisites: ['已登录、有可用训练镜像和单卡配额；把 train.py 放入 Git、工作区快照或代码 ZIP，并在提交页明确选择这个代码来源。', '示例中的尖括号必须替换；数据路径从「我的数据」复制，不假定示例目录在你所在团队存在。CLI 先按「外部提交」安装并登录，在代码目录执行；检查已有项目配置，显式参数覆盖配置。'],
    success: ['任务成功结束；日志有输入目录、CUDA 信息，训练产物列表可见 hello.txt。'],
    troubleshooting: ['排队先查配额；找不到 train.py 核对代码版本和相对入口；目录无权访问先在我的数据确认权限，不用扩卡排查。'],
    relatedLinks: [link('新建任务', '/job/create'), link('外部提交与安装', '/external-submit')],
  },
  debug: {
    prerequisites: ['先选与正式训练一致的镜像。只检查文件与依赖时选无卡调试，需要 CUDA 才申请 GPU。'],
    success: ['终端可打开，数据目录可读；代码保存到 /workspace 后创建快照，正式提交明确选择该快照。'],
    troubleshooting: ['容器系统目录的临时修改不保证保留；/workspace/.venv 保留不等于正式训练自动激活它。把依赖固定到 requirements 或镜像，并在训练入口验证导入。无卡环境 nvidia-smi 无 GPU 是正常行为。'],
    relatedLinks: [link('调试环境', '/devcenter'), link('工作区与快照', '/datacache')],
  },
  storage: {
    prerequisites: ['在「我的数据」选空间并逐级浏览；public 的容器挂载是 /mnt/storage/public，个人空间可在 /mnt/storage/me 查看，训练仍优先使用 PLATFORM_DATASET_PATH。'],
    success: ['从输入变量读到所选目录；写入输出变量的文件可在任务产物和个人空间找到。'],
    troubleshooting: ['公共和团队数据只读，上传请切到「我的文件」。容量不足、目录权限或路径错误不是换文件后缀能解决的；保留报错和目标路径联系管理员，不往根目录或缓存保存唯一结果。'],
    relatedLinks: [link('我的数据', '/datacache'), link('任务列表', '/job')],
  },
  code: {
    prerequisites: ['确认入口相对于代码根目录存在，训练所需依赖在选定镜像中；提交前排除数据、权重、密钥和不兼容编译扩展。'],
    success: ['Git：填仓库和分支后点击「解析」，看到固定 Commit 再继续；调试快照：在我的工作区创建版本再回提交页选择；ZIP：用专用入口上传并完成校验，再选择生成的不可变代码包。'],
    troubleshooting: ['改了 Git 分支或工作区不改变已提交版本，需重新解析或创建快照。Git 解析失败先检查账户凭据和仓库权限；普通文件上传与专用代码 ZIP 上传不可混用。'],
    relatedLinks: [link('代码与提交', '/job/create'), link('我的工作区', '/datacache'), link('Git 凭据', '/account-security')],
  },
  submit: {
    prerequisites: ['CLI 从代码根目录运行，并先核对 spk-rayjob --help。示例只展示不同模式的关键参数；镜像、代码、输入与资源应在提交预览确认，不能依赖未知项目默认值。', 'ray-train 必须使用已接入平台托管协议的入口；UI/CLI 会处理受支持的 Python 命令规范化，直接调用 API 时遵守托管入口的 python 相对脚本或 python -m 模块格式。'],
    success: ['提交预览的引擎、Worker 数、每 Worker 卡数和数据范围正确；任务拓扑与预期一致，所有 rank 等步完成且输出可读。'],
    troubleshooting: ['普通脚本不因选择 ray-train 就自动获得 checkpoint 和 Ray Data 支持。入口校验失败不要绕过校验用 shell 包装；核对适配器、相对入口和镜像。'],
    relatedLinks: [link('提交任务', '/job/create'), link('外部提交', '/external-submit')],
  },
  'data-mode': {
    prerequisites: ['先量出数据等待时间、所选数据量和 NVMe 可用预算；明确训练代码是文件 DataLoader 还是平台 Ray Data 适配器。'],
    success: ['切换模式后样本数、split、loss 与基线一致，再比较预热加训练的总耗时，而不只比较热缓存单步时间。'],
    troubleshooting: ['全量超出本地容量时，不使用完整预热绕过限制；选兼容 streaming 或缩小目录。模式变化不能自动修复输入字段不匹配。'],
    relatedLinks: [link('数据与缓存', '/datacache'), link('版本化数据集', '/datasets')],
  },
  cache: {
    prerequisites: ['确认所选目录大小加临时文件能放进申请缓存；CLI 显式设置 cache-preload input，不选公共根目录做预热。'],
    success: ['日志显示预热完成后进入训练；比较首读、热读、data_time 和总耗时，训练仍读 PLATFORM_DATASET_PATH。'],
    troubleshooting: ['预热未结束时没有 loss 不一定卡住，先看数据准备日志。ENOSPC/容量不足应缩小数据或调整获批预算，不删除其他任务缓存；只读一轮可能不值得预热。'],
    relatedLinks: [link('我的数据', '/datacache'), link('任务状态', '/job')],
  },
  'ray-data': {
    prerequisites: ['ray-data 要求 ray-train 与兼容训练适配器。示例 image 字段只适用于 images reader；先检查实际 schema、标签来源、张量维度与 dtype。代码片段不是完整的模型训练入口。'],
    success: ['适配器在 worker 内拿到 train shard，完成前向、反向、优化器更新和平台指标/Checkpoint 上报；各 rank 步数一致，epoch 边界明确。'],
    troubleshooting: ['shard 为空先查模式和训练上下文；KeyError image 应检查读取格式而非改字段猜测。不要叠加 DistributedSampler；不等步可能引发 DDP 等待，应按平台等步策略处理尾批。'],
    relatedLinks: [link('提交配置', '/job/create'), link('版本数据', '/datasets')],
  },
  streaming: {
    prerequisites: ['管理员已发布 READY 的兼容版本，训练镜像与模型入口支持该 schema；按场地训练还要求 manifest 有 site_id，镜像支持场地筛选 protocol 1。', '先在 UI 选择数据集、版本和一个或多个场地；留空选择全量。CPU 检查不等于实际多卡性能已验证。'],
    success: ['启动没有 manifest 或场地校验错误，再核对筛选后的训练样本数及补齐策略；各 rank 等步，数据摘要和范围可追溯，NVMe 使用不持续无界增长。'],
    troubleshooting: ['旧镜像协议不兼容需选新镜像；缺 site_id 需管理员发布新版本。未知场地或该场地无训练样本会明确失败。预检的全版本样本数不是筛选后数量，不能拿它验算局部训练步数。'],
    relatedLinks: [link('版本与场地', '/datasets'), link('新建训练', '/job/create')],
  },
  observability: {
    prerequisites: ['从任务列表打开具体任务；确认代码有带 step 的指标输出或 MLflow 记录，平台不会自动理解任意打印格式。'],
    success: ['能关联任务 ID、日志时间、训练 step、GPU 指标和产物；跨任务比较时实验参数已记录。'],
    troubleshooting: ['没有曲线不等于训练停止，先看日志和 GPU；MLflow 为空先查代码是否记录、是否只有 rank 0 写。任务结束后 Ray Dashboard 不再可用，历史日志/指标以平台实际保留范围为准，关键记录及时导出。'],
    relatedLinks: [link('任务列表', '/job'), link('实验中心', '/experiments')],
  },
  diagnose: {
    prerequisites: ['固定镜像、代码、版本和场地，记录稳定区间至少数百步；区分首读、预热与热读。'],
    success: ['找到可复现的瓶颈信号，单次只改一个参数做 A/B，吞吐改善且样本数和数值结果没有异常。'],
    troubleshooting: ['向管理员提供任务 ID、发生时间、节点/卡数、data_time、单步均值/P95、数据模式、缓存预算和相关日志；不要发送令牌。创建 PKL 慢应单独测数据准备阶段，不能由 GPU 利用率推断预处理已优化。'],
    relatedLinks: [link('任务与日志', '/job'), link('实验对比', '/experiments')],
  },
  scaling: {
    prerequisites: ['固定数据版本与场地、模型和优化器语义；有梯度累积时，有效全局 Batch = 每卡微批量 × 总卡数 × 累积步数。'],
    success: ['保存对比记录：规模｜微批量｜累积步数｜有效 Batch｜稳定区间｜单步耗时｜样本/秒｜epoch 耗时｜data_time 占比。按实际处理样本数计算吞吐，区别 micro-step 和 optimizer-step。'],
    troubleshooting: ['扩展效率下降先分清通信与数据等待，不为追求吞吐随意改采样或验证集；近似线性加速不是所有模型都能达到的保证。'],
    relatedLinks: [link('实验中心', '/experiments'), link('任务列表', '/job')],
  },
  resume: {
    prerequisites: ['确认原任务存在可读 checkpoint，入口实现了完整恢复。仅 model 权重用于微调；真正续训还要优化器、调度器、epoch/step，必要时 AMP scaler、随机数及采样状态。'],
    success: ['启动日志显示恢复来源和非零 epoch/step；学习率与优化器状态延续，输出写入新任务目录，不覆盖只读来源。'],
    troubleshooting: ['找不到 latest.pth 时先浏览原任务实际文件名，示例路径并非所有框架通用。模型/配置/数据范围变化应作为新实验；streaming 续训保留原版本与场地。自动故障恢复依赖适配器上报的 Checkpoint，不是设置重试次数就能保证。'],
    relatedLinks: [link('原任务与产物', '/job'), link('续训提交', '/job/create')],
  },
  artifacts: {
    prerequisites: ['训练将文件写入 PLATFORM_OUTPUT_PATH，并完成关闭/保存；先记下任务 ID 和文件相对路径。'],
    success: ['从任务产物或个人空间下载；本地文件大小与页面一致，重要权重再用训练侧记录的 SHA-256 验证。'],
    troubleshooting: ['大文件下载留足本地容量并保持网络；失败先重新登录再重试，浏览器能否续传取决于实际下载链路，不保证自动断点续传。文件找不到先查训练保存日志和输出变量，别去缓存找唯一副本。'],
    relatedLinks: [link('训练产物入口', '/job'), link('个人文件', '/datacache')],
  },
  datasets: {
    prerequisites: ['团队已开放版本化数据；发布由具备发布权限的人完成，普通用户选择已就绪版本。原始公共目录不是自动可训练的 Parquet 版本。'],
    success: ['选择 READY 版本，核对 schema、train/val/test 数量、来源和摘要；提交时固定版本，可选场地并记录实验范围。'],
    troubleshooting: ['发布中需等待；FAILED 查看发布错误并由发布者修复后重发，不引用失败版本。旧版本缺场地元数据不能原地补写；发布新版本并使用兼容镜像。'],
    relatedLinks: [link('版本化数据集', '/datasets'), link('提交训练', '/job/create')],
  },
  access: {
    prerequisites: ['打开账户与安全创建 PAT，选择合理有效期并安全保存一次性显示的值；再按外部提交页面当前系统的登录命令操作。'],
    success: ['CLI 登录验证成功；私有仓库凭据配置后提交页能解析出固定 Commit。'],
    troubleshooting: ['401 先检查到期/撤销并重新登录，403 查账号权限和团队范围；登录 PAT 不代替 Git 凭据。不要把令牌写入 Git、截图或工单；遗失时撤销旧令牌再新建。'],
    relatedLinks: [link('账户与安全', '/account-security'), link('登录命令', '/external-submit')],
  },
  quota: {
    prerequisites: ['提交前确认所需总卡数和规格，查看自己占用的调试环境及当前配额。'],
    success: ['任务从排队进入运行，实际 Pod/卡数匹配；等待期间不重复提交同一任务。'],
    troubleshooting: ['先读状态原因：配额不足、资源不足、镜像拉取或调度约束处理方式不同。可停止自己不用的调试环境，或新建小规模验证；长期不动提供任务 ID 和状态原因联系管理员，不停止他人任务。'],
    relatedLinks: [link('任务与资源状态', '/job'), link('我的调试环境', '/devcenter')],
  },
  errors: {
    prerequisites: ['保留完整错误、发生时间和任务/上传目标，不只截最后一行；区分提交、准备数据、训练与上传阶段。'],
    success: ['按原因修复后只重试失败的操作；确认任务/文件结果，而不是仅看 HTTP 成功。'],
    troubleshooting: ['401：重新登录；403：检查空间/团队权限；413：确认使用最新版分片上传，仍失败请提供报错分片和时间排查代理限制，不必切割原权重。', '上传中断：保留原文件，重新选择同一文件；旧镜像协议失败：选择支持 protocol 1 的镜像；缺 site_id：重新发布；场地不存在：核对代码与训练 split，禁止静默全量替代。'],
    relatedLinks: [link('我的文件', '/datacache'), link('任务日志', '/job')],
  },
  preflight: {
    prerequisites: ['已确认代码版本、训练镜像、数据范围和入口适配方式；检查的是本次提交预览，而非旧任务截图。'],
    success: ['普通 DataLoader 与 Ray Data 两类分片规则各自满足，输出持久化，单卡验证通过；streaming 另核对 READY、schema、场地元数据、镜像协议与有界缓存预算。'],
    troubleshooting: ['预检通过只说明已检查的配置合法，不承诺显存、吞吐或选定场地样本已经验证；启动期 manifest 检查失败按原错误修复，不跳过护栏。'],
    relatedLinks: [link('提交预览', '/job/create'), link('版本信息', '/datasets')],
  },
  uploads: {
    prerequisites: ['进入「我的数据 → 我的文件」并选择可写目标目录；确认个人空间容量与文件完整。公共/团队只读空间不可上传。'],
    success: ['等待分片上传及服务端完成确认，页面提示成功后刷新目录；核对文件名和大小，再用于训练。100% 传输进度不等于文件已经提交完成。'],
    troubleshooting: ['断网自动重试后仍失败，保持原文件不变，在同一目标重新选择同一文件继续；会重新校验分片，匹配的已传片不重复上传。会话过期或原文件改变时可能需要重新传输。', '超过 5 GB 可以分片上传，不要求手工拆文件；没有业务侧固定单文件上限不等于无限物理容量。配额、存储容量、浏览器和平台可表示大小仍是边界。'],
    relatedLinks: [link('上传文件', '/datacache'), link('账户与登录', '/account-security')],
  },
}

export const uploadTopic = {
  id: 'uploads', group: '数据', title: '大文件上传与恢复',
  summary: '权重 .pth、.pt 等通过平台分片上传，不需要客户端直接访问 TOS；文件大时保持页面和网络可用，观察进度即可。',
  blocks: [{ kind: 'steps', items: [
    { title: '选择目录和文件', body: '在可写的「我的文件」进入目标目录，点击「上传文件」。不要用上传代码 ZIP 入口传训练权重。' },
    { title: '等待校验和分片', body: '当前分片大小为 32 MiB，客户端逐片计算 SHA-256、显示进度并在可重试错误时自动重试；校验阶段也需要时间，不会一次把整个文件读进内存。' },
    { title: '中断后恢复', body: '网络恢复、重新登录后，重新选择同一文件到原目标。页面不会在关闭后继续后台上传；恢复需要再次选择本地文件并校验，不能只凭同名假定内容一致。' },
    { title: '确认可用', body: '等到成功提示再刷新目录核对大小；多文件失败时仅重试失败文件，已成功文件不重复上传。训练读取前确认目标文件已经完成。' },
  ] }],
}
