# BEVFusion 从拉取代码到 2×8 卡训练：完整操作

本手册从一份全新的 BEVFusion checkout 开始，完成代码改造、数据预检、1 卡检查、2×8 卡训练、日志查看、Ray Dashboard、Checkpoint 和断点续训。

本文保留的是已经验证的 **Ray 编排 DDP**（`--engine ray-ddp`）流程。Ray Train 托管需要额外的 MMCV Hook 和 checkpoint/report 适配，见 [Ray Train 托管指南](RAY_TRAIN_MANAGED_GUIDE.md)。本文不声称目标集群已经升级到 KubeRay 1.6.2，也不声称 BEVFusion 托管引擎已完成生产验证。

最终训练形态为：

```text
当前代码目录
  └─ spk-rayjob 打包当前版本（不是重新构建镜像）
       └─ 平台任务记录 + Kueue 准入
            └─ KubeRay 自动创建任务专属 RayCluster
                 ├─ Ray Head（平台管理）
                 ├─ GPU Worker 0：8 GPU / 64 CPU / 256GiB
                 └─ GPU Worker 1：8 GPU / 64 CPU / 256GiB
                      └─ 16 个 PyTorch DDP rank 跨两台节点训练
```

代码与镜像是两条独立版本线：

- Python、YAML、配置和训练参数每次随 working directory 上传，修改后直接重新提交。
- 镜像固定 CUDA、PyTorch、Ray、系统库和已编译 CUDA/C++ 扩展。
- 只有 CUDA/PyTorch ABI、系统包、编译扩展或大型固定依赖发生变化时才构建新镜像。

---

## 1. 已验证基线

| 项目 | 当前交付值 |
| --- | --- |
| 平台地址 | `https://raytrain.wellspiking.ai` |
| Git 仓库 | `gitlab.qomolo.com/dl/bevfusion` |
| `bev_3dod` 验证提交 | `0c1dc9d` |
| `bev_3dod_s1h` 验证提交 | `7931cee` |
| 训练镜像 | `harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553` |
| 公共数据逻辑空间 | `public` |
| Pod 内公共根目录 | `/mnt/storage/public`，只读 |
| 训练任务所选输入 | `$PLATFORM_DATASET_PATH`，只读 |
| 当次任务输出 | `$PLATFORM_OUTPUT_PATH`，读写 |
| 续训的历史结果 | `$PLATFORM_CHECKPOINT_PATH`，只读、可选 |
| 分布式拓扑 | 2 Worker × 8 GPU，`worldSize=16`，`STRICT_SPREAD` |

全量 FZ 数据已经完成一次真实的 2×8 卡、1 epoch 验收：

- 训练样本 15,228 条，验证样本 1,620 条；
- 完成 3,585/3,585 个训练 iteration 和完整验证；
- 最新独立复跑任务 `job-20c0affe64d0a638f1f348c8` 为 `SUCCEEDED`，`mAP=0.6090`、`NDS=0.4909`；
- 两个 Worker 返回码均为 0；
- 生成 99,798,985 字节的 `epoch_1.pth`。

该结果证明平台、数据、跨节点 NCCL/DDP、日志和输出持久化链路可用；不同一次运行的数值可能受代码提交、随机性和配置影响，正式多 epoch 收敛、业务精度和超参数仍由算法负责人验收。

`bev_3dod_s1h` 已验证范围是 smoke-128：数据预检、2×8 DDP、日志和 checkpoint 链路已经通过，但当前全量数据包含 S1H 历史 smoke 未覆盖的 IGV 类别，且全量训练出现过类别头不匹配和 fp16 数值不稳定。因此本文第 10 节的全量模板只对 `bev_3dod` 构成已验收基线，不能据此宣称 S1H 全量模型已经收敛。

---

## 2. CPU、内存和 GPU 应该怎么填

`cpuPerWorker` 和 `memoryPerWorker` 表示**每个 Ray Worker Pod**的资源，不是整个任务的资源，也不是单个 GPU/rank 的资源。

例如：

```yaml
workers: 2
gpusPerWorker: 8
cpuPerWorker: 64
memoryPerWorker: 256Gi
```

实际申请：

```text
GPU：    2 × 8       = 16 GPU
CPU：    2 × 64      = 128 CPU
内存：   2 × 256GiB  = 512GiB
```

Ray Head、任务提交器和平台组件由平台单独管理，不包含在上述 Worker 合计中。

### 2.1 当前 GPU 节点的建议值

| 场景 | Worker 数 | 每 Worker GPU | 每 Worker CPU | 每 Worker 内存 | 说明 |
| --- | ---: | ---: | ---: | ---: | --- |
| 数据/环境预检 | 1 | 1 | 8 | 32GiB | 只检查 CUDA、依赖和数据路径。 |
| 小样本 smoke | 1 或 2 | 8 | 32 | 128GiB | 与已验证基线一致。 |
| 正式 2×8 卡默认 | 2 | 8 | **64** | **256GiB** | 当前节点规格的推荐起点。 |
| 数据加载较重 | 2 | 8 | 96 | 384GiB | 只有观测到 GPU 等数据时再提高。 |
| 单 Worker 建议上限 | 任意 | 8 | 144 | 600GiB | 必须给 kubelet、CSI、DCGM 和突发内存留余量。 |

不要直接按节点的物理标称 CPU/内存填满资源。Kubernetes 按节点 `allocatable` 而不是物理标称值调度，系统 Pod 也需要资源；填满节点会导致任务长时间 Pending，或在节点压力下被驱逐。

当前 BEVFusion 验收参数为 `data.workers_per_gpu=1`。这意味着 8 卡 Worker 的数据加载并发并不高，单纯把 CPU 从 64 提到 144 通常不会自动变快。建议按下面顺序调优：

1. 先用 `64 CPU / 256GiB` 跑基线；
2. 看 GPU 利用率、数据等待时间、CPU 和内存；
3. GPU 经常空闲且 CPU 未满时，把 `data.workers_per_gpu` 从 1 调到 2；
4. 再测 4，不要一次跳到很大；
5. 只有 DataLoader 确实吃满 CPU，才把 Worker 提到 96 CPU；
6. 每次只改变一个变量，记录 samples/s、iteration time 和 GPU 利用率。

新增 GPU 节点后仍保持“一个 8 卡 Worker 对应一台 8 卡节点”。例如 4 台节点做 4×8 时设置 `workers: 4`，每 Worker 的 CPU/内存不变，总资源按 Worker 数线性增长，并受团队 Kueue 配额限制。

### 2.2 配额、并发与排队

当前租户的团队配额是 16 GPU，因此一个 2×8 任务会占满团队 GPU 配额；同一团队第二个 2×8 任务会由 Kueue 排队，不会抢占正在运行的任务。1 卡探针和 2×8 正式任务也共享这 16 GPU 配额，提交长任务前应先确认没有遗留的 `RUNNING`、`PROVISIONING` 或已准入任务。

平台按实际加入集群且可调度的 GPU 动态计算物理容量，但团队管理员分配的配额不会随扩容自动增加。新增 GPU 节点后，仍需由超级管理员调整团队配额；否则新节点存在，团队任务也可能继续排队。

任务结束后，Kueue 准入资源会释放。RayCluster 和 Pod 的成功诊断保留窗口当前约 60 秒，失败诊断保留窗口约 10 分钟；历史任务记录、Loki 日志、MLflow 指标和个人结果目录不依赖这些 Pod 存活。不要用“Pod 还没消失”判断配额是否已经释放，应以 Portal 的配额、队列和任务状态为准。

---

## 3. 准备账号、Git 和客户端

在执行任何 `git clone` 之前，先确认四项前置条件全部满足：

- **平台账号**：能登录 Portal，并且 `spk-rayjob jobs` 能返回本人任务；
- **GitLab 访问权**：个人 SSH Key 或组织批准的 HTTPS 凭据能读取目标分支；
- **已批准镜像**：团队镜像目录中已登记本手册固定的不可变 digest；
- **团队 16 GPU 配额**：团队剩余配额至少为 16 GPU，且当前物理资源允许 2 Worker × 8 GPU 准入。

任一项不满足都应先联系团队管理员，不要通过共享账号、临时镜像或绕过平台入口继续。

### 3.1 Git 凭据

优先使用个人 SSH Key 拉私有仓库，不要把 GitLab Token 写进 clone URL、脚本或 shell history：

```bash
ssh -T git@gitlab.qomolo.com
```

如果组织要求 HTTPS，请使用 Git 的安全凭据管理器，在交互提示中输入凭据。平台网页从 Git 提交时，凭据应在“Git 凭据”页面添加并测试，不写入算法仓库。

没有 SSH Key 时，可以安全地使用 HTTPS；不要把 PAT 放进 URL：

```bash
git clone --single-branch --branch bev_3dod \
  https://gitlab.qomolo.com/dl/bevfusion.git \
  bevfusion-bev_3dod
```

Git 提示认证时，用户名输入 `oauth2`，密码位置粘贴个人 GitLab PAT。PAT 不会出现在命令历史和 Git remote URL 中。平台 PAT 与 GitLab PAT 是两种不同凭据，不能混用。

### 3.2 安装 `spk-rayjob`

