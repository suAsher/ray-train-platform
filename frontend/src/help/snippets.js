// Copyable snippets shown on the help page. They live apart from the topic
// text so a long command does not bury the explanation around it.

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

export const SUBMIT_SMOKE = `# 单卡冒烟：先确认链路通了
spk-rayjob submit --watch \\
  --name smoke --entrypoint "python3 train.py" \\
  --input-space public --input-path bevfusion/2026-08-0429`

export const SUBMIT_MULTINODE = `# 多机多卡：workers≥2，平台在每个 Worker 内启动 torchrun
spk-rayjob submit --watch \\
  --engine ray-train \\
  --entrypoint "python3 tools/train.py configs/lidar.yaml --launcher pytorch" \\
  --input-space public --input-path bevfusion/2026-08-0429 \\
  --max-failures 2 --checkpoint-every-epochs 1`

export const SUBMIT_RESUME = `# 续训：把上一次运行的结果目录作为只读 checkpoint 传入
spk-rayjob submit --watch --resume-from-job <上一次的 JOB ID>`

export const NATIVE_RAY_SUBMIT = `export RAY_ADDRESS="https://<平台地址>/ray"
export RAY_JOB_HEADERS='{"Authorization":"Bearer <平台 PAT>"}'

# 不带 ray-platform 元数据时默认 1 卡；要多卡必须把这组元数据写全
ray job submit --address "$RAY_ADDRESS" --working-dir . \\
  --metadata-json '{
    "ray-platform.image": "harbor.wellspiking.ai/<项目>/<镜像>@sha256:<digest>",
    "ray-platform.worker-replicas": "2",
    "ray-platform.gpus-per-worker": "8",
    "ray-platform.cpu-per-worker": "32",
    "ray-platform.memory-per-worker": "128Gi"
  }' \\
  -- python3 tools/train.py configs/lidar.yaml --launcher pytorch`

export const SUBMIT_CACHE = `# 预热输入到本地双 NVMe，训练代码一行都不用改
spk-rayjob submit --watch \\
  --cache-mode runtime --cache-size 1Ti --cache-preload input \\
  --input-space public --input-path bevfusion/2026-08-0429 \\
  --entrypoint "python3 tools/train.py configs/lidar.yaml --launcher pytorch"`

export const SUBMIT_RAY_DATA_STAGE = `# Ray Data 分布式预热成本地视图，之后仍由原 DataLoader 读取
spk-rayjob submit --watch \\
  --engine ray-train --data-mode ray-data-stage \\
  --cache-mode runtime --cache-size 1Ti \\
  --input-space public --input-path bevfusion/2026-08-0429 \\
  --entrypoint "python3 tools/train.py configs/lidar.yaml --launcher pytorch"`

export const SUBMIT_RAY_DATA = `# 由 Ray Data 直接给每个 Train Worker 分片；训练代码必须自己消费
spk-rayjob submit --watch \\
  --engine ray-train --data-mode ray-data \\
  --ray-data-format images --ray-data-path images/train \\
  --entrypoint "python3 tools/train_raydata.py"`

export const RAY_DATA_CODE = `# 用 ray-data 时，分片由 Ray Train 提供，不要再自己按 rank 切分
import ray.train

shard = ray.train.get_dataset_shard("train")

for batch in shard.iter_torch_batches(batch_size=8):
    images = batch["image"]
    # ... 正常的前向/反向

# 注意：DistributedSampler 与手工 rank 分片必须去掉，
# 否则会和 Ray Data 的分片叠加，导致每张卡少看数据。`

export const SUBMIT_STREAMING = `# 固定不可变数据版本，按需流式读取
spk-rayjob submit --watch \\
  --engine ray-train --data-mode streaming \\
  --dataset <数据集>:<版本> --dataset-cache-policy bounded \\
  --entrypoint "python3 tools/train_raydata.py"`

export const RESUME_CODE = `# checkpoint 必须写进 PLATFORM_OUTPUT_PATH，续训时从 PLATFORM_CHECKPOINT_PATH 读
import os
from pathlib import Path

output = Path(os.environ["PLATFORM_OUTPUT_PATH"])
resume_root = os.environ.get("PLATFORM_CHECKPOINT_PATH", "")

# 保存（只让 rank 0 写）
if int(os.environ.get("RANK", "0")) == 0:
    (output / "checkpoints").mkdir(parents=True, exist_ok=True)
    torch.save(state, output / "checkpoints" / "latest.pth")

# 恢复
if resume_root:
    state = torch.load(Path(resume_root) / "checkpoints" / "latest.pth")
    model.load_state_dict(state["model"])
    start_epoch = state["epoch"] + 1`

export const MLFLOW_CODE = `# 只让 rank 0 记录，否则每张卡都会写一份
import os, mlflow

if int(os.environ.get("RANK", "0")) == 0:
    mlflow.log_params({"lr": lr, "batch_size": batch_size})
    mlflow.log_metric("loss", float(loss), step=global_step)`

export const DEBUG_SELFCHECK = `# 在调试环境的终端里跑一遍，确认卡和数据都在
nvidia-smi

python3 - <<'PY'
import torch, os
print(torch.__version__, torch.cuda.is_available(), torch.cuda.device_count())
PY

ls -la /mnt/storage/public     # 公共数据
ls -la /mnt/storage/me         # 你自己的空间`

export const DEBUG_DEPS = `# 临时装依赖：装进工作区，重启后仍在
pip install --user <包名>
# 或者建一个虚拟环境
python3 -m venv /workspace/.venv && source /workspace/.venv/bin/activate

# 容器以非 root 运行，apt install 不可用。
# 需要系统库、CUDA 或编译器时，请让管理员登记新镜像。`

export const THROUGHPUT_FORMULA = `全局 Batch   = Worker 数 × 每 Worker GPU 数 × samples_per_gpu
样本吞吐     = 全局 Batch ÷ 平均单步耗时
加速比       = 多机样本吞吐 ÷ 单机样本吞吐
扩展效率     = 加速比 ÷ GPU 数量倍数
数据等待占比 = 平均 data_time ÷ 平均单步耗时`

export const SCALING_AB = `# 目的一：验证扩卡是否有效（每卡 Batch 不变）
1×8 卡   samples_per_gpu=8   全局 Batch 64
2×8 卡   samples_per_gpu=8   全局 Batch 128
# 预期：单步耗时接近，样本吞吐≈2 倍，每轮迭代数≈减半

# 目的二：比较相同优化语义下的训练时间（全局 Batch 固定）
1×8 卡   samples_per_gpu=8   全局 Batch 64
2×8 卡   samples_per_gpu=4   全局 Batch 64
# 预期：单步耗时下降，但达不到严格 2 倍——多了跨节点 AllReduce，
#       每张卡的计算粒度也变小了

# 两种测法结论不能混用。`
