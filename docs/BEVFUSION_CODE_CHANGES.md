# BEVFusion 代码改造说明

交付文档。说明**为什么改、改了哪里、怎么复现、其他代码怎么照做**。

改动对象：`bev_3dod`（`0c1dc9d`）与 `bev_3dod_s1h`（`7931cee`）两个分支。

---

## 1. 为什么要改

`*_infos_*.pkl` 索引里存的是**生成索引那台机器上的绝对路径**：

```python
info['lidar_path']
# '/mnt/storage/public/bevfusion/fz-3dod-v1/raw/cnfzhjyg/cnfzhjyg/<uuid>/samples/LIDAR_TOP/1755401392.199735.bin'

info['cams']['CAM_FRONT_MID']['data_path']
# '/mnt/storage/public/bevfusion/fz-3dod-v1/raw/.../samples/CAM_FRONT_MID/1755401392.162986.jpg'
```

这条路径只在生成它的那台机器上成立。数据一旦换挂载点、换数据空间、换集群，**每个样本都会 FileNotFoundError**，而且不是提交时报错，是训练跑起来几分钟后才炸。

同一份索引在不同环境下需要不同的前缀，靠改数据或改配置都治不了根本——所以改的是**读取逻辑**。

---

## 2. 改了哪里

完整改造不只包含数据路径：

- `mmdet3d/datasets/platform_paths.py` 与 `nuscenes_dataset.py`：重定位数据路径；
- `tools/westwell_train.py`：DDP local rank、rank-0 输出、对象存储日志兼容与 MLflow 生命周期；
- `mmdet3d/utils/platform_mlflow.py`：只由全局 rank 0 上报参数和标量指标；
- `.gitignore`、`.rayignore`：保证必要源码进入 working-dir，同时排除数据和 checkpoint；
- `tools/platform_data_preflight.py`：提交 16 卡任务前验证索引和原始文件；
- S1H 配置：移除机器本地 checkpoint，并固定缺失的检测范围。

### 2.1 新增 `mmdet3d/datasets/platform_paths.py`

核心是 `DatasetPathResolver`：**丢掉索引里记录的前缀，保留路径尾部，重新挂到数据实际所在的目录上。**

实际挂载点由平台通过环境变量 `PLATFORM_DATASET_PATH` 提供。

关键设计——**前缀和一级命名空间重命名都自动发现，不需要写死 `fz → cnfzhjyg`**：

```python
def discover_drop_count(recorded_path, mounted_root):
    """从最长后缀开始试，找到第一个在磁盘上存在的，返回要丢弃的前缀段数。"""
    parts = [p for p in recorded_path.split("/") if p]
    for drop in range(len(parts)):
        if existing_path_within_root(
            os.path.join(mounted_root, *parts[drop:]), mounted_root
        ):
            return drop
    return None
```

- **从最长后缀开始试**：短尾巴（如 `LIDAR_TOP/x.bin`）可能在根目录下碰巧也存在，先匹配它会静默读错文件。必须让最具体的路径先赢。
- **允许发布目录改名**：如果普通后缀无法命中，再尝试公共根下的每个一级目录。例如 pkl 中的 `/temp_data/fz/<scene>` 可以定位到公共根的 `cnfzhjyg/<scene>`。
- **命中后缓存、缓存失配再发现**：同一前缀的样本复用第一次规则；后续样本不在该位置时才重新发现，因此一个 pkl 可以包含多个发布命名空间。
- **失败时不猜也不污染后续样本**：没有任何后缀能解析，就原样返回，保留原始报错信息，但不会永久禁用 resolver。
- **没有环境变量时完全惰性**：不设 `PLATFORM_DATASET_PATH` 就整个不生效，本地原有跑法不受影响。
- **限定在所选数据根内**：包含 `.`/`..` 的路径不会参与重定位；已经存在但位于 `PLATFORM_DATASET_PATH` 外的绝对路径也不会被直接信任。

### 2.2 修改 `mmdet3d/datasets/nuscenes_dataset.py`

`NuScenesDataset.get_data_info()` 里三处读路径的地方接上 resolver：

