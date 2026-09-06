// Native Ray metadata values must be strings, including the nested sites JSON.
export const nativeStreamingMetadata = {
  'ray-platform.image': 'registry.example.com/team/training-env:v1',
  'ray-platform.worker-replicas': '1',
  'ray-platform.gpus-per-worker': '1',
  'ray-platform.cpu-per-worker': '8',
  'ray-platform.memory-per-worker': '32Gi',
  'ray-platform.queue': 'REPLACE_WITH_TEAM_QUEUE',
  'platform.training.engine': 'ray-train',
  'platform.dataset.ref': 'REPLACE_WITH_DATASET_ID',
  'platform.dataset.version': 'REPLACE_WITH_READY_VERSION',
  'platform.dataset.cache-policy': 'bounded',
  'platform.dataset.sites': JSON.stringify(['REPLACE_WITH_SITE_CODE']),
}

export const commandRecipes = {
  id: 'command-recipes', group: '训练', title: '命令提交示例：spk-rayjob 与原生 Ray',
  summary: '按命令完成查环境、提交、选场地、看日志和续训。所有示例从 1 Worker × 1 GPU 起步；先替换镜像、目录、数据版本和队列，不直接复制占位值提交。',
  prerequisites: [
    '先按「外部提交」安装并登录。spk-rayjob images 需要包含此功能的新版 CLI；旧版出现 unknown command 时重新执行安装命令。以下多行示例适用于 Bash/zsh；Windows 基础提交使用外部提交页的 PowerShell 示例。',
    '在代码根目录运行，使用 .gitignore / .rayignore 排除数据、权重、环境构建目录和秘密。镜像必须已由管理员登记；支持 tag 与 digest，不限制仓库域名。CLI 不能替代管理员登记或配置私有仓库凭据。',
    '不要在已有 streaming 项目配置上直接套用 mount 示例：未指定字段会沿用 .spk-rayjob.yaml。先核对配置和数据模式，保持一次任务只使用一套数据来源。',
  ],
  blocks: [
    { kind: 'code', label: '1. 查看已登记环境与数据（只读）', lang: 'bash', text: `spk-rayjob login-check
spk-rayjob images
spk-rayjob images --output json
spk-rayjob datasets
spk-rayjob dataset versions REPLACE_WITH_DATASET_ID
# 环境描述是管理员声明；JSON 可用于保存版本与依赖信息。` },
    { kind: 'code', label: '2. 自建环境 + 普通文件输入（兼容 DDP 入口）', lang: 'bash', text: `spk-rayjob submit --watch \\
  --name custom-env-smoke \\
  --image 'registry.example.com/team/training-env:v1' \\
  --engine ray-ddp --execution-mode single_gpu --data-mode mount \\
  --workers 1 --gpus-per-worker 1 --cpu-per-worker 8 --memory-per-worker 32Gi \\
  --input-space public --input-path 'REPLACE_WITH_READABLE_SUBDIRECTORY' \\
  --output-path 'custom-env-smoke' \\
  --entrypoint 'python train.py'
# train.py 从 PLATFORM_DATASET_PATH 读取，只向 PLATFORM_OUTPUT_PATH 写入。` },
    { kind: 'code', label: '3. Ray Train + 版本化数据 + 场地筛选', lang: 'bash', text: `spk-rayjob submit --watch \\
  --name site-streaming-smoke \\
  --image 'registry.example.com/team/managed-env:v1' \\
  --engine ray-train --data-mode streaming \\
  --workers 1 --gpus-per-worker 1 --cpu-per-worker 8 --memory-per-worker 32Gi \\
  --dataset 'REPLACE_DATASET:REPLACE_READY_VERSION' \\
  --dataset-sites 'REPLACE_SITE_1,REPLACE_SITE_2' \\
  --dataset-cache-policy bounded \\
  --max-failures 2 --checkpoint-every-epochs 1 \\
  --entrypoint 'python tools/train_managed.py'
# tools/train_managed.py 必须已接入平台训练和 streaming 适配器。
# 全量训练：无项目场地默认值时删掉 --dataset-sites；
# 若 YAML 已有场地值，用 --dataset-sites '' 显式清空。` },
    { kind: 'code', label: '4. 状态、日志与手工续训', lang: 'bash', text: `# JOB_ID 用平台任务 ID；替换占位值，不加尖括号。
spk-rayjob status JOB_ID
spk-rayjob logs -f JOB_ID
# 在原代码目录、原镜像与数据配置下，接入 checkpoint 的入口才能恢复。
spk-rayjob submit --resume-from-job JOB_ID --watch
# 只有确实要停止自己的任务时，才执行 spk-rayjob cancel JOB_ID。` },
    { kind: 'note', text: '原生 Ray 可以通过 --metadata-json 选择自建镜像和资源；一旦填写任意 ray-platform.* 资源字段，就必须同时填写 image、worker-replicas、gpus-per-worker、cpu-per-worker、memory-per-worker、queue 六项。队列名请管理员提供，不是团队显示名。普通提交默认 ray-ddp；托管训练显式设置 platform.training.engine。' },
    { kind: 'code', label: '5. 原生 Ray 托管场地流式提交（完整 Bash/zsh 示例）', lang: 'bash', text: `# 已按外部提交页安装兼容 Ray CLI。把下面所有 REPLACE 值和镜像换成实际值。
(
  set -e
  RAY_ADDRESS='https://REPLACE_PLATFORM_HOST/ray'
  printf '平台 PAT: '
  read -rs SPK_TOKEN
  printf '\\n'
  RAY_JOB_HEADERS=$(printf '%s' "$SPK_TOKEN" | python3 -c 'import json,sys; print(json.dumps({"Authorization":"Bearer " + sys.stdin.read()}))')
  export RAY_JOB_HEADERS
  unset SPK_TOKEN
  ray job submit --address "$RAY_ADDRESS" --working-dir . \\
    --metadata-json '${JSON.stringify(nativeStreamingMetadata, null, 2)}' \\
    -- python tools/train_managed.py
)
# sites 是 JSON 数组编码后的字符串；全量可写 "[]"，不要传目录。
# 这里不能同时设置 platform.data.input-* 或 platform.cache.*。` },
    { kind: 'code', label: '6. 原生 Ray 后续操作（另开命令时重新设置认证）', lang: 'bash', text: `(
  set -e
  RAY_ADDRESS='https://REPLACE_PLATFORM_HOST/ray'
  printf '平台 PAT: '
  read -rs SPK_TOKEN
  printf '\\n'
  RAY_JOB_HEADERS=$(printf '%s' "$SPK_TOKEN" | python3 -c 'import json,sys; print(json.dumps({"Authorization":"Bearer " + sys.stdin.read()}))')
  export RAY_JOB_HEADERS
  unset SPK_TOKEN
  ray job status --address "$RAY_ADDRESS" RAY_SUBMISSION_ID
  ray job logs --address "$RAY_ADDRESS" --follow RAY_SUBMISSION_ID
  # 仅确实要停止自己的任务时取消下一行注释：
  # ray job stop --address "$RAY_ADDRESS" RAY_SUBMISSION_ID
)
# RAY_SUBMISSION_ID 使用 ray job submit 输出的 submission ID；
# spk-rayjob status 使用平台任务 ID，两个 ID 不应混用。` },
    { kind: 'table', headers: ['场景', '推荐命令 / 当前边界'], rows: [
      ['镜像环境清单', 'spk-rayjob images；安装新 CLI 才有此子命令。'],
      ['原生 Ray 普通文件输入', 'platform.data.input-space 与 platform.data.input-path 配对；不要与 platform.dataset.* 混用。'],
      ['缓存预热', 'spk-rayjob --cache-mode runtime --cache-size 1Ti --cache-preload input；输入必须放得进缓存。原生对应 platform.cache.mode/size/preload。'],
      ['ray-data / ray-data-stage', '优先 spk-rayjob 的 --data-mode；原生入口不接受任意 data-mode 键，不能自行编造元数据。'],
      ['续训与恢复参数', '优先 spk-rayjob --resume-from-job 和 --max-failures；原生托管恢复策略目前使用平台固定默认值，不支持任意 platform.training.* 参数。'],
    ] },
  ],
  success: ['提交返回 ID 后任务按预期镜像、规模和数据范围运行；日志、样本数和持久化产物符合预期。Streaming 全量多卡验收仍未完成，命令解析成功不代表真实吞吐或恢复已验证。'],
  troubleshooting: ['unknown command images：升级 CLI。镜像未登记/引擎不兼容：先查 images 输出并联系管理员，不通过改 tag 绕过登记。', '原生 metadata 缺字段：补齐六个资源键；所有值必须是字符串。队列或空间无权访问：核对当前账号授权。', '401 重新登录或获取有效 PAT；不要打印 RAY_JOB_HEADERS。场地不存在、缺 site_id 或镜像协议不兼容：按明确错误修复，不退回全量继续训练。'],
  relatedLinks: [{ label: '安装与三端命令', to: '/external-submit' }, { label: '训练配置', to: '/job/create' }, { label: '账号与 PAT', to: '/account-security' }],
}
