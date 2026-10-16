"""CPU-only build routing contracts; run on the existing builder."""
import os
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]


class BuildContract(unittest.TestCase):
    def build_plan(self, targets, buildx=True):
        return subprocess.run(
            ['bash', str(ROOT / 'build-image.sh')], cwd=ROOT,
            env={**os.environ, 'BUILD_TARGETS': targets, 'DRY_RUN': 'true',
                 'PUSH_IMAGE': 'false', 'USE_BUILDX': str(buildx).lower(),
                 'IMAGE_TAG': 'contract-test'}, capture_output=True, text=True)

    def test_explicit_target_builds_only_cuda126(self):
        for buildx in (True, False):
            result = self.build_plan('workspace-cuda126', buildx)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('images/cuda126-workspace/Dockerfile', result.stdout)
            self.assertIn('ray-workspace-cuda126:contract-test', result.stdout)
            self.assertEqual(result.stdout.count('--- Building '), 1)
            self.assertNotIn('--push', result.stdout)

    def test_all_preserves_existing_targets(self):
        result = self.build_plan('all')
        self.assertEqual(result.returncode, 0, result.stderr)
        targets = [line.removeprefix('--- Building ') for line in result.stdout.splitlines()
                   if line.startswith('--- Building ')]
        self.assertEqual(targets, ['backend', 'frontend', 'source-materializer',
                                  'test-training', 'dataset-publisher', 'idc-sync',
                                  'workspace', 'train-pytorch', 'pytorch-ray-ddp',
                                  'pytorch-ray-train', 'workspace-ray256',
                                  'bevfusion-runtime', 'tos-prefix-init', 'spk-rayjob'])

    def test_original_workspace_still_uses_legacy_stage(self):
        result = self.build_plan('workspace')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('--target workspace-legacy', result.stdout)
        self.assertNotIn('cuda126-workspace', result.stdout)


if __name__ == '__main__':
    unittest.main()