下面给出 Linux AMD64 命令。macOS、Windows 用户应打开 Portal“外部提交”页面，复制当前平台版本对应的安装和校验命令；[三种提交方式](SUBMIT_GUIDE.md#31-安装与登录)同时提供 Linux、macOS、Windows 的完整示例。

Linux AMD64：

```bash
mkdir -p ~/.local/bin ~/.cache/spk-rayjob

curl -fL https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-linux-amd64 \
  -o ~/.cache/spk-rayjob/spk-rayjob-linux-amd64
curl -fL https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS \
  -o ~/.cache/spk-rayjob/SHA256SUMS

(cd ~/.cache/spk-rayjob && \
  grep 'spk-rayjob-linux-amd64$' SHA256SUMS | sha256sum -c -)

install -m 0755 \
  ~/.cache/spk-rayjob/spk-rayjob-linux-amd64 \
  ~/.local/bin/spk-rayjob

export PATH="$HOME/.local/bin:$PATH"
spk-rayjob --help
```

上面的 `export` 只对当前终端生效。再执行一次下面的命令持久化 PATH，重新登录后仍可直接使用：

```bash
grep -qxF 'export PATH="$HOME/.local/bin:$PATH"' "$HOME/.profile" || \
  printf '%s\n' 'export PATH="$HOME/.local/bin:$PATH"' >>"$HOME/.profile"
```

### 3.3 使用平台账号登录

```bash
spk-rayjob login --server https://raytrain.wellspiking.ai
spk-rayjob jobs
```

登录信息与 Web Portal 账号一致。客户端只获得提交本人训练任务所需的权限，不获得 Kubernetes kubeconfig 或对象存储 AK/SK。

这里有两个名字容易混淆：

- `--config` 是登录认证 JSON，只包含平台地址和登录令牌。它必须由 `spk-rayjob login --config <文件.json>` 生成，并保持属主可读，例如权限 `0600`；它不是任务 YAML。
- `.spk-rayjob.yaml` 是任务默认值，由 `spk-rayjob init` 在代码目录生成。`spk-rayjob submit --dir <代码目录>` 只自动读取该目录中这个固定文件名，不支持用 `--config` 指向另一份任务 YAML。

因此下面两种方式才是正确的：

```bash
# 独立保存登录态；后续所有选项都放在任务 ID 前面。
AUTH_CONFIG="$HOME/.config/spk-rayjob/config.json"
spk-rayjob login \
  --server https://raytrain.wellspiking.ai \
  --config "$AUTH_CONFIG"

# 任务默认值必须放在 checkout 根目录的固定文件名中。
cd ~/training-src/bevfusion-bev_3dod
spk-rayjob init
spk-rayjob submit --config "$AUTH_CONFIG" --watch
```

不要把派生的任务 YAML 传给 `--config`；即使内容看起来与 `.spk-rayjob.yaml` 等价，也会因它不是认证 JSON 而得到 `invalid spk-rayjob config`。如需保存另一套任务参数，应使用另一份 checkout/工作目录，或在提交时用显式参数覆盖 `.spk-rayjob.yaml`。

---

## 4. 拉取全新代码

两个分支分别使用独立目录，不要在同一 dirty working tree 里来回切换：

### 4.1 `bev_3dod`

```bash
mkdir -p ~/training-src
cd ~/training-src

git clone --single-branch --branch bev_3dod \
  git@gitlab.qomolo.com:dl/bevfusion.git \
  bevfusion-bev_3dod

cd bevfusion-bev_3dod
git rev-parse --short HEAD
git status --short
git switch -c platform/raytrain-bev-3dod
```

已验证基线应显示 `0c1dc9d`。如果提交不同，文件结构和修改位置可能已经变化；应先对照实际源码确认，再应用第 5 节差异。

### 4.2 `bev_3dod_s1h`

```bash
mkdir -p ~/training-src
cd ~/training-src

git clone --single-branch --branch bev_3dod_s1h \
  git@gitlab.qomolo.com:dl/bevfusion.git \
  bevfusion-bev_3dod_s1h

cd bevfusion-bev_3dod_s1h
git rev-parse --short HEAD
git status --short
git switch -c platform/raytrain-bev-3dod-s1h
```

已验证基线应显示 `7931cee`。

---

## 5. 直接修改原始 BEVFusion 代码

本节给出所有必需改动。用户只需要自己的 BEVFusion checkout 和本文。

两个分支都执行 5.1～5.6 和 5.8；`bev_3dod_s1h` 还要执行 5.7。5.8 不是可选项：跳过后训练仍可能成功，但实验中心不会保存完整 Loss、学习率和验证指标。

### 5.1 新增数据路径解析器

创建 `mmdet3d/datasets/platform_paths.py`：

```python
"""Re-root absolute dataset paths onto PLATFORM_DATASET_PATH."""

from __future__ import annotations

import os
from typing import Any, Dict, Iterable, List, Optional, Tuple

DATASET_ROOT_ENV = "PLATFORM_DATASET_PATH"


def safe_path_parts(recorded_path: str) -> Optional[List[str]]:
    """Split an index path without allowing traversal components."""
    parts = [part for part in recorded_path.split("/") if part]
    if any(part in (".", "..") for part in parts):
        return None
    return parts


def mounted_dataset_root() -> Optional[str]:
    """Return the directory selected for this training task."""
    root = os.environ.get(DATASET_ROOT_ENV, "").strip()
    return root or None


def existing_path_within_root(path: str, mounted_root: str) -> bool:
    """Accept existing files only when they stay inside the selected root."""
    try:
        root = os.path.realpath(mounted_root)
        candidate = os.path.realpath(path)
        return os.path.commonpath((root, candidate)) == root and os.path.exists(candidate)
    except (OSError, ValueError):
        return False


def discover_drop_count(recorded_path: str, mounted_root: str) -> Optional[int]:
    """Find how many leading path components must be discarded."""
    parts = safe_path_parts(recorded_path)
    if parts is None:
        return None
    for drop in range(len(parts)):
        candidate = os.path.join(mounted_root, *parts[drop:])
        if existing_path_within_root(candidate, mounted_root):
            return drop
    return None


def discover_path_rewrite(
    recorded_path: str, mounted_root: str
) -> Optional[Tuple[Tuple[str, ...], int]]:
    """Discover a direct or one-level namespace rewrite."""
    direct_drop = discover_drop_count(recorded_path, mounted_root)
    if direct_drop is not None:
        return (), direct_drop

    parts = safe_path_parts(recorded_path)
    if parts is None:
        return None
    try:
        namespaces = tuple(
            sorted(
                entry.name
                for entry in os.scandir(mounted_root)
                if entry.is_dir(follow_symlinks=False)
            )
        )
    except OSError:
        return None

    for drop in range(len(parts)):
        for namespace in namespaces:
            candidate = os.path.join(mounted_root, namespace, *parts[drop:])
            if existing_path_within_root(candidate, mounted_root):
                return (namespace,), drop
    return None


class DatasetPathResolver:
    """Resolve recorded absolute paths below the selected dataset root."""

    def __init__(self, mounted_root: Optional[str] = None) -> None:
        configured_root = (
            mounted_root if mounted_root is not None else mounted_dataset_root()
        )
        self._root = configured_root
        self._prefix: Tuple[str, ...] = ()
        self._drop: Optional[int] = None
        self._resolved = False
        self._disabled = self._root is None

    @property
    def active(self) -> bool:
        return not self._disabled

    def resolve(self, path: Any) -> Any:
        if self._disabled or not isinstance(path, str) or not path:
            return path
        parts = safe_path_parts(path)
        if parts is None:
            return path
        if existing_path_within_root(path, self._root):
            return path

        if not self._resolved:
            rewrite = discover_path_rewrite(path, self._root)
            if rewrite is None:
                return path
            self._prefix, self._drop = rewrite
            self._resolved = True

        if self._drop is None or self._drop >= len(parts):
            return path

        candidate = os.path.join(
            self._root, *self._prefix, *parts[self._drop:]
        )
        if existing_path_within_root(candidate, self._root):
            return candidate

        # 一个 pkl 可能同时引用多个发布命名空间；缓存失配时重新发现。
        rewrite = discover_path_rewrite(path, self._root)
        if rewrite is None:
            return path
        self._prefix, self._drop = rewrite
        candidate = os.path.join(
            self._root, *self._prefix, *parts[self._drop:]
        )
        return (
            candidate
            if existing_path_within_root(candidate, self._root)
            else path
        )

    def resolve_sweeps(
        self, sweeps: Iterable[Dict[str, Any]]
    ) -> List[Dict[str, Any]]:
        resolved: List[Dict[str, Any]] = []
        for sweep in sweeps or []:
            if isinstance(sweep, dict) and "data_path" in sweep:
                sweep = dict(sweep)
                sweep["data_path"] = self.resolve(sweep["data_path"])
            resolved.append(sweep)
        return resolved
```

这个 resolver 不写死旧机器前缀，也不写死 `fz → cnfzhjyg`：它从真实存在的文件发现最长后缀，限制结果必须位于本任务选择的数据根目录中，并缓存成功映射。

### 5.2 修改 `nuscenes_dataset.py`

在 `mmdet3d/datasets/nuscenes_dataset.py` 的相对导入区加入：

```python
from .platform_paths import DatasetPathResolver
```

在 `NuScenesDataset` 类中、`get_data_info()` 前加入：

```python
    @property
    def _platform_paths(self) -> DatasetPathResolver:
        resolver = getattr(self, "_platform_paths_cache", None)
        if resolver is None:
            resolver = DatasetPathResolver()
            self._platform_paths_cache = resolver
        return resolver
```

在 `get_data_info()` 中修改 lidar 和 sweep：

```diff
-            lidar_path=info["lidar_path"],
-            sweeps=info["sweeps"],
+            lidar_path=self._platform_paths.resolve(info["lidar_path"]),
+            sweeps=self._platform_paths.resolve_sweeps(info["sweeps"]),
```

修改 camera 路径：

```diff
-                data["image_paths"].append(camera_info["data_path"])
+                data["image_paths"].append(
+                    self._platform_paths.resolve(camera_info["data_path"])
+                )
```

不要只修改 lidar。BEVFusion 同时读取 sweep 和多个 camera，漏掉任意一处都会在训练几分钟后报路径不存在。

### 5.3 修正 `.gitignore`

两个原始分支的 `.gitignore` 都含有：

```text
run*/
```

它会意外匹配并排除 `mmdet3d/runner/`。改成只忽略仓库根目录的输出：

```diff
-run*/
+/run*/
```

### 5.4 修改 `tools/westwell_train.py`

确认文件顶部已经导入 `copy`、`os` 和 `Config`；缺少时加入：

```python
import copy
import os

from mmcv import Config
```

在 `main()` 前加入完整函数：

```python
def configure_platform_output(cfg):
    """Use stdout/Loki and avoid append-style writes on object storage."""
    if not os.environ.get("PLATFORM_OUTPUT_PATH"):
        return cfg

    # MMCV 1.x 的 deepcopy(Config) 会退化成 ConfigDict，丢失 dump()。
    platform_cfg = Config(
        copy.deepcopy(cfg._cfg_dict), filename=cfg.filename
    )
    platform_cfg.log_config.hooks = [
        dict(hook)
        for hook in platform_cfg.log_config.hooks
        if hook.get("type") != "TensorboardLoggerHook"
    ]

    from mmcv.runner.hooks.logger.text import TextLoggerHook

    def raytrain_noop_json_log(*_args, **_kwargs):
        return None

    TextLoggerHook._dump_log = raytrain_noop_json_log
    return platform_cfg
```

在 `parser.parse_known_args()` 前增加 torchrun 参数：

```diff
+    parser.add_argument(
+        "--local-rank", "--local_rank", type=int, default=0
+    )
     args, opts = parser.parse_known_args()
```

创建 `Config` 后立即调用输出兼容逻辑：

```diff
     cfg = Config(recursive_eval(configs), filename=args.config)
+    cfg = configure_platform_output(cfg)
```

配置文件只允许全局 rank 0 写：

```diff
-    cfg.dump(os.path.join(cfg.run_dir, "configs.yaml"))
+    if int(os.environ.get("RANK", "0")) == 0:
+        cfg.dump(os.path.join(cfg.run_dir, "configs.yaml"))
```

平台运行时不创建需要 append 的本地日志文件，stdout 会被 Loki 保存：

```diff
-    log_file = os.path.join(cfg.run_dir, f"{timestamp}.log")
+    log_file = (
+        None
+        if os.environ.get("PLATFORM_OUTPUT_PATH")
+        else os.path.join(cfg.run_dir, f"{timestamp}.log")
+    )
```

调用 `train_model()` 时不要写死分布式模式：

```diff
-        distributed=True,
+        distributed=distributed,
```

这些修改不会改变模型结构和 loss。没有 `PLATFORM_OUTPUT_PATH` 的本地运行仍保留原日志行为；平台运行时的 loss、lr、mAP 和 traceback 全部从 stdout 进入 Loki。

### 5.5 创建 `.rayignore`

`spk-rayjob` 每次提交都会打包当前目录。创建 `.rayignore`：

```text
.git/
.venv/
__pycache__/
*.pyc
*.pkl
*.pth
/run/
/run_dir/
mmdet3d/ops/
```

这组规则只排除已经确认安全的内容。当前客户端的忽略规则会按目录名匹配，`data/`、`datasets/`、`work_dirs/` 以及带前导 `/` 的同名写法都可能误伤 `mmdet3d/datasets/`，因此不要加入这些规则。大型数据必须放在 checkout 外；提交前按第 6.2 节检查 ZIP，确认 `mmdet3d/datasets/platform_paths.py` 仍在包内。

其他格式的大型训练数据不会被自动识别，仍建议放在算法 checkout 外。当前固定镜像已经包含匹配的 `mmdet3d.ops` 和 CUDA 扩展；入口中的 `raytrain-bevfusion-prepare` 会把镜像内缺失的扩展补入 working directory，但不会覆盖用户 Python 源码。提交前必须按第 6.2 节检查最终 ZIP，确认 `mmdet3d/datasets/platform_paths.py` 存在。

如果用户修改了自定义 CUDA/C++ op，必须删除 `mmdet3d/ops/` 忽略规则、重新构建兼容镜像并登记新的不可变 digest；不能把任意机器上编译的 `.so` 随源码包上传。

### 5.6 新增数据预检脚本

创建 `tools/platform_data_preflight.py`：

```python
#!/usr/bin/env python3
"""Validate BEVFusion annotation paths against PLATFORM_DATASET_PATH."""

from __future__ import annotations

import argparse
import os
import pickle
import sys
from pathlib import Path


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--train", required=True)
    parser.add_argument("--val", required=True)
    parser.add_argument("--max-train", type=int, default=1024)
    parser.add_argument("--max-val", type=int, default=512)
    return parser.parse_args()


def resolve_annotation(root: Path, relative: str) -> Path:
    requested = Path(relative)
    if requested.is_absolute():
        raise ValueError("annotation path must be relative")
    resolved = (root / requested).resolve()
    if os.path.commonpath((str(root), str(resolved))) != str(root):
        raise ValueError("annotation path escapes PLATFORM_DATASET_PATH")
    return resolved


def load_infos(path: Path):
    with path.open("rb") as stream:
        payload = pickle.load(stream)
    infos = payload.get("infos", payload) if isinstance(payload, dict) else payload
    if not isinstance(infos, list):
        raise TypeError(f"{path} does not contain an info list")
    return infos


def candidate_paths(info):
    if info.get("lidar_path"):
        yield info["lidar_path"]
    for sweep in info.get("sweeps", [])[:1]:
        if sweep.get("data_path"):
            yield sweep["data_path"]
    for camera in info.get("cams", {}).values():
        if camera.get("data_path"):
            yield camera["data_path"]


def main():
    args = parse_args()
    root = Path(os.environ["PLATFORM_DATASET_PATH"]).resolve()
    script_path = Path(__file__).resolve()
    sys.path.insert(0, str(script_path.parent.parent / "mmdet3d" / "datasets"))
    from platform_paths import DatasetPathResolver

    checked = 0
    missing = []
    splits = (
        ("train", args.train, args.max_train),
        ("val", args.val, args.max_val),
    )
    for split, relative, limit in splits:
        annotation = resolve_annotation(root, relative)
        infos = load_infos(annotation)
        print(
            f"ANNOTATION split={split} path={annotation} samples={len(infos)}",
            flush=True,
        )
        resolver = DatasetPathResolver(str(root))
        for info in infos[:limit]:
            for recorded in candidate_paths(info):
                resolved = resolver.resolve(recorded)
                checked += 1
                if not os.path.exists(resolved):
                    missing.append((recorded, resolved))

    print(f"PATH_CHECK checked={checked} missing={len(missing)}", flush=True)
    for recorded, resolved in missing[:10]:
        print(f"MISSING recorded={recorded} resolved={resolved}", flush=True)
    if missing:
        return 2
    print("BEVFUSION_PLATFORM_DATA_PREFLIGHT_OK", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

### 5.7 `bev_3dod_s1h` 的额外配置

只在 `bev_3dod_s1h` 分支修改以下两处。

`configs/default.yaml`：

```diff
-load_from: /storage/run_dir/lidar_0706/epoch_20.pth
+load_from: null
```

`configs/westwell/det/transfusion/default.yaml`：

```diff
-post_center_range: ${post_center_range}
+post_center_range: [-61.2, -61.2, -10.0, 61.2, 61.2, 10.0]
```

历史 checkpoint 不能写死在代码中。正式续训由新任务显式选择旧任务，并通过 `$PLATFORM_CHECKPOINT_PATH` 读取。

如果 S1H 要使用本文的全量 `0429_pkl` 数据，必须先由算法负责人核对 `object_classes`、`name_mapping` 和 TransFusion Head 的 `num_classes`。当前全量数据包含映射为第 10 类的 IGV，而历史 S1H smoke 配置的 Head 只有 9 类；直接复用会触发 CUDA index out of bounds。把 Head 改成 10 类会改变模型结构并与旧 9 类 checkpoint 不兼容。即使类别数修正，历史复跑仍观察到 Hungarian cost 和 loss NaN，因此 S1H 全量还需要单独完成学习率、fp16 和收敛验收。

S1H 全量数值稳定性建议按下面顺序做单变量实验，不要一次同时修改全部参数：

1. 先关闭 fp16，用 fp32 跑 1 卡小样本，确认 loss 全程有限；
2. 保持 fp32，降低学习率到当前值的 `1/2` 或 `1/10`；
3. 增加梯度裁剪并记录 `grad_norm`，确认异常发生在哪个 loss 分项；
4. 如必须使用 fp16，再启用 dynamic loss scale，并记录每次 overflow/scale 变化；
5. 延长 warmup，避免多机 world size 放大有效 batch 后过早进入高学习率；
6. 最后恢复 2×8，并要求所有 rank loss 有限、验证指标非零、checkpoint 可 resume。

`num_classes: 10` 和 HungarianAssigner NaN 防护属于算法补丁，必须经过独立的 fp32、fp16 和 checkpoint 兼容验收后，以企业 Git MR 或可访问的 commit 发布。未发布的本地 commit 不能作为交付链接；本文当前不伪造 MR 地址，也不把一次本地成功当作 S1H 全量基线。算法负责人发布 MR 后，应在这里补充 MR URL、commit、适用基线和验收任务 ID。

### 5.8 接入平台 MLflow 实验中心

两个分支都新增 `mmdet3d/utils/platform_mlflow.py`。这段代码只在全局 rank 0 创建 run，参数、loss、学习率和验证指标进入实验中心；checkpoint 仍写 `PLATFORM_OUTPUT_PATH`：

```python
"""Bridge MMCV scalar logging to the platform-owned MLflow run."""

from __future__ import annotations

import os
from typing import Any, Dict, Optional


def _required_environment(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError("platform MLflow contract is missing {}".format(name))
    return value


def _mapping_value(value: Any, name: str, default: Any = None) -> Any:
    if value is None:
        return default
    getter = getattr(value, "get", None)
    return getter(name, default) if callable(getter) else default


def _platform_tags() -> Dict[str, str]:
    return {
        "platform.job_id": _required_environment("RAYTRAIN_JOB_ID"),
        "platform.tenant_id": _required_environment("RAYTRAIN_TENANT_ID"),
        "platform.submitter_user_id": _required_environment(
            "RAYTRAIN_SUBMITTER_USER_ID"
        ),
        "platform.provenance": _required_environment(
            "RAYTRAIN_MLFLOW_PROVENANCE"
        ),
    }


def start_platform_mlflow(cfg: Any, rank: int, world_size: int) -> Optional[Any]:
    tracking_uri = os.environ.get("MLFLOW_TRACKING_URI", "").strip()
    if not tracking_uri or rank != 0:
        return None

    import mlflow

    mlflow.set_tracking_uri(tracking_uri)
    mlflow.set_experiment(_required_environment("MLFLOW_EXPERIMENT_NAME"))
    mlflow.start_run(
        run_name=_required_environment("MLFLOW_RUN_NAME"),
        tags=_platform_tags(),
    )
    optimizer = _mapping_value(cfg, "optimizer", {})
    runner = _mapping_value(cfg, "runner", {})
    filename = getattr(cfg, "filename", "") or ""
    mlflow.log_params(
        {
            "config_file": os.path.basename(filename),
            "learning_rate": _mapping_value(optimizer, "lr"),
            "max_epochs": _mapping_value(runner, "max_epochs"),
            "optimizer": _mapping_value(optimizer, "type"),
            "seed": _mapping_value(cfg, "seed"),
            "world_size": world_size,
        }
    )
    interval = int(cfg.log_config.get("interval", 10))
    cfg.log_config.hooks.append(
        {
            "type": "MlflowLoggerHook",
            "log_model": False,
            "interval": interval,
            "ignore_last": False,
            "reset_flag": False,
            "by_epoch": True,
        }
    )
    return mlflow


def finish_platform_mlflow(client: Optional[Any], status: str) -> None:
    if client is not None and client.active_run() is not None:
        client.end_run(status=status)
```

然后在 `tools/westwell_train.py` 中导入并包住训练调用：

```diff
 from mmdet3d.utils import get_root_logger, convert_sync_batchnorm, recursive_eval
+from mmdet3d.utils.platform_mlflow import (
+    finish_platform_mlflow,
+    start_platform_mlflow,
+)
@@
    logger.info(f"Model:\n{model}")
+    rank, world_size = get_dist_info()
+    platform_mlflow = start_platform_mlflow(cfg, rank, world_size)
-    train_model(
-        model, datasets, cfg, distributed=distributed,
-        validate=True, timestamp=timestamp,
-    )
+    try:
+        train_model(
+            model, datasets, cfg, distributed=distributed,
+            validate=True, timestamp=timestamp,
+        )
+    except BaseException:
+        finish_platform_mlflow(platform_mlflow, "FAILED")
+        raise
+    else:
+        finish_platform_mlflow(platform_mlflow, "FINISHED")
```

MLflow run 在 Dataset、模型和 Logger 初始化成功后、进入 `train_model()` 前才创建；这样初始化阶段失败不会留下永久 `RUNNING` 的空 run，训练异常则会明确结束为 `FAILED`。不要在代码中写死 MLflow 地址、实验名、租户或任务 ID；平台会为每个任务注入并校验这些字段。自定义镜像必须包含与 Python 版本兼容的 MLflow client；本手册固定的 BEVFusion 镜像已经包含。

当前交付把实验记录视为训练验收的一部分：如果 `start_platform_mlflow()` 返回 404/5xx，任务会失败，而不是悄悄丢失 Loss 后继续。用户不要在算法仓库里临时写死地址或吞掉异常；先保留最早的 Worker traceback，并联系平台管理员检查 MLflow ingest。平台健康检查路径是 `/healthz`，不是 `/health`。平台应在 16 卡任务准入前完成 MLflow API 预检，避免把实验服务故障拖到训练启动后才暴露。

---

## 6. 改造后自检

### 6.1 Python 语法和忽略规则

```bash
python3 -m py_compile \
  mmdet3d/datasets/platform_paths.py \
  mmdet3d/datasets/nuscenes_dataset.py \
  mmdet3d/utils/platform_mlflow.py \
  tools/westwell_train.py \
  tools/platform_data_preflight.py

if git check-ignore -q mmdet3d/runner/__init__.py; then
  echo '错误：mmdet3d/runner 仍被 .gitignore 排除' >&2
  git check-ignore -v mmdet3d/runner/__init__.py
  exit 1
fi

grep -Fx 'mmdet3d/ops/' .rayignore
if grep -Eq '^(data|datasets|work_dirs)/?$' .rayignore; then
  echo '错误：.rayignore 含有会排除嵌套源码目录的宽泛规则' >&2
  exit 1
fi
grep -n 'def configure_platform_output' tools/westwell_train.py
grep -n 'DatasetPathResolver' mmdet3d/datasets/nuscenes_dataset.py
git diff --check
git status --short
```

### 6.2 检查实际提交包

不要只检查 Git working tree，还要检查 `spk-rayjob` 最终生成的 ZIP：

```bash
PACKAGE="/tmp/bevfusion-source-$(date +%Y%m%d-%H%M%S).zip"
spk-rayjob package --dir . --output "$PACKAGE"

unzip -l "$PACKAGE" | grep 'mmdet3d/datasets/platform_paths.py'
unzip -l "$PACKAGE" | grep 'mmdet3d/runner/__init__.py'
unzip -l "$PACKAGE" | grep 'mmdet3d/utils/platform_mlflow.py'

if unzip -l "$PACKAGE" | grep -Eq '\.(pkl|pth)$'; then
  echo '错误：源码包中混入了数据或 checkpoint' >&2
  exit 1
fi
```

三个 `grep` 都必须命中；如果任一关键文件不在 ZIP 中，不要提交任务。检查完成后可以删除这个临时 ZIP。

### 6.3 不依赖训练环境的路径解析自测

下面测试只使用 Python 标准库，不需要安装 MMCV、PyTorch 或 CUDA：

```bash
python3 - <<'PY'
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, "mmdet3d/datasets")
from platform_paths import DatasetPathResolver

with tempfile.TemporaryDirectory() as temporary:
    root = Path(temporary)
    target = root / "published" / "scene-a" / "samples" / "LIDAR_TOP" / "1.bin"
    target.parent.mkdir(parents=True)
    target.write_bytes(b"test")

    resolver = DatasetPathResolver(str(root))
    recorded = "/old-machine/temp_data/fz/scene-a/samples/LIDAR_TOP/1.bin"
    resolved = Path(resolver.resolve(recorded))
    assert resolved == target, (resolved, target)
    print("PLATFORM_PATH_RESOLVER_OK", resolved)
PY
```

预期结果：

```text
PLATFORM_PATH_RESOLVER_OK <临时目录>/published/scene-a/samples/LIDAR_TOP/1.bin
```

### 6.4 保存改造

查看差异：

```bash
git diff -- .gitignore tools/westwell_train.py \
  mmdet3d/datasets/nuscenes_dataset.py \
  mmdet3d/utils/platform_mlflow.py configs
git diff --check
git status --short
```

提交到自己的算法分支：

```bash
# 全新机器首次提交前设置当前仓库的 Git 身份；使用自己的真实信息。
git config --local user.name "你的姓名"
git config --local user.email "你的企业邮箱"

git config --get user.name
git config --get user.email

git add \
  .gitignore .rayignore \
  mmdet3d/datasets/platform_paths.py \
  mmdet3d/datasets/nuscenes_dataset.py \
  mmdet3d/utils/platform_mlflow.py \
  tools/westwell_train.py \
  tools/platform_data_preflight.py \
  configs

git commit -m 'fix: support RayTrain distributed execution'
git rev-parse HEAD
```

如果暂时没有推送分支，`spk-rayjob` 仍会提交当前 working tree；但正式实验必须记录 Git commit 和未提交差异，否则无法复现同一次训练。

---

## 7. 数据从哪里读、结果写到哪里

用户看到的是逻辑目录，不是 TOS URI：

| 目录/变量 | 权限 | 用途 |
| --- | --- | --- |
| `/mnt/storage/public` | 只读 | 浏览全部已发布公共数据。 |
| `$PLATFORM_DATASET_PATH` | 只读 | 当前任务选择的公共/团队/个人输入。 |
| `$PLATFORM_OUTPUT_PATH` | 读写 | 当前任务独占的结果与 checkpoint。 |
| `$PLATFORM_CHECKPOINT_PATH` | 只读 | 续训时选中的历史任务结果。 |

当前公共数据在对象存储的管理员前缀由平台映射为 `/mnt/storage/public`。用户提交时只选择：

```yaml
input:
  space: public
  path: ""
```

`path: ""` 表示选择公共根目录。不要填写 `.`，不要在训练代码里使用 TOS URI，也不要注入 AK/SK。

### 7.1 `input-path`、挂载根和 pkl 文件名对照

`$PLATFORM_DATASET_PATH` 不是永远等于 `/mnt/storage/public`：它指向本次任务选择的目录根。选择子目录后，平台只把该子目录作为任务输入根，代码里不能再次拼接这个子目录。

| 场景 | pkl 引用路径（相对 `$PLATFORM_DATASET_PATH`） | `--input-space` | 必需的 `--input-path` | Pod 内 `$PLATFORM_DATASET_PATH` |
| --- | --- | --- | --- | --- |
| 全量 FZ | `0429_pkl/fz/merged_nuscenes_infos_*.pkl`，根目录必须留空 | `public` | `''` | `/mnt/storage/public` |
| smoke-128 | `platform-validation/annotations/fz-0429-platform-smoke-128/final_merged_nuscenes_infos_*.pkl` | `public` | `bevfusion/fz-3dod-v1` | 选中子目录的挂载根 |

两套索引名称不同，不要凭文件名猜测：

```text
# 全量：公共根下
0429_pkl/fz/merged_nuscenes_infos_train.pkl
0429_pkl/fz/merged_nuscenes_infos_val.pkl

# smoke：选择 bevfusion/fz-3dod-v1 后，相对于所选根
platform-validation/annotations/fz-0429-platform-smoke-128/final_merged_nuscenes_infos_train.pkl
platform-validation/annotations/fz-0429-platform-smoke-128/final_merged_nuscenes_infos_val.pkl
```

提交前用同一套规则检查：全量命令必须是 `--input-path ''`，smoke 命令必须是 `--input-path bevfusion/fz-3dod-v1`。如果 smoke 选择了公共根，或选择子目录后又在代码里拼接 `bevfusion/fz-3dod-v1`，都会得到“文件不存在”。

公共根的实际对象存储前缀是 `tos://shanghai-data-transfer/ray-train/public/`。当前目录至少包含：

```text
0429_pkl/                 全量 FZ 训练/验证索引
0813_pkl/                 另一批索引
labeled/                  持续同步的全量原始数据，当前平台可见 35 个站点目录
bevfusion/fz-3dod-v1/     128 样本平台验收数据
```

全量任务选择公共根（`path: ""`），因此索引写成 `$PLATFORM_DATASET_PATH/0429_pkl/...`；smoke 任务选择 `bevfusion/fz-3dod-v1`，因此索引写成 `$PLATFORM_DATASET_PATH/platform-validation/...`。不要把 `bevfusion/fz-3dod-v1` 重复拼接到后者。`labeled/` 只是原始文件池，真正参与训练的样本由 pkl 索引决定。

全量 FZ 索引：

```text
$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_train.pkl
$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl
```

索引中的旧绝对路径可能以 `/temp_data/fz/...` 开头，实际文件位于公共根的其他发布目录。第 5 节安装的 `DatasetPathResolver` 会在公共根内寻找最长可命中后缀并缓存映射，不需要批量重写 pkl，也不应在代码中写死某个机器路径。

---

## 8. 先提交 1 卡数据预检

正式占用 16 卡前，先验证：源码包完整、镜像可运行、pkl 可读、抽查的 lidar/sweep/camera 文件存在。

在 BEVFusion checkout 根目录执行：

```bash
IMAGE='harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553'

spk-rayjob submit \
  --engine ray-ddp \
  --name bevfusion-fz-data-preflight \
  --image "$IMAGE" \
  --entrypoint 'python3 tools/platform_data_preflight.py --train 0429_pkl/fz/merged_nuscenes_infos_train.pkl --val 0429_pkl/fz/merged_nuscenes_infos_val.pkl --max-train 1024 --max-val 512' \
  --input-space public \
  --input-path '' \
  --output-path bevfusion/preflight \
  --workers 1 \
  --gpus-per-worker 1 \
  --cpu-per-worker 8 \
  --memory-per-worker 32Gi \
  --execution-mode single_gpu \
  --watch
```

通过标志：

```text
PATH_CHECK checked=<数量> missing=0
BEVFUSION_PLATFORM_DATA_PREFLIGHT_OK
```

如果 `missing` 不为 0，不要继续提交 16 卡。先确认选择的公共目录、pkl 版本、原始数据是否完整，以及路径 resolver 是否已经接入 `nuscenes_dataset.py`。

### 8.1 1 卡探针调试：先定位，再申请 16 卡

目录、MLflow 和历史结果问题都应先用 `1 Worker × 1 GPU × 8 CPU × 32GiB` 的短任务定位。探针使用与正式任务相同的镜像、数据挂载、身份和网络，因此比在提交机器上执行 `ls` 或 `curl` 更接近真实训练环境。

把下面三个脚本放进 checkout 的 `tools/`。它们会随 working directory archive 上传，不需要构建镜像。

`tools/platform_directory_probe.py`：

```python
from __future__ import annotations

import argparse
import os
from pathlib import Path


def selected_path(relative: str) -> Path:
    root = Path(os.environ["PLATFORM_DATASET_PATH"]).resolve()
    candidate = (root / relative).resolve()
    try:
        candidate.relative_to(root)
    except ValueError as error:
        raise SystemExit("path escapes PLATFORM_DATASET_PATH") from error
    return candidate


parser = argparse.ArgumentParser()
parser.add_argument("relative", nargs="?", default=".")
parser.add_argument("--fail-after-print", action="store_true")
args = parser.parse_args()
target = selected_path(args.relative)
print("DATASET_ROOT", os.environ["PLATFORM_DATASET_PATH"])
print("TARGET", target, "EXISTS", target.exists())
if not target.exists():
    raise SystemExit(2)
if target.is_dir():
    for child in sorted(target.iterdir())[:200]:
        print("ENTRY", child.name, "DIR" if child.is_dir() else child.stat().st_size)
else:
    print("FILE_SIZE", target.stat().st_size)
if args.fail_after_print:
    raise SystemExit("intentional probe failure after printing diagnostics")
```

`tools/platform_mlflow_probe.py`：

```python
from __future__ import annotations

import os
import urllib.request

import mlflow


tracking_uri = os.environ["MLFLOW_TRACKING_URI"].rstrip("/")
with urllib.request.urlopen(tracking_uri + "/healthz", timeout=10) as response:
    print("MLFLOW_HEALTH", response.status, response.read(256).decode("utf-8", "replace"))

mlflow.set_tracking_uri(tracking_uri)
mlflow.set_experiment(os.environ["MLFLOW_EXPERIMENT_NAME"])
tags = {
    "platform.job_id": os.environ["RAYTRAIN_JOB_ID"],
    "platform.tenant_id": os.environ["RAYTRAIN_TENANT_ID"],
    "platform.submitter_user_id": os.environ["RAYTRAIN_SUBMITTER_USER_ID"],
    "platform.provenance": os.environ["RAYTRAIN_MLFLOW_PROVENANCE"],
}
with mlflow.start_run(run_name=os.environ["MLFLOW_RUN_NAME"] + "-probe", tags=tags):
    mlflow.log_param("probe", "mlflow-connectivity")
    mlflow.log_metric("probe_ok", 1.0)
print("MLFLOW_PROBE_OK")
```

`tools/platform_result_probe.py`：

```python
from __future__ import annotations

import argparse
import os
from pathlib import Path


parser = argparse.ArgumentParser()
parser.add_argument("relative", nargs="?", default=".")
parser.add_argument("--fail-after-print", action="store_true")
args = parser.parse_args()
root = Path(os.environ["PLATFORM_CHECKPOINT_PATH"]).resolve()
target = (root / args.relative).resolve()
try:
    target.relative_to(root)
except ValueError as error:
    raise SystemExit("path escapes PLATFORM_CHECKPOINT_PATH") from error
print("CHECKPOINT_ROOT", root)
print("TARGET", target, "EXISTS", target.exists())
if not target.exists():
    raise SystemExit(2)
if target.is_dir():
    for child in sorted(target.iterdir())[:200]:
        print("ENTRY", child.name, "DIR" if child.is_dir() else child.stat().st_size)
else:
    print("FILE_SIZE", target.stat().st_size)
    if target.suffix.lower() in {".txt", ".json", ".yaml", ".yml", ".log"}:
        print(target.read_text(encoding="utf-8", errors="replace")[:4096])
if args.fail_after_print:
    raise SystemExit("intentional probe failure after printing diagnostics")
```

目录探针示例。注意它与 smoke 使用相同的子目录选择：

```bash
spk-rayjob submit \
  --engine ray-ddp \
  --name "bevfusion-dir-probe-$(date +%H%M%S)" \
  --image "$IMAGE" \
  --entrypoint 'python3 tools/platform_directory_probe.py platform-validation/annotations/fz-0429-platform-smoke-128/' \
  --input-space public \
  --input-path bevfusion/fz-3dod-v1 \
  --output-path bevfusion/probes/directory \
  --workers 1 --gpus-per-worker 1 \
  --cpu-per-worker 8 --memory-per-worker 32Gi \
  --execution-mode single_gpu --watch
```

MLflow 探针示例：

```bash
spk-rayjob submit \
  --engine ray-ddp \
  --name "bevfusion-mlflow-probe-$(date +%H%M%S)" \
  --image "$IMAGE" \
  --entrypoint 'python3 tools/platform_mlflow_probe.py' \
  --output-path bevfusion/probes/mlflow \
  --workers 1 --gpus-per-worker 1 \
  --cpu-per-worker 8 --memory-per-worker 32Gi \
  --execution-mode single_gpu --watch
```

读取历史结果探针示例。`--resume-from-job` 只读挂载指定任务的结果：

```bash
spk-rayjob submit \
  --engine ray-ddp \
  --name "bevfusion-result-probe-$(date +%H%M%S)" \
  --image "$IMAGE" \
  --entrypoint 'python3 tools/platform_result_probe.py epoch_1.pth' \
  --resume-from-job <已有成功任务ID> \
  --output-path bevfusion/probes/result \
  --workers 1 --gpus-per-worker 1 \
  --cpu-per-worker 8 --memory-per-worker 32Gi \
  --execution-mode single_gpu --watch
```

`--fail-after-print` 用于主动让探针失败，以验证失败状态和 `statusMessage` 兜底；它只用于调试，正常验收不要加。探针通过后再提交 2×8，避免用 16 卡排查目录拼错、MLflow 断连或历史产物文件名错误。

---

## 9. 可选：2×8 卡小样本 smoke

正式长任务前建议用 128 样本执行一次真实 forward/backward、NCCL 和 checkpoint。32 CPU / 128GiB 仅用于 smoke，因为它用于链路验收而不是吞吐调优；正式 2×8 必须从每个 Worker 64 CPU / 256GiB 起步。

在 checkout 根目录创建临时 `.spk-rayjob.yaml`：

```yaml
name: bevfusion-fz-smoke-2x8
engine: ray-ddp
image: harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553
entrypoint: >-
  raytrain-bevfusion-prepare python3 tools/westwell_train.py
  configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml
  --launcher pytorch
  --run-dir "$PLATFORM_OUTPUT_PATH"
  dataset_root="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/"
  eval_dataset_root="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/"
  data.samples_per_gpu=1
  data.workers_per_gpu=1
  runner.max_epochs=1
  log_config.interval=1
  evaluation.interval=1
workers: 2
gpusPerWorker: 8
cpuPerWorker: 32
memoryPerWorker: 128Gi
executionMode: ray_train
input:
  space: public
  path: bevfusion/fz-3dod-v1
output:
  path: bevfusion/smoke-2x8
```

提交：

```bash
spk-rayjob submit --watch
```

S1H 分支需要在 entrypoint 中额外覆盖：

```text
data.val.ann_file="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/final_merged_nuscenes_infos_val.pkl"
data.test.ann_file="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/final_merged_nuscenes_infos_val.pkl"
```

通过标准：两个 Worker 分布在不同节点，日志出现 NCCL、6/6 iteration、loss、验证、checkpoint，拓扑文件中 `returnCodes=[0,0]`。

---

## 10. 提交全量 2×8 卡训练

下面是当前 180C / 780GiB GPU 节点的推荐生产模板。每个 8 卡 Worker 使用 64 CPU、256GiB；两台节点总共申请 128 CPU、512GiB。

本节只对 `bev_3dod` 完成了全量验收。`bev_3dod_s1h` 已验证范围是 smoke-128；S1H 全量训练不能直接复用下面模板，必须先完成第 5.7 节的类别配置和数值稳定性验收。

在 `bev_3dod` checkout 根目录创建或替换 `.spk-rayjob.yaml`：

```yaml
name: bevfusion-fz-full-2x8
engine: ray-ddp
image: harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553
entrypoint: >-
  raytrain-bevfusion-prepare python3 tools/westwell_train.py
  configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml
  --launcher pytorch
  --run-dir "$PLATFORM_OUTPUT_PATH"
  dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/"
  eval_dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/"
  data.train.dataset.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_train.pkl"
  data.val.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl"
  data.test.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl"
  data.samples_per_gpu=1
  data.workers_per_gpu=1
  optimizer.lr=1.0e-5
  runner.max_epochs=1
  log_config.interval=20
  evaluation.interval=1
workers: 2
gpusPerWorker: 8
cpuPerWorker: 64
memoryPerWorker: 256Gi
executionMode: ray_train
input:
  space: public
  path: ""
output:
  path: bevfusion/fz-full-2x8
```

提交当前代码：

```bash
spk-rayjob submit --watch
```

`.spk-rayjob.yaml` 只是当前项目的默认参数，不是平台必需文件：

- 可以提交到算法仓库，让团队复用同一资源和入口；
- 每次执行仍会重新打包当前代码；
- 命令行参数优先于模板；
- 同一个 `name` 可以重复提交，每次都会产生新的平台任务 ID；
- 平台会给输出目录附加任务 ID，不覆盖上一次结果。

### 10.1 不使用模板的一条完整命令

```bash
IMAGE='harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553'

spk-rayjob submit \
  --engine ray-ddp \
  --name bevfusion-fz-full-2x8 \
  --image "$IMAGE" \
  --entrypoint 'raytrain-bevfusion-prepare python3 tools/westwell_train.py configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml --launcher pytorch --run-dir "$PLATFORM_OUTPUT_PATH" dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/" eval_dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/" data.train.dataset.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_train.pkl" data.val.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl" data.test.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl" data.samples_per_gpu=1 data.workers_per_gpu=1 optimizer.lr=1.0e-5 runner.max_epochs=1 log_config.interval=20 evaluation.interval=1' \
  --input-space public \
  --input-path '' \
  --output-path bevfusion/fz-full-2x8 \
  --workers 2 \
  --gpus-per-worker 8 \
  --cpu-per-worker 64 \
  --memory-per-worker 256Gi \
  --execution-mode ray_train \
  --watch
```

entrypoint 整体使用单引号很重要。这样 `$PLATFORM_DATASET_PATH` 和 `$PLATFORM_OUTPUT_PATH` 才会在训练 Pod 内展开，而不是在提交机器上提前展开为空。

### 10.2 多 epoch 正式训练

先保持已验证的 1 epoch 命令跑通。确认 loss、mAP、吞吐、GPU 利用率和 checkpoint 后，再由算法负责人修改：

```text
runner.max_epochs=<正式 epoch>
optimizer.lr=<确认后的学习率>
evaluation.interval=<验证周期>
checkpoint_config.interval=<checkpoint 周期>
```

不要因为节点 CPU/内存变大就同时改变学习率、batch size、DataLoader workers 和 epoch；否则出现差异时无法归因。

---

## 11. 修改代码后如何再次提交

日常循环只有四步：

```bash
cd ~/training-src/bevfusion-bev_3dod

# 1. 修改 Python/YAML
git diff --check

# 2. 确认没有把数据、checkpoint 或关键源码错误打包/忽略
git status --short
git check-ignore -v mmdet3d/runner/__init__.py || true

# 3. 提交最新版 working tree
spk-rayjob submit --watch

# 4. 记录平台任务 ID、Git commit 和未提交差异
git rev-parse HEAD
```

不需要执行 Docker build。任务详情保存源码包摘要、镜像 digest、参数、输入、输出和提交人；即使 working tree 尚未 commit，也会按本次不可变源码包运行，但正式实验应把改动提交到 Git。

准确地说，`spk-rayjob` 提交的是当前目录的 working directory archive：

- 它包含当前磁盘上的已提交文件、已修改文件和未跟踪文件；
- 它与 Git commit 状态无关，不要求先 `git commit`；
- `.rayignore` 命中的文件不会进入 archive；
- `.gitignore` 不等于 `.rayignore`，但被 Git 忽略的源码也容易在改造/审计时漏掉；
- 上传完成后，平台按 archive 摘要运行本次不可变快照，随后继续修改本地文件不会改变已提交任务。

这让“改完立即提交”成为可能，也意味着本地未提交的实验代码会被一起带上集群。每次提交前至少执行：

```bash
git status --short
git diff --check
spk-rayjob package --dir . --output "/tmp/source-$(date +%s).zip"
```

确认工作区差异与实际 ZIP 后再提交；正式实验还应保存 Git commit、`git diff` 和平台记录的源码摘要，保证可追溯。

---

## 12. 网页提交同一份代码

适合第一次使用或希望可视化选择目录、镜像、资源的人：

1. 登录 `https://raytrain.wellspiking.ai`；
2. 在调试环境中用最终镜像打开 GPU Worker；
3. 在 `/workspace` 修改并验证代码；
4. 把工作区代码发布为不可变快照，或选择私有 Git 的固定 commit；
5. 创建训练任务；
6. 镜像选择本手册固定 digest；
7. 执行方式选择“多机多卡 Ray Train”；
8. Worker 数填 2，每 Worker GPU 填 8；
9. 每 Worker CPU 填 64，内存填 256GiB；
10. 输入从目录树选择“公共数据”根目录；
11. 输出选择个人运行结果子目录；
12. 命令填写第 10 节 `entrypoint` 的内容；
13. 提交后在任务详情查看排队、Pod、日志、GPU 指标和 Ray Dashboard。

网页提交与 `spk-rayjob` 最终创建同一类 RayJob 和任务专属 RayCluster。差别只是代码来自工作区快照/Git commit，而不是本机 working directory 包。

---

## 13. 原生 Ray CLI 提交（已有 Ray 自动化时使用）

日常推荐 `spk-rayjob`，因为它能让平台统一校验数据目录、输出隔离和断点续训。已有 Ray Jobs 自动化时可以使用原生入口。

安装与安全配置：

```bash
python3 -m pip install 'ray[default]==2.56.1'

export RAY_ADDRESS='https://raytrain.wellspiking.ai/ray'
read -rsp '平台个人访问令牌: ' RAYTRAIN_PAT
printf '\n'
export RAY_JOB_HEADERS="$(jq -cn --arg token "$RAYTRAIN_PAT" \
  '{Authorization:("Bearer "+$token)}')"
unset RAYTRAIN_PAT
```

在已经完成第 5 节改造的 checkout 根目录提交：

```bash
IMAGE='harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553'
RUN_ID="$(date +%Y%m%d-%H%M%S)"
NATIVE_OUTPUT="/mnt/storage/me/runs/bevfusion-native-${RUN_ID}"

META="$(jq -cn --arg image "$IMAGE" '{
  "ray-platform.image":$image,
  "ray-platform.worker-replicas":"2",
  "ray-platform.gpus-per-worker":"8",
  "ray-platform.cpu-per-worker":"64",
  "ray-platform.memory-per-worker":"256Gi",
  "ray-platform.queue":"local-gpu",
  "platform.training.engine":"ray-ddp"
}')"

ray job submit \
  --submission-id "bevfusion-native-${RUN_ID}" \
  --working-dir . \
  --runtime-env-json '{"excludes":[".git/","*.pkl","*.pth","mmdet3d/ops/"]}' \
  --metadata-json "$META" \
  -- \
  env PLATFORM_DATASET_PATH=/mnt/storage/public \
      PLATFORM_OUTPUT_PATH="$NATIVE_OUTPUT" \
  raytrain-bevfusion-prepare python3 tools/westwell_train.py \
  configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml \
  --launcher pytorch \
  --run-dir "$NATIVE_OUTPUT" \
  dataset_root=/mnt/storage/public/0429_pkl/fz/ \
  eval_dataset_root=/mnt/storage/public/0429_pkl/fz/ \
  data.train.dataset.ann_file=/mnt/storage/public/0429_pkl/fz/merged_nuscenes_infos_train.pkl \
  data.val.ann_file=/mnt/storage/public/0429_pkl/fz/merged_nuscenes_infos_val.pkl \
  data.test.ann_file=/mnt/storage/public/0429_pkl/fz/merged_nuscenes_infos_val.pkl \
  data.samples_per_gpu=1 data.workers_per_gpu=1 \
  optimizer.lr=1.0e-5 runner.max_epochs=1 \
  log_config.interval=20 evaluation.interval=1
```

原生入口没有 `spk-rayjob` 的逻辑目录参数，因此这里明确使用平台稳定容器路径；它们不是节点路径，也不会暴露对象存储凭据。令牌只保存在当前 shell 环境，不能写入仓库或脚本。任务会同时出现在 Web Portal，提交人是该 PAT 对应的平台用户。原生 Ray API 与 `spk-rayjob` 都持久化为 `submissionOrigin=ray-cli`，原生入口另以 `externalSubmissionId` 和接入方式识别；Portal 交互会话持久化为 `submissionOrigin=portal`。

任务提交完成后清理当前 shell 中的认证头：

```bash
unset RAY_JOB_HEADERS
```

---

## 14. 状态、日志、GPU 和 Ray Dashboard

常用命令：

```bash
spk-rayjob jobs --state RUNNING
spk-rayjob status <平台任务ID>
spk-rayjob status --output json <平台任务ID>
spk-rayjob logs -f <平台任务ID>
spk-rayjob logs --limit 3000 <平台任务ID>
spk-rayjob cancel <平台任务ID>
```

当前 CLI 的选项必须全部放在任务 ID 前面。因此使用 `status --output json <ID>`，不能写成 `status <ID> --output json`；如果使用独立登录文件，完整写法是：

```bash
AUTH_CONFIG="$HOME/.config/spk-rayjob/config.json"
spk-rayjob status --config "$AUTH_CONFIG" --output json <平台任务ID>
spk-rayjob logs --config "$AUTH_CONFIG" --limit 3000 <平台任务ID>
```

当前 CLI 不支持 `--job-id`。`spk-rayjob status <ID>` 本身可用，但 ID 后不能再放 `--config`、`--output` 或其他选项；同理，`--limit` 必须放在日志任务 ID 前面。违反顺序时会统一返回 `requires a job ID`，这个错误不代表任务 ID 不存在。

自动化脚本读取 `.observedState`，不是 `.state` 或 `.status`：

```bash
JOB_ID=<平台任务ID>
spk-rayjob status --output json "$JOB_ID" | jq -r ".observedState"
```

如果当前客户端的 `status` 命令仍因参数顺序或版本差异不可用，可以从任务列表按 ID 读取同一状态：

```bash
JOB_ID=<平台任务ID>
spk-rayjob jobs --output json \
  | jq -r --arg id "$JOB_ID" '.items[] | select(.id == $id) | .observedState'
```

正常终态为 `SUCCEEDED / FAILED / CANCELED / TIMED_OUT`。`SUBMITTED`、`VALIDATING`、`QUEUED`、`ADMITTED`、`PROVISIONING` 和 `RUNNING` 都不是终态。

历史日志接口当前按时间正序返回生命周期窗口中的前 N 行；`--limit 3000` 不是“倒序取最后 3000 行”。运行中优先使用 `logs -f`，结束后在 Portal 日志页按日志流查看和检索。任务仍在 `PROVISIONING` 且提交器/Worker 尚未创建时，日志为空是正常现象。

下面是当前 2×8 验收环境的实测时间，不是 SLA。镜像缓存、Kueue 排队、节点状态和 CSI 挂载都会改变时长：

| 阶段 | 常见实测 | 何时开始排查 |
| --- | ---: | --- |
| 1 卡探针从提交到运行 | 数十秒到数分钟 | 超过 5 分钟仍无 Pod，检查队列、配额和挂载事件。 |
| 2×8 `PROVISIONING` | 7～9 分钟 | 超过 15 分钟仍无两个 Ready Worker，检查 RayCluster、镜像、CSI 和节点资源。 |
| smoke-128 总时长 | 约 10 分钟 | Worker Ready 后长期没有 iteration，检查最早的 Worker 日志。 |
| 全量 1 epoch | 约 13～15 分钟 | 以 iteration/data_time 为准，不能只看墙钟。 |

`QUEUED` 没有固定上限：团队配额被其他任务占用时，等多久取决于前序任务何时释放资源。

### 14.1 日志接口故障时如何保住 traceback

正常顺序始终是：`logs -f` → Portal 日志流 → 任务状态兜底。当 Loki/logs API 暂时不可用时，先不要反复提交 16 卡任务：

```bash
JOB_ID=<失败任务ID>
spk-rayjob status --output json "$JOB_ID" \
  | jq -r '.statusReason, .statusMessage'
```

KubeRay 会把部分失败信息写入任务的 `statusMessage`。当前环境曾观察到其中内嵌约最后 20k 字符，可能包含完整 Python traceback；但这不是稳定的日志归档接口，也不保证包含完整 traceback，长度和内容可能随 KubeRay/Ray 版本改变。

实际排障中，显式参数式提交曾在 `statusMessage` 中保留较完整 traceback，而使用 `.spk-rayjob.yaml` 默认值的失败任务只留下 torchrun 摘要。这不是“参数式提交”和“模板提交”的设计差异，两者最终创建同一类任务；不能依赖这个偶发现象。为了提高失败时的可诊断性：

1. 先用第 8.1 节的 1 卡探针复现；
2. 在入口最外层不要吞 Python 异常，保留原始 traceback 和非零退出码；
3. 调试时可给探针加 `--fail-after-print`，验证 `statusMessage` 兜底；
4. 保存 `status --output json` 的结果并记录任务 ID；
5. logs API 恢复后，再以 Loki 按时间正序的完整日志为准。

当前 CLI 还没有产物列表子命令。任务产物应在 Portal“我的数据 → 我的运行结果”或任务详情的产物页查看；不要用 kubeconfig，也不需要再提交辅助训练任务来列目录。平台策略允许页面预览受支持的文本产物，但不开放任意下载。

任务通常有四个日志流：

| 日志流 | 含义 | 主要检查 |
| --- | --- | --- |
| 任务提交器 | 把入口提交给 Ray | 源码准备、命令、退出码。 |
| Ray Head | Ray 控制与调度 | Worker 注册、GCS、Dashboard。 |
| Worker 0 | 第一台 8 卡节点 | NCCL、rank、loss、OOM、数据读取。 |
| Worker 1 | 第二台 8 卡节点 | 同上，并确认跨节点。 |

2 Worker × 8 GPU 不是 16 个 Worker 日志流。每个 Worker Pod 内运行 8 个本地 rank，用户代码应在日志中打印 rank/local_rank 方便定位。

任务详情出现 RayCluster 后，可点击“Ray Dashboard”：

- 查看 Ray node、task、actor、placement group 和资源；
- Dashboard 只在任务 RayCluster 存活时可用；
- RayCluster 清理后，历史状态看平台任务详情，历史日志看 Loki，GPU/CPU 指标看 Prometheus/Grafana，产物看个人运行结果。

训练 Worker 日志应包含：

```text
NCCL 初始化
epoch / iteration
learning rate
loss 分项和总 loss
iteration time / data time
checkpoint 路径
验证 mAP / NDS
```

`worldSize` 和进程返回码不保证打印在训练 stdout。应在个人运行结果中的 `raytrain-topology.json`，或平台任务状态/拓扑区域确认：

```text
mode=ray_train
workers=2
gpusPerWorker=8
worldSize=16
两个不同节点
returnCodes=[0,0]
```

---

## 15. Checkpoint 与断点续训

BEVFusion 必须把 checkpoint 写入 `$PLATFORM_OUTPUT_PATH`。正常结果应包含：

```text
configs.yaml
epoch_1.pth
latest.pth
raytrain-topology.json
```

续训会创建一个新任务，把旧任务结果只读挂载为 `$PLATFORM_CHECKPOINT_PATH`，新结果仍写新的 `$PLATFORM_OUTPUT_PATH`：

```bash
spk-rayjob submit \
  --engine ray-ddp \
  --resume-from-job <上一次的平台任务ID> \
  --entrypoint 'raytrain-bevfusion-prepare python3 tools/westwell_train.py configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml --launcher pytorch --run-dir "$PLATFORM_OUTPUT_PATH" resume_from="$PLATFORM_CHECKPOINT_PATH/epoch_1.pth" dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/" eval_dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/" data.train.dataset.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_train.pkl" data.val.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl" data.test.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl"' \
  --watch
```

普通“重试”只是重新执行，不是断点续训。只有同时满足下面两点才算续训：

1. 新任务选择了旧任务 ID；
2. 训练入口显式读取 `$PLATFORM_CHECKPOINT_PATH` 中的 checkpoint。

正式交付前至少做一次“训练 → 生成 checkpoint → 新任务 resume → iteration/epoch 连续”的恢复验收。

---

## 16. 数据读取性能怎么验收

CPU 和内存配置是否合适，要以训练和数据基准共同判断。至少记录：

- 数据目录递归读取吞吐；
- 大文件顺序读取吞吐；
- 小文件打开/读取速率；
- 训练 `data_time`、`iter_time`、samples/s；
- 两个 Worker 的 GPU 利用率和显存；
- CPU、内存、网络和缓存命中情况。

推荐对比：

1. smoke 对照：`32 CPU / 128GiB`、`workers_per_gpu=1`；
2. `64 CPU / 256GiB`、`workers_per_gpu=1`；
3. `64 CPU / 256GiB`、`workers_per_gpu=2`；
4. 必要时再测 `96 CPU / 384GiB`、`workers_per_gpu=4`。

如果增加 CPU 后 `data_time`、samples/s 和 GPU 利用率没有改善，就保留更小规格，把资源留给并发任务。完整基准方法见[数据路径与读取性能基准](DATA_IO_BENCHMARK.md)。

---

## 17. 常见故障

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `No module named mmdet3d.runner` | 原 `.gitignore` 的 `run*/` 把源码排除了 | 应用运行时补丁，确认已改为 `/run*/`。 |
| `No module named platform_paths` | `.rayignore` 的 `datasets/`（包括前导 `/` 写法）把 `mmdet3d/datasets/` 整体排除了 | 删除 `data`、`datasets`、`work_dirs` 这些宽泛规则，并按 6.2 检查实际 ZIP 中存在 `mmdet3d/datasets/platform_paths.py`。 |
| `No module named mmdet3d.ops` | working-dir 中没有镜像提供的编译扩展，或入口没有执行准备器 | 保留精确规则 `mmdet3d/ops/`，并确保 entrypoint 以 `raytrain-bevfusion-prepare` 开头。 |
| `distutils.version` 或 TensorBoard 报错 | 旧 PyTorch/MMCV 与 TensorBoard/setuptools 组合不兼容 | 应用完整 `configure_platform_output()` 补丁，不要只改 local rank。 |
| smoke pkl 不存在 | smoke 任务错误选择了公共根，或把全量的 `merged_*` 当成 smoke 的 `final_merged_*` | 按 7.1 选择 `bevfusion/fz-3dod-v1`，并使用 `final_merged_nuscenes_infos_*.pkl`。 |
| pkl 能读但样本文件缺失 | pkl 保存了旧机器绝对路径，或公共数据不完整 | 先跑第 8 节预检，检查 `missing=0`。 |
| 一直 Pending | GPU、CPU、内存或团队 Kueue 配额不足 | 先看任务排队原因；不要给单个 Worker 填 180C/780GiB。 |
| GPU 利用率低 | 数据加载慢、worker 数过少或 I/O 小文件瓶颈 | 比较 `data_time`，逐步提高 `workers_per_gpu`，再考虑 CPU。 |
| `CUDA out of memory` | batch、模型或输入过大 | 降低 `samples_per_gpu`，使用梯度累积，不要只增加 Pod 内存。 |
| NCCL timeout/broken pipe | 某个 rank 先因 Python/数据/OOM 退出，其他 rank 连带失败 | 先找最早的 Worker traceback，不要只看最后一条 NCCL 错误。 |
| MLflow 404/5xx 后其他 rank 报 NCCL 超时 | rank 0 先因实验记录服务失败退出，其他 rank 连带失败 | 保留最早 traceback，联系平台管理员检查 ingest `/healthz` 和 Tracking API；不要在用户代码中改写平台地址。 |
| S1H 全量出现 index out of bounds 或 NaN | 全量 IGV 类别超出历史 9 类 Head，或 fp16/学习率数值不稳定 | 先按 5.7 核对类别数；变更 Head 后不得加载旧 9 类 checkpoint，再依次验证 fp32、降学习率、梯度裁剪、dynamic loss scale 和 warmup。 |
| `status` 报 `requires a job ID` | 选项放在 ID 后，或使用了不支持的 `--job-id` | 所有选项放在 ID 前；仍有问题时用 `jobs --output json` 按 ID 过滤。 |
| `invalid spk-rayjob config` | 把任务 YAML 传给了认证参数 `--config` | 用 `login --config <JSON>` 生成认证文件；任务参数放 checkout 根目录的 `.spk-rayjob.yaml`。 |
| logs API 暂时不可用 | Loki、网关或网络链路故障 | 先读取失败任务的 `statusMessage` 兜底并用 1 卡探针复现；恢复后以 Loki 完整日志为准。 |
| 日志末尾 loss 看不到 | 启动时模型结构过长 | 用 `logs -f` 或 `spk-rayjob logs --limit 3000 <ID>`。 |
| Dashboard 打不开 | Head 未就绪，或任务集群已按 TTL 清理 | 运行中重试；结束后使用平台历史日志与指标。 |
| 普通重试从头开始 | 没有显式选择 checkpoint | 使用 `--resume-from-job` 并在入口读取 `PLATFORM_CHECKPOINT_PATH`。 |
| 修改 Python 后是否重建镜像 | 不需要 | 直接再次运行 `spk-rayjob submit`。 |

---

## 18. 最终验收清单

### 代码

- [ ] 从目标 Git 分支全新 clone；
- [ ] 记录原始 commit；
- [ ] 路径 resolver、DDP、rank-0 输出和日志补丁已应用；
- [ ] `platform_mlflow.py` 已接入，只有全局 rank 0 创建并结束实验；
- [ ] S1H 额外配置已应用；
- [ ] `.gitignore` 不再排除 `mmdet3d/runner`；
- [ ] `.rayignore` 不含 `data/`、`datasets/`、`work_dirs/` 宽泛规则；
- [ ] 实际源码 ZIP 包含 `platform_paths.py`、`platform_mlflow.py` 和 `mmdet3d/runner`；
- [ ] 源码 ZIP 不包含数据、checkpoint 或凭据；
- [ ] 路径解析自测、`py_compile`、`git diff --check` 通过；
- [ ] 平台改造已提交到算法 Git 分支。

### 数据

- [ ] 输入选择 `public`，全量根路径留空；
- [ ] 训练代码读取 `$PLATFORM_DATASET_PATH`；
- [ ] 预检 train 1,024、val 512 抽样 `missing=0`；
- [ ] 训练代码不使用 TOS URI、AK/SK 或节点本地绝对路径。

### 资源与分布式

- [ ] 正式任务为 2 Worker × 8 GPU；
- [ ] 每 Worker 从 64 CPU / 256GiB 开始；
- [ ] 两个 Worker 位于不同 GPU 节点；
- [ ] `worldSize=16`、NCCL 初始化成功；
- [ ] Kueue 团队配额至少覆盖任务总资源。

### 训练与结果

- [ ] loss 为有限值且趋势符合算法预期；
- [ ] 两个 Worker 返回码均为 0；
- [ ] `$PLATFORM_OUTPUT_PATH` 中有 configs、checkpoint 和拓扑文件；
- [ ] 平台可查看创建、排队、开始、结束和运行时长；
- [ ] Loki 日志、GPU 指标和运行期 Ray Dashboard 可用；
- [ ] 实验中心显示本任务的 `FINISHED` run、参数、Loss 和验证指标；
- [ ] 已完成一次从历史 checkpoint 创建新任务的 resume 验收。

达到以上条件后，用户的日常操作可以收敛为：

```text
git pull / 修改代码
→ 本地静态检查
→ spk-rayjob submit --watch
→ 平台看日志、Loss、GPU、Dashboard
→ 在个人运行结果查看 Checkpoint
→ 修改参数或从历史任务续训
```

相关文档：

- [用户使用手册](USER_GUIDE.md)
- [三种提交方式](SUBMIT_GUIDE.md)
- [BEVFusion 逐文件代码改造](BEVFUSION_CODE_CHANGES.md)
- [BEVFusion 已验证任务与结果](BEVFUSION_RUNBOOK.md)
- [新训练代码接入](NEW_TRAINING_CODE_GUIDE.md)
- [数据路径与读取性能基准](DATA_IO_BENCHMARK.md)
