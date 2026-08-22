// Production job sizes mirror the currently labelled GPU pool: two RTX 4090
// servers with eight GPUs each. Keep this user-facing list explicit; adding a
// node is an administrator change to the cluster profile, not a silent UI
// promise of unavailable capacity.
export const productionTrainingProfiles = Object.freeze([
  Object.freeze({ name: '单卡', description: '先验证代码、数据和日志；命令在一张 GPU 上执行。', workers: 1, gpus: 1, cpu: 8, memory: '32Gi', executionMode: 'single_gpu' }),
  Object.freeze({ name: '单机多卡 DDP', description: '一个 8 卡 worker Pod 内执行 torchrun，适合现有 PyTorch DDP 代码。', workers: 1, gpus: 8, cpu: 32, memory: '128Gi', executionMode: 'torchrun' }),
  Object.freeze({ name: '多机多卡 Ray Train', description: '两个 8 卡 worker Pod 跨节点执行 Ray 管理的 torchrun DDP。', workers: 2, gpus: 8, cpu: 32, memory: '128Gi', executionMode: 'ray_train' }),
])
