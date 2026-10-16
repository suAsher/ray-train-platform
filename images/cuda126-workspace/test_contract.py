"""CPU-only build routing contracts; run on the existing builder."""
import os
import json
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

    def test_official_cuda_wheels_precede_mirrored_dependency_resolution(self):
        dockerfile = (ROOT / 'images/cuda126-workspace/Dockerfile.base').read_text()
        commands = dockerfile.replace('\\\n', ' ').split('\nRUN ')[1:]
        wheel_stage = next(command for command in commands
                           if '--index-url "${TORCH_INDEX_URL}"' in command)
        self.assertIn('--no-deps', wheel_stage)
        self.assertIn('torch==2.13.0+cu126 torchvision==0.28.0+cu126', wheel_stage)
        self.assertNotIn('${PIP_INDEX_URL}', wheel_stage)
        dependency_stage = next(command for command in commands
                                if '--index-url "${PIP_INDEX_URL}"' in command
                                and 'torch==2.13.0+cu126' in command)
        self.assertNotIn('--no-deps', dependency_stage)
        self.assertIn('torchvision==0.28.0+cu126', dependency_stage)
        self.assertIn('python3.10 -m pip check', dependency_stage)

    def test_missing_fixed_packages_use_hash_checked_readonly_wheel_stage(self):
        folder = ROOT / 'images/cuda126-workspace'
        dockerfile = (folder / 'Dockerfile.base').read_text()
        self.assertIn('FROM scratch AS official-wheels', dockerfile)
        sources = json.loads((folder / 'official-wheels.json').read_text())
        requirements = (folder / 'requirements-official.txt').read_text()
        constraints = (folder / 'constraints.txt').read_text()
        security = folder / 'security-constraints.txt'
        if security.exists():
            constraints += security.read_text()
        self.assertEqual(len(sources), 25)
        bootstrap = {'pip': '26.2.1', 'setuptools': '84.0.0', 'wheel': '0.48.0'}
        for wheel in sources:
            pin = f"{wheel['name']}=={wheel['version']}"
            if wheel['name'] in bootstrap:
                self.assertEqual(wheel['version'], bootstrap[wheel['name']])
            else:
                self.assertIn(pin, constraints)
            self.assertIn(f"{pin} --hash=sha256:{wheel['sha256']}", requirements)
            self.assertRegex(wheel['sha256'], r'^[0-9a-f]{64}$')
            self.assertTrue(wheel['url'].startswith('https://files.pythonhosted.org/packages/'))
            self.assertIn(f"ADD --checksum=sha256:{wheel['sha256']} {wheel['url']} /wheels/{wheel['filename']}", dockerfile)
        self.assertIn('--mount=from=official-wheels,source=/wheels,target=/wheels,readonly', dockerfile)
        self.assertIn('--no-index --find-links /wheels --no-deps --require-hashes', dockerfile)
        self.assertNotIn('COPY --from=official-wheels', dockerfile)
        self.assertNotIn('--extra-index-url', dockerfile)

    def test_editor_uses_security_fixed_packages(self):
        dockerfile = (ROOT / 'images/cuda126-workspace/Dockerfile').read_text()
        self.assertIn('jupyterlab==4.5.11 jupyter_server==2.21.0', dockerfile)
        self.assertNotIn('code-server:4.93.1', dockerfile)
        self.assertIn('code-server@sha256:', dockerfile)
        smoke = (ROOT / 'images/cuda126-workspace/editor_smoke.py').read_text()
        self.assertIn("version.startswith('4.99.4 '", smoke)

    def test_cuda126_editor_arg_cannot_inherit_legacy_default(self):
        fixed = 'harbor.wellspiking.ai/hub/codercom/code-server@sha256:80a8e4e472be2feb0f90724b564ddf81ff38fdc482aab4fb887ef53413891f95'
        for buildx in (True, False):
            cuda = self.build_plan('workspace-cuda126', buildx)
            self.assertIn('CODE_SERVER_IMAGE=' + fixed, cuda.stdout)
            legacy = self.build_plan('workspace', buildx)
            self.assertIn('CODE_SERVER_IMAGE=harbor.wellspiking.ai/hub/codercom/code-server:4.93.1', legacy.stdout)

    def test_security_constraints_follow_cached_torch_layer(self):
        folder = ROOT / 'images/cuda126-workspace'
        dockerfile = (folder / 'Dockerfile.base').read_text()
        marker = 'COPY images/cuda126-workspace/security-constraints.txt'
        self.assertIn(marker, dockerfile)
        self.assertLess(dockerfile.index('torch==2.13.0+cu126'), dockerfile.index(marker))
        self.assertIn('cat /opt/raytrain/security-constraints.txt >> /opt/raytrain/environment-constraints.txt', dockerfile)
        constraints = (folder / 'security-constraints.txt').read_text()
        for pin in ('anyio==4.14.2', 'cryptography==50.0.0', 'gitpython==3.1.60',
                    'multidict==6.9.1', 'pyasn1==0.6.4', 'sqlparse==0.6.0',
                    'tornado==6.5.9', 'urllib3==2.8.0', 'virtualenv==21.7.13',
                    'python-discovery==1.6.3'):
            self.assertIn(pin, constraints)

    def test_editor_inherits_safe_base_without_leaking_into_managed_python(self):
        folder = ROOT / 'images/cuda126-workspace'
        dockerfile = (folder / 'Dockerfile').read_text()
        self.assertIn('--copies --system-site-packages --without-pip /opt/raytrain/editor', dockerfile)
        self.assertIn('exec /opt/raytrain/editor/bin/python -m jupyter', dockerfile)
        self.assertIn('--constraint /opt/raytrain/environment-constraints.txt', dockerfile)
        self.assertIn('FROM scratch AS editor-wheels', dockerfile)
        sources = json.loads((folder / 'editor-official-wheels.json').read_text())
        self.assertEqual({w['name']: w['version'] for w in sources},
                         {'jupyterlab': '4.5.11', 'jupyter-server': '2.21.0'})
        for wheel in sources:
            self.assertIn(f"ADD --checksum=sha256:{wheel['sha256']} {wheel['url']} /wheels/{wheel['filename']}", dockerfile)
        self.assertIn("find_spec('jupyterlab') is None", (folder / 'editor_smoke.py').read_text())

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
            if buildx:
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
        self.assertNotIn('/images/cuda126-workspace/Dockerfile', result.stdout)


if __name__ == '__main__':
    unittest.main()
