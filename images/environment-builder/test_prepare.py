"""Run together with workspace tests in the builder's Python 3.10 container."""
import importlib.util
import pathlib
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
