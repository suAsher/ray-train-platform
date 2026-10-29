import importlib.util
import pathlib
import subprocess
import unittest
from unittest.mock import Mock

ROOT = pathlib.Path(__file__).resolve().parents[2]
HERE = pathlib.Path(__file__).resolve().parent


class RuntimeTests(unittest.TestCase):
    def checker(self):
        spec = importlib.util.spec_from_file_location('selfcheck', HERE / 'selfcheck.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_cpu_report_never_claims_gpu_validation(self):
        module = self.checker()
        versions = {'ray': '2.58.0', 'torch': '2.4.1+cu121', 'torchvision': '0.19.1', 'pyarrow': '25.0.1', 'mlflow-skinny': '3.14.0'}
        imported = Mock()
        imported.version.cuda = '12.1'
        imported.SITE_SELECTION_PROTOCOL = 1
        report = module.check(version=versions.__getitem__, importer=lambda _: imported,
                              run=lambda *a, **kw: Mock(returncode=0, stdout='ok', stderr=''),
                              executable=lambda _: True, python_version='3.10.15')
        self.assertTrue(report['cpu_passed'])
        self.assertEqual(report['gpu_validation'], 'not_run')
        self.assertEqual(report['versions']['cuda'], '12.1')

    def test_missing_dependency_and_pip_conflict_fail_closed(self):
        module = self.checker()
        def missing(_):
            raise ImportError('missing')
        report = module.check(version=missing, importer=missing,
                              run=lambda *a, **kw: Mock(returncode=1, stdout='conflict', stderr=''),
                              executable=lambda _: False, python_version='3.11.0')
        self.assertFalse(report['cpu_passed'])
        self.assertGreater(len(report['errors']), 5)
        self.assertEqual(report['gpu_validation'], 'not_run')

    def test_version_drift_and_timeout_fail_closed(self):
        module = self.checker()
        imported = Mock()
        imported.version.cuda = '12.4'
        imported.SITE_SELECTION_PROTOCOL = 0
        def timeout(*args, **kwargs):
            raise subprocess.TimeoutExpired('pip check', 120)
        report = module.check(version=lambda _: '0.0.0', importer=lambda _: imported,
                              run=timeout, executable=lambda _: True, python_version='3.10.15')
        self.assertFalse(report['cpu_passed'])
        self.assertFalse(report['pip_check_passed'])
        self.assertIn('PyTorch CUDA runtime 12.1 required', report['errors'])
        self.assertIn('Site selection protocol 1 required', report['errors'])

    def test_optional_images_require_operator_selected_base(self):
        for image, argument in [('raytrain-base', 'TRAINING_BASE_IMAGE'),
                                ('environment-builder', 'ENVIRONMENT_BASE_IMAGE'),
                                ('environment-workspace', 'ENVIRONMENT_BASE_IMAGE')]:
            with self.subTest(image=image):
                dockerfile = (ROOT / 'images' / image / 'Dockerfile').read_text()
                self.assertIn(f'ARG {argument}\n', dockerfile)
                self.assertNotIn(f'ARG {argument}=', dockerfile)
                self.assertIn('FROM ${' + argument + '}', dockerfile)
        writer = (ROOT / 'images/environment-workspace/write_profile.py').read_text()
        self.assertIn('os.geteuid() != 0', writer)
        self.assertIn('target.chmod(0o444)', writer)
        self.assertIn('environment_runtime.load_profile()', writer)
        for image in ['environment-builder', 'environment-workspace']:
            dockerfile = (ROOT / 'images' / image / 'Dockerfile').read_text()
            self.assertIn('USER root', dockerfile)
            self.assertIn('ENVIRONMENT_BASE_IMAGE="${ENVIRONMENT_BASE_IMAGE}"', dockerfile)
            self.assertIn('/usr/local/lib/raytrain-environment/write_profile.py', dockerfile)

    def test_code_free_templates(self):
        dockerfile = (HERE / 'Dockerfile').read_text()
        self.assertIn('ARG TRAINING_BASE_IMAGE\n', dockerfile)
        self.assertIn('FROM ${TRAINING_BASE_IMAGE}', dockerfile)
        self.assertNotIn('COPY examples/', dockerfile)
        self.assertNotIn('bevfusion', dockerfile.lower())
        self.assertIn('raytrain-selfcheck', dockerfile)
        derived = (HERE / 'example/Dockerfile').read_text()
        self.assertNotIn('COPY .', derived)
        self.assertIn('--constraint /opt/raytrain/constraints.txt', derived)
        self.assertEqual((HERE / 'example/.dockerignore').read_text().splitlines()[0], '**')


if __name__ == '__main__':
    unittest.main()
