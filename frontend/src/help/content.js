// The platform's usage knowledge lives in docs/ as 8000+ lines across twenty
// manuals. A user filling in the submit form cannot read that, so the parts
// that decide whether a job succeeds are restated here and rendered in-app.
//
// This is the single source for both the help page and the file the download
// button produces, so what a user reads on screen and what they take away
// cannot drift apart.
//
// Every entry below is here because it caused a real failure, not because it
// was available to copy from a manual.

export const CONTRACT_SNIPPET = `import os
from pathlib import Path

dataset = Path(os.environ["PLATFORM_DATASET_PATH"])
output = Path(os.environ["PLATFORM_OUTPUT_PATH"])
checkpoint = os.environ.get("PLATFORM_CHECKPOINT_PATH", "")

output.mkdir(parents=True, exist_ok=True)
# 读 dataset；模型、checkpoint、评估结果只写 output。`

export const SMOKE_SCRIPT = `# train.py —— 最小可跑通示例，用来确认平台链路正常
import os
from pathlib import Path

dataset = Path(os.environ["PLATFORM_DATASET_PATH"])
output = Path(os.environ["PLATFORM_OUTPUT_PATH"])
output.mkdir(parents=True, exist_ok=True)

# flush=True：日志实时进入平台日志流，不然可能看不到
print(f"输入目录 {dataset}", flush=True)
print(f"前 5 个条目 {[p.name for p in list(dataset.iterdir())[:5]]}", flush=True)

import torch
print(f"CUDA {torch.cuda.is_available()}，可见卡数 {torch.cuda.device_count()}", flush=True)

(output / "hello.txt").write_text("产物写入成功\\n", encoding="utf-8")
print("已写入产物，可在「训练产物」标签页看到 hello.txt", flush=True)`