```diff
+from .platform_paths import DatasetPathResolver
+
 class NuScenesDataset(Custom3DDataset):

+    @property
+    def _platform_paths(self) -> DatasetPathResolver:
+        resolver = getattr(self, "_platform_paths_cache", None)
+        if resolver is None:
+            resolver = DatasetPathResolver()
+            self._platform_paths_cache = resolver
+        return resolver
+
     def get_data_info(self, index):
         data = dict(
-            lidar_path=info["lidar_path"],
-            sweeps=info["sweeps"],
+            lidar_path=self._platform_paths.resolve(info["lidar_path"]),
+            sweeps=self._platform_paths.resolve_sweeps(info["sweeps"]),
         )
         ...
-                data["image_paths"].append(camera_info["data_path"])
+                data["image_paths"].append(self._platform_paths.resolve(camera_info["data_path"]))
```

共 **20 行新增、3 行修改**，不碰模型、不碰训练循环、不碰配置。

用 `@property` 懒构造而不是写进 `__init__`，是因为两个分支的 `__init__` 签名不一致；这样改动被限制在一处，两个分支可以用同一个补丁。

---

## 3. 怎么在自己的 checkout 中复现

下面就是代码改造本身，不需要从平台构建机复制任何文件。修改完应提交到算法仓库，让后续 checkout 自带补丁。

### 3.1 新建 `mmdet3d/datasets/platform_paths.py`

```python
from __future__ import annotations

import os
from typing import Any, Dict, Iterable, List, Optional, Tuple

DATASET_ROOT_ENV = "PLATFORM_DATASET_PATH"


def safe_path_parts(recorded_path: str) -> Optional[List[str]]:
    parts = [part for part in recorded_path.split("/") if part]
    if any(part in (".", "..") for part in parts):
        return None
    return parts


def mounted_dataset_root() -> Optional[str]:
    root = os.environ.get(DATASET_ROOT_ENV, "").strip()
    return root or None


def existing_path_within_root(path: str, mounted_root: str) -> bool:
    try:
        root = os.path.realpath(mounted_root)
        candidate = os.path.realpath(path)
        return os.path.commonpath((root, candidate)) == root and os.path.exists(candidate)
    except (OSError, ValueError):
        return False


def discover_drop_count(recorded_path: str, mounted_root: str) -> Optional[int]:
    parts = safe_path_parts(recorded_path)
    if parts is None:
        return None
    for drop in range(len(parts)):
        if existing_path_within_root(
            os.path.join(mounted_root, *parts[drop:]), mounted_root
        ):
            return drop
    return None


def discover_path_rewrite(
    recorded_path: str, mounted_root: str
) -> Optional[Tuple[Tuple[str, ...], int]]:
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
    def __init__(self, mounted_root: Optional[str] = None) -> None:
        self._root = mounted_root if mounted_root is not None else mounted_dataset_root()
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
        candidate = os.path.join(self._root, *self._prefix, *parts[self._drop:])
        if existing_path_within_root(candidate, self._root):
            return candidate
        rewrite = discover_path_rewrite(path, self._root)
        if rewrite is None:
            return path
        self._prefix, self._drop = rewrite
        candidate = os.path.join(self._root, *self._prefix, *parts[self._drop:])
        return candidate if existing_path_within_root(candidate, self._root) else path

    def resolve_sweeps(self, sweeps: Iterable[Dict[str, Any]]) -> List[Dict[str, Any]]:
        resolved: List[Dict[str, Any]] = []
        for sweep in sweeps or []:
            if isinstance(sweep, dict) and "data_path" in sweep:
                sweep = dict(sweep)
                sweep["data_path"] = self.resolve(sweep["data_path"])
            resolved.append(sweep)
        return resolved
```

### 3.2 修改 `nuscenes_dataset.py`

按第 2.2 节的 diff 导入 `DatasetPathResolver`，增加缓存 property，并包装 lidar、sweeps 和 camera 的路径。修改后至少执行：

```bash
python3 -m py_compile \
  mmdet3d/datasets/platform_paths.py \
  mmdet3d/datasets/nuscenes_dataset.py \
  tools/westwell_train.py

python3 mmdet3d/datasets/platform_paths_test.py
```

### 3.3 修正代码包忽略规则

