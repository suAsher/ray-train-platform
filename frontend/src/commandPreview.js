/**
 * Show the command the platform will actually run.
 *
 * A user types `python tools/train.py`; the platform's in-image launcher wraps
 * it in torchrun and places it on the reserved GPUs. That wrapping used to be
 * invisible, so people either wrote torchrun themselves (double wrapping) or
 * could not explain why their DDP code saw one rank.
 *
 * This mirrors images/workspace/raytrain-launch.py. Keep the two in step.
 */

import { parseEntrypoint } from './submission.js'
import { resolveExecutionMode } from './platformLimits.js'

export function previewCommand(entrypoint, workers, gpus) {
  const command = parseEntrypoint(entrypoint)
  if (command.length === 0) return ''
  const mode = resolveExecutionMode(workers, gpus)
  const gpuCount = Math.max(1, Number(gpus) || 1)
  if (mode === 'single_gpu') return command.join(' ')
  if (mode === 'torchrun') {
    return torchrunCommand(['--standalone', `--nproc_per_node=${gpuCount}`], command)
  }
  const workerCount = Math.max(2, Number(workers) || 2)
  return torchrunCommand(
    [`--nnodes=${workerCount}`, `--nproc_per_node=${gpuCount}`, '--node_rank=<每个节点 0..' + (workerCount - 1) + '>', '--master_addr=<Ray 分配>', '--master_port=<Ray 分配>'],
    command,
  )
}

/**
 * torchrun treats its first positional argument as a Python script. Most users
 * type `python train.py`, which would make torchrun look for a file literally
 * named `python`, so --no-python is added unless the command starts with a
 * script. This matches torchrun_command() in raytrain-launch.py.
 */
function torchrunCommand(options, command) {
  const [first, ...rest] = command
  // `python train.py ...` becomes torchrun's own Python-script mode.
  if ((first === 'python' || first === 'python3') && rest[0]?.endsWith('.py')) {
    return ['torchrun', ...options, ...rest].join(' ')
  }
  if (first?.endsWith('.py')) {
    return ['torchrun', ...options, ...command].join(' ')
  }
  return ['torchrun', ...options, '--no-python', ...command].join(' ')
}

const shellOperators = ['&&', '||', ';', '|', '>', '<']

/**
 * entrypointWarnings catches, at form time, the three mistakes that otherwise
 * fail minutes later inside a GPU Pod with an unhelpful message.
 */
export function entrypointWarnings(entrypoint, workers, gpus) {
  const text = String(entrypoint || '').trim()
  if (!text) return []
  const warnings = []
  const mode = resolveExecutionMode(workers, gpus)
  if (/(^|\s)torchrun(\s|$)/.test(text) || /torch\.distributed\.launch/.test(text)) {
    warnings.push(
      mode === 'single_gpu'
        ? '启动命令里不要自己写 torchrun：单卡模式会直接执行你的命令。'
        : '启动命令里不要自己写 torchrun：平台已经按所选 GPU 数自动加上 torchrun，重复包装会导致 rendezvous 失败。',
    )
    return warnings
  }
  if (/torchpack\s+dist-run/.test(text)) {
    warnings.push('torchpack dist-run 使用 MPI 启动，与平台的 torchrun 不兼容。请改用 tools/westwell_train.py --launcher pytorch。')
    return warnings
  }
  if (shellOperators.some((operator) => text.includes(operator))) {
    warnings.push('启动命令只能是一条命令，不能包含 && 、|| 、; 或管道。工作目录已经是 /workspace，无需先 cd。')
  }
  return warnings
}
