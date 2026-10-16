"""Run together with workspace tests in the builder's Python 3.10 container."""
import importlib.util
import pathlib
import json
import sys
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).parent
sys.path.insert(0, str(HERE.parent / 'environment-workspace'))
import environment_runtime as runtime
spec = importlib.util.spec_from_file_location('prepare', HERE / 'prepare.py')
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


class PrepareTest(unittest.TestCase):
    def test_materials_report_records_profile_versions_only_when_available(self):
        versions = {'python': '3.10', 'pytorch': '2.13.0+cu126', 'cuda': '12.6', 'ray': '2.58.0'}
        for labels in [versions, {}]:
            with self.subTest(profile=bool(labels)), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                helper = root / 'helpers'
                helper.mkdir()
                for name in ('environment_runtime.py', 'raytrain-environment', 'ray', 'torchrun', 'environment-shell.sh'):
                    (helper / name).write_text('trusted helper')
                capture = root / 'capture.json'
                capture.write_text('{}')
                manifest = {'pythonVersion': '.'.join(map(str, sys.version_info[:3]))}
                with mock.patch.object(prepare, 'RUNTIME_ROOT', helper), mock.patch.object(prepare, 'read_manifest', return_value=manifest), mock.patch.object(prepare, 'download_wheels', return_value=[]), mock.patch.object(prepare, 'runtime_versions', return_value=labels):
                    report = prepare.prepare(capture, root / 'context', 'https://mirror/simple')
                self.assertEqual(report['baseImage'], prepare.BASE_IMAGE)
                self.assertEqual(report.get('runtimeVersions', {}), labels)
                self.assertEqual(json.loads((root / 'context/materials.json').read_text()), report)
                self.assertEqual((root / 'context/capture.json').read_bytes(), capture.read_bytes())
                if not labels:
                    self.assertNotIn('runtimeVersions', report)

    def test_materials_failure_removes_only_fresh_operation_context(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / 'retain').write_text('unrelated')
            manifest = {'pythonVersion': '.'.join(map(str, sys.version_info[:3]))}
            with mock.patch.object(prepare, 'read_manifest', return_value=manifest), mock.patch.object(prepare, 'download_wheels', side_effect=runtime.CaptureError('wheel mismatch', 'PACKAGE_MODIFIED')):
                with self.assertRaises(runtime.CaptureError):
                    prepare.prepare(root / 'capture.json', root / 'context', 'https://mirror/simple')
            self.assertFalse((root / 'context').exists())
            self.assertEqual((root / 'retain').read_text(), 'unrelated')
    def test_pip_environment_does_not_forward_credentials_or_extra_index(self):
        with mock.patch.dict('os.environ', {'PIP_EXTRA_INDEX_URL': 'https://secret@example.invalid',
                                          'HARBOR_PASSWORD': 'test-secret', 'AWS_ACCESS_KEY_ID': 'secret'}, clear=True):
            env = prepare.pip_environment()
        self.assertNotIn('PIP_EXTRA_INDEX_URL', env)
        self.assertNotIn('HARBOR_PASSWORD', env)
        self.assertNotIn('AWS_ACCESS_KEY_ID', env)
        self.assertEqual(env['PIP_CONFIG_FILE'], '/dev/null')

    def test_materializer_rejects_embedded_credentials_and_insecure_index(self):
        for index in ['http://mirror/simple', 'https://user:secret@mirror/simple',
                      'https://mirror/simple?token=secret', 'file:///tmp/wheels']:
            with tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(runtime.CaptureError):
                    prepare.download_wheels({'packages': []}, pathlib.Path(directory), index)

    def test_failed_download_does_not_expose_subprocess_output(self):
        with tempfile.TemporaryDirectory() as directory:
            package = {'name': 'example', 'version': '1.0', 'filesHash': 'a' * 64}
            with mock.patch.object(prepare.subprocess, 'run', return_value=mock.Mock(returncode=1)) as run:
                with self.assertRaisesRegex(runtime.CaptureError, 'Matching wheel is unavailable'):
                    prepare.download_wheels({'packages': [package]}, pathlib.Path(directory), 'https://mirror/simple')
            command = run.call_args.args[0]
            self.assertIn('--only-binary=:all:', command)
            self.assertIn('--no-deps', command)
            self.assertEqual(run.call_args.kwargs['stdout'], prepare.subprocess.DEVNULL)
            self.assertEqual(run.call_args.kwargs['stderr'], prepare.subprocess.DEVNULL)

    def test_empty_dependency_set_is_valid_and_has_empty_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            materials = prepare.download_wheels({'packages': []}, root, 'https://mirror/simple')
            self.assertEqual(materials, [])
            self.assertEqual((root / 'requirements.lock').read_text(), '\n')


if __name__ == '__main__':
    unittest.main()