两个原始分支的 `.gitignore` 都有 `run*/`，它会错误排除 `mmdet3d/runner/`。改为只匹配根目录：

```diff
-run*/
+/run*/
```

必须验证下面命令没有输出：

```bash
git check-ignore -v mmdet3d/runner/__init__.py
```

### 3.4 运行时与 S1H 配置

`tools/westwell_train.py` 不能只按第 5 节的表格猜测修改。两个分支都必须加入下面的完整兼容函数（文件已有 `copy`、`os` 和 `Config` 导入；缺少时一并补上）：

```python
def configure_platform_output(cfg):
    """平台运行时使用 stdout/Loki，避免在 TOS/FSX 上追加日志。"""
    if not os.environ.get("PLATFORM_OUTPUT_PATH"):
        return cfg

    # MMCV 1.x 的 deepcopy(Config) 会退化为 ConfigDict，丢失 dump()。
    platform_cfg = Config(copy.deepcopy(cfg._cfg_dict), filename=cfg.filename)
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

创建 `Config` 后立即调用：

```diff
 cfg = Config(recursive_eval(configs), filename=args.config)
+cfg = configure_platform_output(cfg)
```

同时完成以下三项修改：

```diff
+parser.add_argument('--local-rank', '--local_rank', type=int, default=0)

-cfg.dump(os.path.join(cfg.run_dir, "configs.yaml"))
+if int(os.environ.get("RANK", "0")) == 0:
+    cfg.dump(os.path.join(cfg.run_dir, "configs.yaml"))

-log_file = os.path.join(cfg.run_dir, f"{timestamp}.log")
+log_file = (None if os.environ.get("PLATFORM_OUTPUT_PATH") else
+            os.path.join(cfg.run_dir, f"{timestamp}.log"))

-distributed=True,
+distributed=distributed,
```

这段逻辑只在存在 `PLATFORM_OUTPUT_PATH` 时生效，本地原有 TensorBoard/文件日志行为不变。它同时规避旧版 PyTorch 与新版 setuptools 的 `distutils.version` 兼容错误；平台训练的 loss、lr、mAP 等仍由 stdout 输出并进入 Loki。

`bev_3dod_s1h` 还要修改：

```diff
-load_from: <历史机器上的 checkpoint>
+load_from: null

-post_center_range: ${post_center_range}
+post_center_range: [-61.2, -61.2, -10, 61.2, 61.2, 10]
```

最后用 `git diff --check` 和 `git status --short` 检查差异，把 `.gitignore`、`platform_paths.py`、`nuscenes_dataset.py`、`westwell_train.py` 及 S1H 配置一起提交。

### 实测验证结果

用 `fz-0429-platform-smoke-128` 数据集在集群内实测：

| 划分 | 样本 | 点云命中 | 相机命中 |
| --- | --- | --- | --- |
| train | 128 | 128 / 0 缺失 | 640 / 0 缺失 |
| val | 32 | 32 / 0 缺失 | 160 / 0 缺失 |

2026-08-21 对公共根中的 FZ 合并标注再次验证：

| 标注 | 样本数 | 抽查路径 | 缺失 |
| --- | ---: | ---: | ---: |
| `0429_pkl/fz/merged_nuscenes_infos_train.pkl` | 15,228 | 1,024 | 0 |
| `0429_pkl/fz/merged_nuscenes_infos_val.pkl` | 1,620 | 512 | 0 |

这组 pkl 记录的是 `/temp_data/fz/<scene>/...`，实际发布目录是 `$PLATFORM_DATASET_PATH/cnfzhjyg/<scene>/...`。旧版“只删除前缀”规则抽查 1,536 个路径全部失败；增加一级命名空间自动发现后，同一批路径全部命中。回归测试与可直接加入算法仓库的预检脚本分别位于：

- [`examples/bevfusion/patches/platform_paths_test.py`](../examples/bevfusion/patches/platform_paths_test.py)
- [`examples/bevfusion/platform_data_preflight.py`](../examples/bevfusion/platform_data_preflight.py)

重定位示例：
```
/mnt/storage/public/bevfusion/fz-3dod-v1/raw/.../LIDAR_TOP/1755401392.199735.bin
  ↓