export const helpSections = [
  {
    id: 'quickstart',
    title: '第一次跑通',
    summary: '先用下面这个最小示例确认平台链路是通的：环境变量能解析、输入目录读得到、GPU 可见、产物写得出去。这四件事是后面所有失败的根源，几分钟就能排除掉。确认之后再接你自己的代码。',
    blocks: [
      {
        kind: 'steps',
        items: [
          {
            title: '把这个脚本放进你的代码目录',
            body: '它不训练任何东西，只验证平台和代码之间的四个接口。',
            code: SMOKE_SCRIPT,
            codeLabel: 'train.py',
            codeLang: 'python',
          },
          {
            title: '在提交表单里填这条启动命令',
            body: '先选 1 卡。多卡由平台启动 torchrun，你不需要也不应该自己写。',
            code: 'python3 train.py',
            codeLabel: '训练启动命令',
            codeLang: 'bash',
          },
          {
            title: '选输入数据和运行环境',
            body: '输入目录先在「我的数据」里逐级点开确认真实存在——路径写错是最常见的失败，任务通常在两分钟内挂掉。运行环境选一个已登记的镜像即可。',
          },
          {
            title: '提交后看这三处',
            body: '日志里应出现输入目录和 CUDA 信息；任务详情的「训练产物」标签页应出现 hello.txt。三处都对，说明链路通了，可以换成你自己的训练脚本。',
          },
        ],
      },
    ],
  },
  {
    id: 'contract',
    title: '训练代码的对接契约',
    summary: '容器里唯一稳定的东西是这几个环境变量。不要写死 TOS 地址、桶名、PVC 名或节点路径——它们在不同任务中会解析到不同位置。',
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
        kind: 'warning',
        title: '在 Python 里读，不要靠启动命令里的 shell 展开',
        text: '启动命令中的 $PLATFORM_OUTPUT_PATH 有可能在提交侧就被求值，展开成空字符串，训练随后会向类似 /run_dir 的根路径写入并报 PermissionError。用 os.environ 在训练进程内读取则不受影响。',
      },
    ],
  },
  {
    id: 'entrypoint',
    title: '启动命令的三条约定',
    summary: '',
    blocks: [
      {
        kind: 'list',
        items: [
          '不要自己写 torchrun 或 torchpack。单机多卡与多机多卡都由平台启动 torchrun，你只写 python tools/train.py ...；自己再套一层会导致进程数翻倍或 rank 冲突。',
          '用 python3。部分镜像没有 python 这个名字，写 python 会得到 python: not found。',
          '只能是一条命令。需要传含空格的参数时用引号包起来；命令在工作目录下执行。',
        ],
      },
      { kind: 'code', label: '示例', lang: 'bash', text: 'python3 tools/train.py configs/lidar.yaml --launcher pytorch' },
    ],
  },
  {
    id: 'data-mode',
    title: '五种数据模式怎么选',
    summary: '默认用 mount。只有当你确认瓶颈在数据读取上时，才需要换其他模式——换错了不会更快，只会多一层复杂度。',
    blocks: [
      {
        kind: 'table',
        headers: ['模式', '什么时候用它'],
        rows: [
          ['mount', '默认。直接读已授权的数据挂载，不做任何缓存。先用它跑通，再谈优化。'],
          ['cache', '同一份数据要被反复读很多轮，且单个文件不算小。启动时预热到本地 NVMe，之后从本地读。'],
          ['ray-data-stage', '数据是海量小文件，读取延迟是瓶颈。由 Ray Data 分布式读取并在本地生成视图。'],
          ['ray-data', '你的训练代码自己用 Ray Data 接收分片（Parquet／图片），而不是用普通 DataLoader。'],
          ['streaming', '要固定一个不可变的数据集版本，按需流式读取，保证多次训练读到完全相同的数据。'],
        ],
      },
      {
        kind: 'note',
        text: '不确定就选 mount。先测出数据读取确实是瓶颈（比如训练日志里 data_time 明显高于 0），再考虑换模式。',
      },
    ],
  },
  {
    id: 'errors',
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
          ['任务一直排队', 'GPU 配额或空闲卡不足', '看任务状态原因；检查是否有没停掉的调试环境占着卡'],
          ['日志看不到末尾 loss', '模型结构日志过长把后面顶掉了', '用日志流跟随，或调大返回条数'],
        ],
      },
    ],
  },
  {
    id: 'preflight',
    title: '提交前自检',
    summary: '八卡任务因为一个路径写错而在两分钟内失败，是这套平台上最常见的浪费。',
    blocks: [
      {
        kind: 'checklist',
        items: [
          '输入目录已在「我的数据」里点开确认存在，不是凭记忆写的路径。',
          '代码读 PLATFORM_DATASET_PATH、写 PLATFORM_OUTPUT_PATH，没有写死任何绝对路径。',
          '先用 1 卡最小批量跑通至少几个 step，再扩到多卡多机。',
          '所有 rank 都用 DistributedSampler，只有 rank 0 写 checkpoint。',
          '启动命令里没有 torchrun。',
          '调试环境用完已经停掉，不要空占 GPU。',
        ],
      },
    ],
  },
]

// The download is generated from the same sections the page renders, so the
// file a user keeps on their laptop matches what they just read.
export function renderHelpMarkdown(sections = helpSections) {
  const lines = ['# RayTrain 平台使用说明', '']
  for (const section of sections) {
    lines.push(`## ${section.title}`, '')
    if (section.summary) lines.push(section.summary, '')
    for (const block of section.blocks) {
      lines.push(...renderBlock(block))
    }
  }
  return lines.join('\n').replace(/\n{3,}/g, '\n\n').trimEnd() + '\n'
}

function renderBlock(block) {
  switch (block.kind) {
    case 'steps':
      return [
        ...block.items.flatMap((item, index) => {
          const head = [`${index + 1}. **${item.title}** — ${item.body}`, '']
          if (!item.code) return head
          return [...head, '```' + (item.codeLang || ''), item.code, '```', '']
        }),
      ]
    case 'list':
      return [...block.items.map((item) => `- ${item}`), '']
    case 'checklist':
      return [...block.items.map((item) => `- [ ] ${item}`), '']
    case 'table':
      return [
        `| ${block.headers.join(' | ')} |`,
        `| ${block.headers.map(() => '---').join(' | ')} |`,
        ...block.rows.map((row) => `| ${row.join(' | ')} |`),
        '',
      ]
    case 'code':
      return [block.label ? `**${block.label}**` : '', '', '```' + (block.lang || ''), block.text, '```', '']
    case 'warning':
      return [`> **${block.title}**`, '>', `> ${block.text}`, '']
    case 'note':
      return [`> ${block.text}`, '']
    default:
      return []
  }
}
