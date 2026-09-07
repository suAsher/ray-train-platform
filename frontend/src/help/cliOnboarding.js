import { externalSubmitCommands } from './externalSubmit.js'
import { commandRecipes, nativeStreamingMetadata } from './commandRecipes.js'

const origin = 'https://raytrain.wellspiking.ai'
const installs = ['linux', 'macos', 'windows'].flatMap(platform => {
  const commands = externalSubmitCommands(origin, platform)
  const label = { linux: 'Linux x86_64 / Bash', macos: 'macOS Apple Silicon / zsh', windows: 'Windows x64 / PowerShell' }[platform]
  return [
    { kind: 'code', label: `${label}：安装 spk-rayjob（先核对平台域名）`, lang: platform === 'windows' ? 'powershell' : 'bash', text: commands.install },
    { kind: 'code', label: `${label}：登录`, lang: platform === 'windows' ? 'powershell' : 'bash', text: commands.login },
    { kind: 'code', label: `${label}：原生 Ray 安装、PAT 与自定义镜像提交`, lang: platform === 'windows' ? 'powershell' : 'bash', text: commands.nativeCustomImage },
  ]
})

export const cliOnboarding = {
  id: 'cli-onboarding-v2', group: '入门', title: '完整 CLI 上手与升级：spk-rayjob / 原生 Ray',
  summary: '从安装登录到查镜像、资源、队列、数据版本、场地、日志、取消与续训。按自己的系统复制命令并替换占位值，不需要一键生成配置。',
  prerequisites: [
    '以下域名为生产平台示例；私有部署请替换为自己的平台 HTTPS 域名。先进入真实代码目录，确认 train.py 或训练适配器入口存在。',
    '本机原生 Ray CLI 2.35.0 是 Jobs API 兼容客户端；集群内 ray-train 训练镜像使用 Ray 2.58 系列。两者是不同组件，不要为了客户端兼容把训练镜像降级。',
    'macOS 示例只提供 Apple Silicon，Linux 示例只提供 x86_64，Windows 示例只提供 x64；Intel Mac / Linux ARM 不要运行不匹配的二进制。Bash 多行用反斜杠，PowerShell 用单行或反引号；不要直接混用。',
    'spk-rayjob 使用账号密码或 PAT 登录；原生 Ray 使用账户与安全页创建的 PAT。不要把密码、PAT、RAY_JOB_HEADERS 写进仓库或日志。',
  ],
  blocks: [
    { kind: 'warning', title: 'Windows 自更新发布边界', text: '本次仅开放 Linux/macOS 的 spk-rayjob upgrade。Windows 二进制可正常手动下载使用，但退出后自动替换尚未完成实机验收，upgrade 会提示从集群外提交页重新安装，不会修改当前程序。' },
    { kind: 'note', text: '平台页面更新不等于本机 CLI 已更新。旧客户端第一次没有 upgrade 子命令，必须重新执行下面的安装命令；安装支持 upgrade 的版本以后，手工执行 spk-rayjob upgrade。login / submit 会向 stderr 提示可用版本，但不会自动覆盖程序；未配置 minimumVersion 表示没有强制最低版本要求，也不要求每次平台发布都升级 CLI。' },
    { kind: 'code', label: '已有新版客户端：检查并升级', lang: 'bash', text: 'spk-rayjob version\nspk-rayjob login-check\nspk-rayjob upgrade\nspk-rayjob version' },
    { kind: 'note', text: 'upgrade 从已登录的平台 server 获取发布信息，并校验同域下载的 SHA256 清单、实际可执行格式和架构。校验失败或无写权限时不关闭校验。Unix 保留 .previous.* 备份后原子替换；Windows 退出后替换，需重新运行 version 确认。Windows 已有 .previous 时，先确认当前版本可用，将备份移到安全位置再升级；异步失败查看程序旁的 .upgrade-error.txt。平台版本、CLI 版本、镜像摘要分别记录。' },
    ...installs,
    { kind: 'note', text: 'spk-rayjob 的队列由平台根据当前团队授权决定，没有 --queue 参数；原生 Ray 自定义资源必须填写 ray-platform.queue，队列名称向团队管理员确认。自定义镜像需先登记；spk-rayjob images 只列可见镜像，不会登记镜像。资源数量受团队配额及任务形状限制。' },
    ...commandRecipes.blocks.map(block => block.kind === 'code'
      ? { ...block, text: block.text.replace('spk-rayjob submit --resume-from-job JOB_ID --watch', 'spk-rayjob submit --engine ray-train --resume-from-job JOB_ID --watch') }
      : block),
    { kind: 'code', label: 'PowerShell：spk-rayjob 完整托管数据版本提交', lang: 'powershell', text: `# 先完成上面的安装登录；在代码根目录执行并替换所有 REPLACE 值
spk-rayjob images
spk-rayjob datasets
spk-rayjob dataset versions REPLACE_DATASET
spk-rayjob submit --name site-smoke --image 'REPLACE_REGISTERED_IMAGE' --engine ray-train --workers 1 --gpus-per-worker 1 --cpu-per-worker 8 --memory-per-worker 32Gi --data-mode streaming --dataset 'REPLACE_DATASET:REPLACE_VERSION' --dataset-sites 'REPLACE_SITE' --dataset-cache-policy bounded --entrypoint 'python tools/train_managed.py' --watch
spk-rayjob status JOB_ID
spk-rayjob logs -f JOB_ID
# 确实要取消自己的任务时运行：
# spk-rayjob cancel JOB_ID
# 续训保留原镜像、数据版本、场地和资源；入口必须支持托管 checkpoint：
spk-rayjob submit --image 'REPLACE_REGISTERED_IMAGE' --engine ray-train --workers 1 --gpus-per-worker 1 --cpu-per-worker 8 --memory-per-worker 32Gi --data-mode streaming --dataset 'REPLACE_DATASET:REPLACE_VERSION' --dataset-sites 'REPLACE_SITE' --dataset-cache-policy bounded --entrypoint 'python tools/train_managed.py' --resume-from-job JOB_ID --watch` },
    { kind: 'code', label: 'PowerShell：原生 Ray 数据版本、场地及后续操作', lang: 'powershell', text: `# 已安装兼容 Ray CLI；替换所有 REPLACE 值，在代码根目录执行。
& {
  $ErrorActionPreference = 'Stop'
  $spkSecret = Read-Host '平台 PAT' -AsSecureString
  $spkPtr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($spkSecret)
  try {
    $env:RAY_JOB_HEADERS = @{ Authorization = ('Bearer ' + [Runtime.InteropServices.Marshal]::PtrToStringBSTR($spkPtr)) } | ConvertTo-Json -Compress
    $spkMetadata = @{
${Object.entries(nativeStreamingMetadata).map(([key, value]) => `      '${key}' = '${value}'`).join('\n')}
    } | ConvertTo-Json -Compress
    ray job submit --address '${origin}/ray' --working-dir . --metadata-json $spkMetadata -- python tools/train_managed.py
    if ($LASTEXITCODE -ne 0) { throw '提交失败' }
    $spkJob = Read-Host '复制刚才返回的 submission ID'
    ray job status --address '${origin}/ray' $spkJob
    ray job logs --address '${origin}/ray' --follow $spkJob
    # 确实要取消自己的任务时运行：
    # ray job stop --address '${origin}/ray' $spkJob
  } finally { Remove-Item Env:RAY_JOB_HEADERS -ErrorAction SilentlyContinue; [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($spkPtr); $spkSecret.Dispose() }
}` },
    { kind: 'warning', title: '原生 Ray 的续训边界', text: '原生 Jobs API 没有平台的 --resume-from-job，也不接受任意 checkpoint / platform.training.* 元数据。托管任务续训请在任务详情确认平台 JOB_ID，然后用 spk-rayjob submit --engine ray-train --resume-from-job JOB_ID，并明确保留原镜像、入口、数据版本及场地。不要把 Ray submission ID 当成平台 JOB_ID。普通 ray-ddp 脚本须自行接入 checkpoint 读取，不支持该托管续训选项。' },
    { kind: 'note', text: '提交后 workers 固定；新增节点不会给已有任务自动增加 Worker。版本化数据和场地需按授权可见的已就绪版本填写，streaming 入口需有适配器。命令成功不代表多机全量吞吐和恢复已验收。' },
    { kind: 'steps', items: [
      { title: '管理员：新 GPU 节点先隔离', body: '先核对集群 context 和新节点名称，再 cordon。检查 Node Ready、驱动、device plugin、DCGM、GPU 数量、CNI、FSX/CSI、DNS 和受权前缀挂载。不要重启、迁移或删除现有训练任务。', code: 'kubectl cordon NODE_NAME\nkubectl get node NODE_NAME -o wide\nkubectl describe node NODE_NAME\nkubectl get pods -A -o wide --field-selector spec.nodeName=NODE_NAME', codeLang: 'bash' },
      { title: '检查节点 NFS 客户端', body: '在新节点宿主机检查 mount.nfs；Ubuntu 缺少该工具时安装 nfs-common。此项修复无需重启内核或 kubelet，安装后观察 kubelet 重试和实际卷挂载。仍需分别验收 FSX/TOS 与 IDC NFS 数据读取。', code: '# 在新节点宿主机执行\ncommand -v mount.nfs\n# 缺少工具时，以 root 执行\napt-get install --no-install-recommends nfs-common\ncommand -v mount.nfs', codeLang: 'bash' },
      { title: '验收真实双盘并生成审阅材料', body: '/data1 与 /data2 必须独立挂载，父目录必须由 root 拥有且不可 group/world 写入，准备各自 ray-cache 子目录及权限。脚本读取两套线上配置，保留全部旧节点，检查挂载、容量与写删探针；输出目录必须不存在，失败不产生可应用补丁。', code: 'bash ops/storage/nvme-cache/register-node.sh --node NODE_NAME --output-dir /tmp/nvme-NODE_NAME-review', codeLang: 'bash' },
      { title: '分别审阅和升级两个供应器', body: 'data1-values-patch.yaml 只用于 ray-cache-local-data1，data2-values-patch.yaml 只用于 ray-cache-local-data2；两套独立 release 分别备份并用 --reuse-values 加对应补丁 dry-run，逐行审阅后按发布流程升级。不得用旧双节点 Profile 覆盖现网，也不得合并双盘到同一 nodePathMap。并发配置变化后重新生成。脚本不会代替你升级 Helm、打标签或解除 cordon。' },
      { title: '最后解除调度隔离', body: '在保持 cordon 时设置经确认的生产标签，完成定向新节点的双盘挂载、写入、删除、回收与资源形状验收；所有检查通过后最后 uncordon，再做单卡和多机 smoke。旧节点 verify 成功不能替代新节点验收。', code: '# 只在全部验收通过后执行\nkubectl uncordon NODE_NAME', codeLang: 'bash' },
    ] },
    { kind: 'warning', title: '自动节点接入启用后的简流程', text: '仅当 ray-node-onboarding controller、训练 node selector / ResourceFlavor 与 cache-ready gate 都已生产启用并验收后，才使用自动简流程。先完成驱动、NFS、FSX/CSI、DNS、镜像拉取与 TOS/FSX/IDC NFS 读取检查，确认 /data1 与 /data2 是独立挂载且父目录 root:root、模式不宽于 0755，再设置 accelerator=nvidia-rtx-4090 和 platform.wellspiking.ai/gpu-pool=production。不要手工设置 platform.wellspiking.ai/cache-ready；节点需要非 cordon 才能让定向 PVC 探针调度，但平台 ready 前训练必须被 cache-ready gate 挡住。controller 自动准备、登记、验收和触发配额发现，超级管理员再分配团队配额；已有运行任务不会重启、迁移或自动扩容。未启用这些门禁时继续走人工注册流程。' },
    { kind: 'warning', title: '标签与调度成功的边界', text: '标签不代表存储就绪。controller、训练 selector / ResourceFlavor 与 cache-ready gate 未生产启用并验收前，仅挂载 /data1、/data2 并设置两个生产标签不会自动接入，仍需执行人工注册与验收。Pod 出现 Scheduled 只说明已经分配节点；FailedMount 是后续卷挂载失败，先按卷类型区分 IDC NFS 与 FSX。NFS 报 bad option 或 might need /sbin/mount.<type> helper 时先检查宿主机 mount.nfs。ErrImagePull / ImagePullBackOff 是镜像拉取失败；均不能当成 GPU 调度失败，也不能仅凭 Scheduled 宣布环境可用。' },
    { kind: 'note', text: '物理 GPU 池由自动配额逻辑重新测量；团队配额仍需管理员按需求手动调整。新节点只增加后续任务可用容量，固定 workers 的已有任务不会自动扩容。需要更多 Worker 时新建任务或从已完成托管 checkpoint 续训，不干预运行中的任务。' },
    { kind: 'table', headers: ['超级管理员：团队退役问题', '使用方法与结果'], rows: [
      ['从哪里操作？', '在团队管理选择目标团队的「退役团队」，先检查活动资源，核对阻断项，再输入完整团队 ID 确认。当前登录团队和受保护系统团队不能退役。'],
      ['为什么被阻断？', '活动任务、调试环境或其他未完成资源会阻断；无法确认集群状态也会阻断。处理或等待这些资源完成后重新检查，不通过强制删除训练绕过检查。'],
      ['历史和空间会怎样？', '保留历史任务、成员记录和存储，不释放空间；撤销令牌并禁止新的提交与写操作。退役不是物理删除，也不是容量清理。'],
      ['能恢复吗？', '当前页面不提供恢复操作，确认前核对团队身份和影响。退役后通过「显示已退役团队」查看只读审计记录。'],
    ] },
    { kind: 'note', title: '管理员：新增 GPU 节点', text: '先由集群管理员把机器加入 Kubernetes，并保持 kubectl cordon 状态；完成 Node、CNI、containerd、GPU 驱动、device plugin、DCGM、DNS、镜像拉取、FSX/NFS 和双 NVMe 挂载检查后，再用 ops/storage/nvme-cache/register-node.sh 生成 review-only 补丁。脚本会分别输出 data1-values-patch.yaml 与 data2-values-patch.yaml，并保留旧节点映射；确认 Helm diff 只包含预期本地缓存映射后，才用 --reuse-values 升级。最后再按资源池标签和 Kueue 配额确认是否 kubectl uncordon。新增节点不会影响活动训练任务，也不会自动扩大既有任务 worker 数。' },
    { kind: 'warning', title: '超级管理员：团队退役', text: '团队退役只禁止继续使用，不删除 Namespace、PVC、对象存储、历史任务、成员审计或数据集，也不终止活动训练。页面会先展示活动任务、工作区、上传、发布、镜像、存储等预检结果；存在活动资源或状态未知时不能退役。确认时必须输入团队 ID。退役后保留历史和数据、撤销 PAT 与本地会话、禁止新提交，本期不提供恢复入口。' },
  ],
  success: ['本机版本核对成功；提交返回的镜像、资源、数据范围符合预期，日志和持久化产物可读。'],
  troubleshooting: ['旧版 unknown command upgrade 时重新安装；校验不一致停止安装。401 重新登录或换有效 PAT；资源或场地错误按明确提示修复。'],
  relatedLinks: [{ label: '外部提交下载入口', to: '/external-submit' }, { label: '镜像与运行环境', to: '/help' }, { label: '账户与 PAT', to: '/account-security' }],
}