/data/bevfusion/fz-3dod-v1/raw/.../LIDAR_TOP/1755401392.199735.bin
```

---

## 4. 其他代码怎么照做

这套办法适用于**任何把绝对路径写进索引/标注文件的训练代码**，不限于 BEVFusion。

三步：

1. **把 `platform_paths.py` 复制进你的项目**（无第三方依赖，纯标准库）
2. **在读取样本路径的地方接上 resolver**：
   ```python
   from platform_paths import DatasetPathResolver
   resolver = DatasetPathResolver()          # 读 PLATFORM_DATASET_PATH
   real_path = resolver.resolve(recorded_path)
   ```
3. **提交时选中对应的数据集**，平台会把 `PLATFORM_DATASET_PATH` 指向它

如果你的代码本来就用相对路径 + 可配置的 `data_root`，那**不需要这个补丁**——直接把 `data_root` 指向 `$PLATFORM_DATASET_PATH` 即可：

```bash
python tools/train.py cfg.yaml --data-root "$PLATFORM_DATASET_PATH"
```

---

## 5. 已应用的运行时与 S1H 修复

除了路径解析，真实 2 节点训练还暴露了以下问题。这些差异必须正式提交到算法分支，不应只保留在某台调试机的工作目录中。

| 问题 | 修复 | 原因 |
| --- | --- | --- |
| `LOCAL_RANK` 未设置时读取不存在的参数 | 增加 `--local-rank` / `--local_rank`，默认 `0` | 同时兼容直接 Python 与不同版本的 `torchrun` |
| `train_model(..., distributed=True)` 写死 | 改为 `distributed=distributed` | 单卡和 DDP 按实际 launcher 运行 |
| 16 个 rank 同时写 `configs.yaml` | 仅 `RANK=0` 写配置 | TOS 文件系统不支持并发创建同一对象 |
| MMCV/TensorBoard 追加日志失败或 `distutils.version` 崩溃 | 使用第 3.4 节完整 `configure_platform_output()`，平台任务只写 stdout；Loki 采集 stdout | TOS 不适合 POSIX append，旧 PyTorch/TensorBoard 组合还有 setuptools 兼容问题 |
| `deepcopy(Config)` 丢失 `dump()` | 重新包装为 `mmcv.Config` | MMCV 1.x 的兼容性问题 |
| `.gitignore` 中的 `run*/` | 改为 `/run*/` | 防止 working-dir 丢失 `mmdet3d/runner` |
| S1H 写死历史 checkpoint | `load_from: null` | checkpoint 必须由任务显式选择，不能依赖旧机器路径 |
| S1H `${post_center_range}` 未定义 | 固定为 `[-61.2, -61.2, -10, 61.2, 61.2, 10]` | 与当前 S1H 检测范围匹配并消除配置解析失败 |

S1H 的 smoke 标注集只有 `final_merged_nuscenes_infos_val.pkl`，因此验收模板还显式覆盖 `data.val.ann_file` 与 `data.test.ann_file`。这只是 smoke 数据适配；正式训练应使用数据版本自身的 train/val/test 索引。当前全量数据包含 IGV 第 10 类，而历史 S1H Head 只有 9 类；调整 Head 会改变模型结构并使旧 9 类 checkpoint 不兼容，且历史全量复跑还出现过 fp16/Hungarian cost/loss NaN。因此 S1H 全量不能按 smoke 通过来宣称可交付，必须单独完成类别和收敛验收。

兼容镜像中的 `raytrain-bevfusion-prepare` 只把工作目录缺少的 `.so` / `.py` 文件从固定镜像环境补齐，并通过目录锁保证同一代码包被 16 个进程并发启动时只准备一次。它不会覆盖用户随任务上传的源码。

---

## 6. 下次任务怎么提交

改完代码直接提交即可，**代码随任务上传，不需要重新构建镜像**：

```bash
cd ~/src/bevfusion
spk-rayjob submit \
  --entrypoint 'raytrain-bevfusion-prepare python3 tools/westwell_train.py configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml --launcher pytorch --run-dir "$PLATFORM_OUTPUT_PATH" dataset_root="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/" eval_dataset_root="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/" data.samples_per_gpu=1 data.workers_per_gpu=1 runner.max_epochs=1 log_config.interval=1 evaluation.interval=1' \
  --workers 2 --gpus-per-worker 8 \
  --execution-mode ray_train \
  --input-space public --input-path bevfusion/fz-3dod-v1 \
  --watch
