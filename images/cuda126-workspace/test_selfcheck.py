"""Negative CPU contracts: accepting a CPU/cu121 wheel is never permitted."""
import types
import subprocess
import unittest

from selfcheck import check, EXPECTED, ENVIRONMENT


class SelfcheckContract(unittest.TestCase):
    def report(self, *, versions=None, cuda='12.6', nvcc='12.6',
               pip_returncode=0, python='3.10.12', prefix=ENVIRONMENT,
               missing_module=None, wrong_executable=None):
        packages = {**EXPECTED, **(versions or {})}

        def importer(name):
            if name == missing_module:
                raise ImportError('missing test dependency')
            return types.SimpleNamespace(version=types.SimpleNamespace(cuda=cuda))

        def run(command, **kwargs):
            return types.SimpleNamespace(
                returncode=pip_returncode if command[0] != 'nvcc' else 0,
                stdout=f'Cuda compilation tools, release {nvcc}, V{nvcc}.85')

        def which(name):
            if name == wrong_executable:
                return '/unexpected/' + name
            return {'python': ENVIRONMENT + '/bin/python',
                    'ray': '/opt/raytrain/environment-wrappers/ray',
                    'torchrun': '/opt/raytrain/environment-wrappers/torchrun',
                    'nvcc': '/usr/local/cuda/bin/nvcc',
                    'raytrain-launch': '/usr/local/bin/raytrain-launch',
                    'raytrain-managed': '/usr/local/bin/raytrain-managed'}[name]
        return check(packages.__getitem__, importer, run, which, python, prefix)

    def test_exact_stack_passes_without_claiming_gpu(self):
        result = self.report()
        self.assertTrue(result['cpuPassed'], result)
        self.assertEqual(result['versions']['torch'], '2.13.0+cu126')
        self.assertEqual(result['versions']['torchvision'], '0.28.0+cu126')
        self.assertEqual(result['gpuValidation'], 'not_run')
        self.assertEqual(result['multiNodeValidation'], 'not_run')

    def test_rejects_cpu_or_wrong_cuda_wheel(self):
        for version in ('2.13.0+cpu', '2.13.0+cu121', '2.13.0', '2.6.0+cu126'):
            result = self.report(versions={'torch': version})
            self.assertFalse(result['cpuPassed'], version)

    def test_rejects_old_nvcc_despite_new_wheel(self):
        result = self.report(nvcc='12.1')
        self.assertFalse(result['cpuPassed'])
        self.assertIn('CUDA nvcc toolkit 12.6 required', result['errors'])

    def test_rejects_wrong_runtime_or_interpreter(self):
        for options in ({'cuda': '12.1'}, {'python': '3.11.9'},
                        {'prefix': '/usr'}, {'wrong_executable': 'ray'},
                        {'wrong_executable': 'nvcc'}):
            self.assertFalse(self.report(**options)['cpuPassed'], options)

    def test_rejects_missing_import_and_broken_dependencies(self):
        self.assertFalse(self.report(missing_module='ray.train.torch')['cpuPassed'])
        self.assertFalse(self.report(pip_returncode=1)['cpuPassed'])

    def test_rejects_each_changed_platform_package(self):
        for name in EXPECTED:
            self.assertFalse(self.report(versions={name: '0.0.0'})['cpuPassed'], name)

    def test_version_lookup_failure_is_reported(self):
        def absent(name):
            raise LookupError('missing')
        result = check(version=absent, importer=lambda name: types.SimpleNamespace(
            version=types.SimpleNamespace(cuda='12.6')),
            run=lambda *args, **kwargs: types.SimpleNamespace(returncode=0, stdout='invalid nvcc'),
            which=lambda name: None, python_version='3.10.12', prefix=ENVIRONMENT)
        self.assertFalse(result['cpuPassed'])
        self.assertTrue(any('LookupError' in error for error in result['errors']))
        self.assertIn('CUDA nvcc toolkit 12.6 required', result['errors'])

    def test_command_timeout_and_missing_binary_fail_closed(self):
        for exception in (OSError('missing'), subprocess.TimeoutExpired('gate', 1)):
            def broken(*args, **kwargs):
                raise exception
            result = check(version=EXPECTED.__getitem__, importer=lambda name: types.SimpleNamespace(
                version=types.SimpleNamespace(cuda='12.6')), run=broken,
                which=lambda name: None, python_version='3.10.12', prefix=ENVIRONMENT)
            self.assertFalse(result['cpuPassed'])
            self.assertFalse(result['pipCheckPassed'])
            self.assertIn('nvcc could not complete', result['errors'])
            self.assertIn('pip check could not complete', result['errors'])


if __name__ == '__main__':
    unittest.main()
