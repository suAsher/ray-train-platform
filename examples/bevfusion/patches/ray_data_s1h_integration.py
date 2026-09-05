"""Idempotently wire the legacy S1H trainer to platform-managed Ray Data."""

from __future__ import annotations


NATIVE_STREAMING_HELPER = '''def _platform_uses_native_streaming():
    return (
        os.environ.get("PLATFORM_TRAINING_ENGINE") == "ray-train"
        and os.environ.get("PLATFORM_DATA_MODE") == "streaming"
    )


'''


TRAIN_DATALOADER_HELPER = NATIVE_STREAMING_HELPER + '''def _platform_build_train_dataloader(ds, cfg, distributed):
    """Keep legacy loaders unchanged unless native streaming is explicit."""
    def legacy_builder():
        return build_dataloader(
            ds,
            cfg.data.samples_per_gpu,
            cfg.data.workers_per_gpu,
            None,
            dist=distributed,
            seed=cfg.seed,
        )

    if not _platform_uses_native_streaming():
        return legacy_builder()

    from mmcv.parallel import collate
    from ray_data_s1h import build_bevfusion_train_dataloader

    return build_bevfusion_train_dataloader(
        data_mode="streaming",
        legacy_builder=legacy_builder,
        pipeline=ds.pipeline,
        collate_fn=collate,
        samples_per_gpu=int(cfg.data.samples_per_gpu),
        worker_sample_count=int(
            os.environ.get("RAYTRAIN_DATASET_WORKER_SAMPLES", "0")
        ),
        prefetch_batches=int(
            os.environ.get("RAYTRAIN_DATASET_PREFETCH_BATCHES", "2")
        ),
        shuffle_seed=int(
            cfg.seed if cfg.seed is not None
            else os.environ.get("RAYTRAIN_DATASET_SHUFFLE_SEED", "0")
        ),
        local_shuffle_buffer_size=max(
            int(cfg.data.samples_per_gpu),
            int(os.environ.get("RAYTRAIN_DATASET_LOCAL_SHUFFLE_BUFFER_SIZE", "1024")),
        ),
    )


def _platform_register_data_epoch_hook(runner):
    if not _platform_uses_native_streaming():
        return
    from mmcv.runner import Hook

    class _PlatformDataEpochHook(Hook):
        def before_train_epoch(self, runner):
            runner.data_loader.set_epoch(runner.epoch)

    runner.register_hook(_PlatformDataEpochHook(), priority="VERY_HIGH")


def _platform_scatter_device_ids(device_ids):
    """Use torch.device objects required by PyTorch 2.x copy streams."""
    return [
        torch.device("cuda", device_id)
        if isinstance(device_id, int) and device_id >= 0
        else device_id
        for device_id in device_ids
    ]


class _PlatformMMDistributedDataParallel(MMDistributedDataParallel):
    """Bridge legacy MMCV distributed helpers to current PyTorch."""

    def _sync_params(self):
        parent_sync = getattr(super(), "_sync_params", None)
        if parent_sync is not None:
            return parent_sync()
        return None

    def to_kwargs(self, inputs, kwargs, device_id):
        from mmcv.parallel.scatter_gather import scatter_kwargs

        device_ids = _platform_scatter_device_ids([device_id])
        return scatter_kwargs(inputs, kwargs, device_ids, dim=self.dim)

    def scatter(self, inputs, kwargs, device_ids):
        from mmcv.parallel.scatter_gather import scatter_kwargs

        normalized = _platform_scatter_device_ids(device_ids)
        return scatter_kwargs(inputs, kwargs, normalized, dim=self.dim)


def _platform_distributed_wrapper():
    if _platform_uses_native_streaming():
        return _PlatformMMDistributedDataParallel
    return MMDistributedDataParallel


'''


LEGACY_DATALOADER_BLOCK = '''    data_loaders = [
        build_dataloader(
            ds,
            cfg.data.samples_per_gpu,
            cfg.data.workers_per_gpu,
            None,
            dist=distributed,
            seed=cfg.seed,
        )
        for ds in dataset
    ]
'''


STREAMING_DATALOADER_BLOCK = '''    data_loaders = [
        _platform_build_train_dataloader(ds, cfg, distributed)
        for ds in dataset
    ]
'''


_EPOCH_HELPER_START = "def _platform_register_data_epoch_hook(runner):"
_EPOCH_HELPER_END = "def _platform_scatter_device_ids(device_ids):"
_SHUFFLE_ARGUMENTS = '''        shuffle_seed=int(
            cfg.seed if cfg.seed is not None
            else os.environ.get("RAYTRAIN_DATASET_SHUFFLE_SEED", "0")
        ),
        local_shuffle_buffer_size=max(
            int(cfg.data.samples_per_gpu),
            int(os.environ.get("RAYTRAIN_DATASET_LOCAL_SHUFFLE_BUFFER_SIZE", "1024")),
        ),
'''


