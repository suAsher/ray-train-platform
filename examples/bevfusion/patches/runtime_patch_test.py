"""Tests for the BEVFusion runtime compatibility source transform."""

from pathlib import Path
import sys
import unittest


sys.path.insert(0, str(Path(__file__).resolve().parent))

from runtime_patch import patch_gitignore, patch_training_entrypoint  # noqa: E402


SOURCE = '''import argparse
import os


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--diff-seed', action='store_true')
    args, opts = parser.parse_known_args()
    if 'LOCAL_RANK' not in os.environ:
        os.environ['LOCAL_RANK'] = str(args.local_rank)

    cfg = Config(recursive_eval(configs), filename=args.config)

    # dump config
    cfg.dump(os.path.join(cfg.run_dir, "configs.yaml"))

    log_file = os.path.join(cfg.run_dir, f"{timestamp}.log")
    logger = get_root_logger(log_file=log_file)

    train_model(
        model,
        datasets,
        cfg,
        distributed=True,
        validate=True,
    )
'''


class RuntimePatchTest(unittest.TestCase):
    def setUp(self):
        self.patched = patch_training_entrypoint(SOURCE)

    def test_adds_torchrun_local_rank_argument(self):
        self.assertIn("'--local-rank', '--local_rank'", self.patched)

    def test_only_global_rank_zero_dumps_the_config(self):
        self.assertIn('if int(os.environ.get("RANK", "0")) == 0:', self.patched)
        self.assertEqual(self.patched.count('cfg.dump(os.path.join(cfg.run_dir, "configs.yaml"))'), 1)

    def test_disables_append_only_loggers_only_on_the_platform(self):
        self.assertIn("def configure_platform_output(cfg):", self.patched)
        self.assertIn('if not os.environ.get("PLATFORM_OUTPUT_PATH"):', self.patched)
        self.assertIn('hook.get("type") != "TensorboardLoggerHook"', self.patched)
        self.assertIn('TextLoggerHook._dump_log = raytrain_noop_json_log', self.patched)
        self.assertIn(
            'Config(copy.deepcopy(cfg._cfg_dict), filename=cfg.filename)',
            self.patched,
        )
        self.assertIn(
            'log_file = (None if os.environ.get("PLATFORM_OUTPUT_PATH") else',
            self.patched,
        )

    def test_preserves_the_log_interval_selected_by_the_training_config(self):
        self.assertNotIn("platform_cfg.log_config.interval = 1", self.patched)

    def test_uses_the_actual_distributed_mode(self):
        self.assertIn("distributed=distributed,", self.patched)
        self.assertNotIn("distributed=True,", self.patched)

    def test_is_idempotent_and_syntactically_valid(self):
        self.assertEqual(patch_training_entrypoint(self.patched), self.patched)
        compile(self.patched, "westwell_train.py", "exec")

    def test_upgrades_the_previous_configdict_deepcopy_bug(self):
        legacy = self.patched.replace(
            "Config(copy.deepcopy(cfg._cfg_dict), filename=cfg.filename)",
            "copy.deepcopy(cfg)",
        )
        self.assertEqual(patch_training_entrypoint(legacy), self.patched)


class GitIgnorePatchTest(unittest.TestCase):
    def test_anchors_the_legacy_run_pattern_at_the_repository_root(self):
        original = "*.log\nrun*/\nresults/\n"
        patched = patch_gitignore(original)

        self.assertEqual(patched, "*.log\n/run*/\nresults/\n")

    def test_is_idempotent(self):
        patched = "*.log\n/run*/\nresults/\n"

        self.assertEqual(patch_gitignore(patched), patched)

    def test_rejects_an_unexpected_ignore_file(self):
        with self.assertRaisesRegex(ValueError, "run directory pattern"):
            patch_gitignore("*.log\nresults/\n")


if __name__ == "__main__":
    unittest.main(verbosity=2)
