"""Idempotent BEVFusion adaptation for the managed Ray Train engine."""

from __future__ import annotations


MANAGED_HOOK_HELPER = '''def configure_ray_train_managed_hook(cfg):
    """Enable the image-provided MMCV adapter only for managed Ray Train."""
    if os.environ.get("PLATFORM_TRAINING_ENGINE") != "ray-train":
        return cfg

    from raytrain_runtime.mmcv_hook import build_hook_config, build_restore_hook_config

    interval = int(cfg.log_config.get("interval", 10))
    restore = {
        **build_restore_hook_config(),
        # Resume state must be loaded before optimizer/LR/logger hooks consume it.
        "priority": "VERY_HIGH",
    }
    reporting = {
        **build_hook_config(
            interval=interval,
            checkpoint_every_epochs=int(
                os.environ.get("RAYTRAIN_CHECKPOINT_EVERY_EPOCHS", "1")
            ),
            keep_latest=int(os.environ.get("RAYTRAIN_CHECKPOINT_KEEP_LATEST", "3")),
            keep_best=int(os.environ.get("RAYTRAIN_CHECKPOINT_KEEP_BEST", "1")),
            best_metric=os.environ.get("RAYTRAIN_CHECKPOINT_BEST_METRIC", ""),
            best_mode=os.environ.get("RAYTRAIN_CHECKPOINT_BEST_MODE", "max"),
        ),
        # Evaluation hooks publish mAP/NDS at epoch end. Run after them so the
        # configured best metric is stable before a checkpoint is reported.
        "priority": "VERY_LOW",
    }
    existing = [
        dict(hook)
        for hook in cfg.get("custom_hooks", [])
        if hook.get("type")
        not in {"RayTrainManagedRestoreHook", "RayTrainManagedHook"}
    ]
    cfg.custom_hooks = [*existing, restore, reporting]
    return cfg


'''


def _replace_once(source: str, old: str, new: str, label: str) -> str:
    count = source.count(old)
    if count != 1:
        raise ValueError(
            f"unexpected westwell_train.py layout for {label}: "
            f"expected one match, found {count}"
        )
    return source.replace(old, new, 1)


def patch_managed_training_entrypoint(source: str) -> str:
    """Add one custom Hook and avoid a duplicate distributed process group."""

    helper_present = "def configure_ray_train_managed_hook(cfg):" in source
    guarded = any(
        marker in source
        for marker in (
            "if distributed and not torch.distributed.is_initialized():",
            "if not torch.distributed.is_initialized():\n"
            "            init_dist(args.launcher, backend='nccl')",
        )
    )
    if helper_present and guarded:
        return source
    if helper_present != guarded:
        raise ValueError("partially applied managed Ray Train patch")

    patched = _replace_once(
        source,
        "def main():\n",
        MANAGED_HOOK_HELPER + "def main():\n",
        "managed hook insertion",
    )
    patched = _replace_once(
        patched,
        "    cfg = Config(recursive_eval(configs), filename=args.config)\n",
        "    cfg = Config(recursive_eval(configs), filename=args.config)\n"
        "    cfg = configure_ray_train_managed_hook(cfg)\n",
        "managed hook configuration",
    )
    standard_initialization = (
        "    if distributed:\n"
        "        init_dist(args.launcher, **cfg.dist_params)\n"
    )
    s1h_initialization = "        init_dist(args.launcher, backend='nccl')\n"
    standard_count = patched.count(standard_initialization)
    s1h_count = patched.count(s1h_initialization)
    if (standard_count, s1h_count) == (1, 0):
        return patched.replace(
            standard_initialization,
            "    if distributed and not torch.distributed.is_initialized():\n"
            "        init_dist(args.launcher, **cfg.dist_params)\n",
            1,
        )
    if (standard_count, s1h_count) == (0, 1):
        return patched.replace(
            s1h_initialization,
            "        if not torch.distributed.is_initialized():\n"
            "            init_dist(args.launcher, backend='nccl')\n",
            1,
        )
    raise ValueError(
        "unexpected westwell_train.py layout for distributed initialization: "
        f"expected one supported match, found {standard_count + s1h_count}"
    )
