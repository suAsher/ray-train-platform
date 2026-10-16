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
                 'IMAGE_TAG': 'contract-test',
                 'CUDA126_BASE_IMAGE': 'harbor.example/base@sha256:' + 'a' * 64},
            capture_output=True, text=True)

    def test_explicit_base_is_editor_free(self):
        result = self.build_plan('cuda126-base')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('images/cuda126-workspace/Dockerfile.base', result.stdout)
        self.assertIn('raytrain-cuda126-base:contract-test', result.stdout)
        self.assertEqual(result.stdout.count('--- Building '), 1)
        dockerfile = (ROOT / 'images/cuda126-workspace/Dockerfile.base').read_text()
        self.assertNotIn('code-server', dockerfile)
        self.assertNotIn('jupyterlab', dockerfile)
        self.assertNotIn('NVIDIA_DISABLE_REQUIRE', dockerfile)
        self.assertNotIn('NVIDIA_REQUIRE_CUDA=', dockerfile)

    def test_workspace_rejects_mutable_base(self):
        for base in ('', 'harbor.example/base:latest'):
            result = subprocess.run(
                ['bash', str(ROOT / 'build-image.sh')], cwd=ROOT,
                env={**os.environ, 'BUILD_TARGETS': 'workspace-cuda126',
                     'DRY_RUN': 'true', 'CUDA126_BASE_IMAGE': base},
                capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('immutable sha256 digest', result.stderr)

    def test_prepare_uses_matching_foundation_and_interpreter(self):
        for buildx in (True, False):
            result = self.build_plan('environment-prepare-cuda126', buildx)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.count('--- Building '), 1)
            self.assertIn('ENVIRONMENT_BASE_IMAGE=harbor.example/base@sha256:' + 'a' * 64,
                          result.stdout)
            self.assertIn('ENVIRONMENT_BASE_PYTHON=/usr/bin/python3.10', result.stdout)
        legacy = self.build_plan('environment-prepare')
        self.assertEqual(legacy.returncode, 0, legacy.stderr)
        self.assertNotIn('ENVIRONMENT_BASE_IMAGE=', legacy.stdout)
        self.assertNotIn('ENVIRONMENT_BASE_PYTHON=', legacy.stdout)

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