```

上面命令不依赖 `.spk-rayjob.yaml`。需要项目默认模板时，[多方式提交手册](SUBMIT_GUIDE.md) 已给出完整 YAML，可直接在自己的 checkout 中创建。

---

## 7. 2026-08-22 fresh-clone 验收记录

本表只记录使用 `guofeng.su` 平台身份、从原始 GitLab commit 新拉取并应用本文补丁后的任务。历史 smoke 只作为故障定位证据，不代替这六项复验。

| 分支 | 提交入口 | 资源 | 当前状态 | 平台任务 ID | 结果 / checkpoint |
| --- | --- | --- | --- | --- | --- |
| `bev_3dod@0c1dc9d` | `spk-rayjob` | 2×8 | `SUCCEEDED` | `job-0d368c1c99570c6cfd30a704` | 6/6 iteration、验证、checkpoint、MLflow |
| `bev_3dod@0c1dc9d` | 原生 Ray `--working-dir` | 2×8 | `SUCCEEDED` | `job-5cd8dd759fa2a481b5a0178e` | 6/6 iteration、验证、MLflow |
| `bev_3dod@0c1dc9d` | Portal 工作区快照 | 2×8 | `SUCCEEDED` | `job-12a730d45f0f83e4b4ae37c5` | 6/6 iteration、checkpoint、MLflow |
| `bev_3dod_s1h@7931cee` | `spk-rayjob` | 2×8 | `SUCCEEDED` | `job-ec9a64a66c953efd346fefe8` | smoke-128，6/6 iteration、验证、checkpoint、MLflow |
| `bev_3dod_s1h@7931cee` | 原生 Ray `--working-dir` | 2×8 | `SUCCEEDED` | `job-42b55a5447b7e93afe29bb87` | smoke-128，6/6 iteration、验证、MLflow |
| `bev_3dod_s1h@7931cee` | Portal 工作区快照 | 2×8 | `SUCCEEDED` | `job-69993074d2ff5de4f2d30b17` | smoke-128，6/6 iteration、checkpoint、MLflow |

每项都以 `guofeng.su` 提交，自动创建独立 RayCluster；RayJob 清单为 `replicas: 2`、每 Worker `nvidia.com/gpu: 8`，两个 Worker 分布在两台 GPU 节点。日志出现 NCCL 2.10.3、loss、验证与 `/mnt/data/output` checkpoint；任务终态及开始/结束时间完整。128 样本 smoke 的 `mAP=0` 和部分 `grad_norm=nan` 是算法/小样本质量信号，不影响平台链路验收，但正式全量训练必须继续做收敛与断点恢复验收。令牌、预签名 URL和内部凭据不写入本表。

六项矩阵完成后，运行时锁改为原子 hard-link，并增加 PID 复用、旧目录锁、损坏锁和多等待者并发恢复。r8 先通过代表性 `2×8` 回归任务 `job-4c883346704ed25eba1b6e3f`；最终 r9 digest `sha256:cbc23478ca97290428bae1e1a3dba49776fa4e7f4ada851c173223278fd49e47` 再以 `job-56fbc0b27e9a5b5bc4491023` 通过真实 `2×8` 回归。r9 日志含 NCCL 2.10.3、6/6 iteration、checkpoint 与验证；两个 8-GPU Worker 分布在两台节点，个人结果目录生成 `epoch_1.pth`（33,332,845 字节）。

2026-08-22 在 r9 兼容层上加入 Python 3.8 可用的 `mlflow-skinny==2.17.2`，并把 `protobuf` 固定为 `3.20.1`，避免破坏 ONNX 1.12。当前生产 digest 为 `sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553`：构建阶段同时导入 MLflow 与 ONNX，随后通过 1-GPU MLflow Tracking 任务和 2×8 BEVFusion smoke；后者包含 NCCL 2.10.3、6/6 iteration 和 33,332,845 字节 checkpoint。
