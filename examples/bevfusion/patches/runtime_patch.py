"""Idempotent source transform for the legacy BEVFusion training entrypoint."""

from __future__ import annotations


RUNTIME_HELPER = '''def configure_platform_output(cfg):
    """Keep append-style logs off the TOS/FSX output mount.

    TOS accepts complete object writes such as checkpoints, but it is not a
    POSIX filesystem and rejects the append operations used by MMCV JSON and
    TensorBoard loggers.  Stdout remains the source of truth and is collected
    by Loki.  Local, non-platform execution keeps the branch's original
    logging behavior.
    """
    if not os.environ.get("PLATFORM_OUTPUT_PATH"):
        return cfg

    # MMCV 1.x implements deepcopy(Config) as ConfigDict, which silently
    # drops methods such as dump(). Re-wrap the copied mapping as Config.
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


'''


def _replace_once(source: str, old: str, new: str, description: str) -> str:
    count = source.count(old)
    if count != 1:
        raise ValueError(
            f"unexpected westwell_train.py layout for {description}: "
            f"expected one match, found {count}"
        )
    return source.replace(old, new, 1)


def patch_gitignore(source: str) -> str:
    """Keep root run directories ignored without excluding ``mmdet3d/runner``.

    Git ignore patterns without a leading slash match at every directory
    depth.  Both legacy BEVFusion branches contain ``run*/``, which therefore
    removes the tracked ``mmdet3d/runner`` package from source archives.  The
    anchored form preserves the intended root-level output exclusion.
    """
    lines = source.splitlines(keepends=True)
    legacy = [index for index, line in enumerate(lines) if line.rstrip("\r\n") == "run*/"]
    anchored = [index for index, line in enumerate(lines) if line.rstrip("\r\n") == "/run*/"]

    if not legacy and len(anchored) == 1:
        return source
    if len(legacy) != 1 or anchored:
        raise ValueError(
            "unexpected .gitignore layout for run directory pattern: "
            f"expected one run*/, found legacy={len(legacy)} anchored={len(anchored)}"
        )

    index = legacy[0]
    newline = lines[index][len(lines[index].rstrip("\r\n")) :]
    patched = [*lines]
    patched[index] = f"/run*/{newline}"
    return "".join(patched)


def patch_training_entrypoint(source: str) -> str:
    """Return a platform-safe version of ``tools/westwell_train.py``.

    The transform is deliberately narrow and fails closed when the upstream
    file changes. Re-running it against an already patched file is a no-op.
    """
    if "def configure_platform_output(cfg):" in source:
        legacy_copy = "    platform_cfg = copy.deepcopy(cfg)\n"
        fixed_copy = (
            "    platform_cfg = Config(copy.deepcopy(cfg._cfg_dict), "
            "filename=cfg.filename)\n"
        )
        if legacy_copy in source:
            source = _replace_once(
                source,
                legacy_copy,
                fixed_copy,
                "MMCV Config type preservation",
            )
        # Older revisions forced every platform run to print every iteration.
        # Preserve the value selected by the project config/CLI instead.
        source = source.replace("    platform_cfg.log_config.interval = 1\n", "", 1)
        return source

    source = _replace_once(
        source,
        "def main():\n",
        RUNTIME_HELPER + "def main():\n",
        "runtime helper insertion",
    )
    source = _replace_once(
        source,
        "    args, opts = parser.parse_known_args()\n",
        "    parser.add_argument('--local-rank', '--local_rank', "
        "type=int, default=0)\n"
        "    args, opts = parser.parse_known_args()\n",
        "torchrun local rank argument",
    )
    source = _replace_once(
        source,
        "    cfg = Config(recursive_eval(configs), filename=args.config)\n",
        "    cfg = Config(recursive_eval(configs), filename=args.config)\n"
        "    cfg = configure_platform_output(cfg)\n",
        "platform output configuration",
    )
    source = _replace_once(
        source,
        '    # dump config\n    cfg.dump(os.path.join(cfg.run_dir, "configs.yaml"))\n',
        '    # TOS objects cannot be concurrently created by every DDP rank.\n'
        '    if int(os.environ.get("RANK", "0")) == 0:\n'
        '        cfg.dump(os.path.join(cfg.run_dir, "configs.yaml"))\n',
        "rank-zero config dump",
    )
    source = _replace_once(
        source,
        '    log_file = os.path.join(cfg.run_dir, f"{timestamp}.log")\n',
        '    log_file = (None if os.environ.get("PLATFORM_OUTPUT_PATH") else\n'
        '                os.path.join(cfg.run_dir, f"{timestamp}.log"))\n',
        "stdout-only platform logger",
    )
    source = _replace_once(
        source,
        "        distributed=True,\n",
        "        distributed=distributed,\n",
        "distributed mode propagation",
    )
    return source
