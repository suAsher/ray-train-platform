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
    )


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
    if marker in source and native_gate_present and streaming_block_present:
        return source
    if marker in source or native_gate_present or streaming_block_present:
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
    return _replace_once(
        patched,
        LEGACY_DATALOADER_BLOCK,
        STREAMING_DATALOADER_BLOCK,
        "data loader selection",
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