def _upgrade_epoch_helper(source: str) -> str:
    """Upgrade the exact previous platform helper in reused source archives."""
    epoch_start = TRAIN_DATALOADER_HELPER.index(_EPOCH_HELPER_START)
    epoch_end = TRAIN_DATALOADER_HELPER.index(_EPOCH_HELPER_END)
    old_helper = (
        TRAIN_DATALOADER_HELPER[:epoch_start] + TRAIN_DATALOADER_HELPER[epoch_end:]
    ).replace(_SHUFFLE_ARGUMENTS, "", 1)
    source = _replace_once(
        source, old_helper, TRAIN_DATALOADER_HELPER, "streaming epoch helper upgrade"
    )
    return _replace_once(
        source, "    runner.run(",
        "    _platform_register_data_epoch_hook(runner)\n    runner.run(",
        "streaming epoch hook upgrade",
    )


LEGACY_DATASET_SELECTION = "    datasets = [build_dataset(cfg.data.train)]\n"
STREAMING_DATASET_SELECTION = '''    if _platform_uses_native_streaming():
        from ray_data_s1h import build_streaming_dataset_proxy

        datasets = [build_streaming_dataset_proxy(cfg.data.train)]
    else:
        datasets = [build_dataset(cfg.data.train)]
'''


def _replace_once(source: str, old: str, new: str, label: str) -> str:
    count = source.count(old)
    if count != 1:
        raise ValueError(
            f"unexpected BEVFusion layout for {label}: "
            f"expected one match, found {count}"
        )
    return source.replace(old, new, 1)


def patch_s1h_train_api(source: str) -> str:
    """Patch ``mmdet3d/apis/train.py`` without changing legacy data modes."""

    marker = "def _platform_build_train_dataloader(ds, cfg, distributed):"
    native_gate_present = "def _platform_uses_native_streaming():" in source
    streaming_block_present = STREAMING_DATALOADER_BLOCK in source
    ddp_wrapper_present = (
        "def _platform_distributed_wrapper():" in source
        and "model = _platform_distributed_wrapper()(" in source
    )
    if (
        marker in source
        and native_gate_present
        and streaming_block_present
        and ddp_wrapper_present
    ):
        if _EPOCH_HELPER_START in source:
            if "    _platform_register_data_epoch_hook(runner)\n    runner.run(" not in source:
                raise ValueError("partially applied S1H epoch hook patch")
            return source
        return _upgrade_epoch_helper(source)
    if (
        marker in source
        or native_gate_present
        or streaming_block_present
        or ddp_wrapper_present
    ):
        raise ValueError("partially applied S1H data loader patch")

    patched = source
    if "import os\n" not in patched:
        patched = _replace_once(
            patched,
            "import torch\n",
            "import os\nimport torch\n",
            "data loader os import",
        )
    patched = _replace_once(
        patched,
        "def train_model(\n",
        TRAIN_DATALOADER_HELPER + "def train_model(\n",
        "data loader helper insertion",
    )
    patched = _replace_once(
        patched,
        LEGACY_DATALOADER_BLOCK,
        STREAMING_DATALOADER_BLOCK,
        "data loader selection",
    )
    patched = _replace_once(
        patched,
        "    runner.run(",
        "    _platform_register_data_epoch_hook(runner)\n    runner.run(",
        "streaming epoch hook",
    )
    return _replace_once(
        patched,
        "    model = MMDistributedDataParallel(\n",
        "    model = _platform_distributed_wrapper()(\n",
        "MMCV distributed wrapper selection",
    )


def patch_s1h_training_entrypoint(source: str) -> str:
    """Patch ``tools/westwell_train.py`` to avoid legacy PKL in streaming."""

    proxy_present = "build_streaming_dataset_proxy" in source
    native_gate_present = "def _platform_uses_native_streaming():" in source
    validation_present = (
        "validate=not _platform_uses_native_streaming()," in source
    )
    if proxy_present and native_gate_present and validation_present:
        return source
    if proxy_present or native_gate_present or validation_present:
        raise ValueError("partially applied S1H dataset selection patch")

    patched = _replace_once(
        source,
        "def main():\n",
        NATIVE_STREAMING_HELPER + "def main():\n",
        "native streaming gate insertion",
    )
    patched = _replace_once(
        patched,
        LEGACY_DATASET_SELECTION,
        STREAMING_DATASET_SELECTION,
        "dataset selection",
    )
    return _replace_once(
        patched,
        "        validate=True,\n",
        "        validate=not _platform_uses_native_streaming(),\n",
        "streaming validation selection",
    )
