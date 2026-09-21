"""Pure contract tests; run on the release builder, never the developer laptop."""
import hashlib
import importlib.util
import json
import pathlib
import tempfile
import unittest
import zipfile

HERE = pathlib.Path(__file__).parent
spec = importlib.util.spec_from_file_location('environment_runtime', HERE / 'environment_runtime.py')
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class RuntimeContractTest(unittest.TestCase):
    def manifest(self):
        return {'schemaVersion': 1, 'baseImage': runtime.BASE_IMAGE,
                'pythonVersion': '3.10.16', 'packages': [
                    {'name': 'demo-package', 'version': '1.2.3', 'filesHash': 'a' * 64}],
                'checks': {'baseUnchanged': True, 'managedOnly': True,
                           'installedFilesVerified': True, 'stableCapture': True}}

    def test_manifest_rejects_base_override_and_duplicate(self):
        value = self.manifest()
        runtime.validate_manifest(value)
        value['baseImage'] = 'attacker/other:latest'
        with self.assertRaises(runtime.CaptureError):
            runtime.validate_manifest(value)
        value = self.manifest()
        value['packages'].append(dict(value['packages'][0]))
        with self.assertRaises(runtime.CaptureError):
            runtime.validate_manifest(value)

    def test_manifest_rejects_shell_or_url_requirement(self):
        for name, version in [('x; touch bad', '1'), ('demo', 'file:///tmp/package'),
                              ('Demo_Package', '1.0'), ('../demo', '1')]:
            value = self.manifest()
            value['packages'][0].update(name=name, version=version)
            with self.assertRaises(runtime.CaptureError):
                runtime.validate_manifest(value)

    def test_manifest_rejects_unknown_fields_or_missing_checks(self):
        value = self.manifest()
        value['dockerfile'] = 'RUN stolen'
        with self.assertRaises(runtime.CaptureError):
            runtime.validate_manifest(value)
        value = self.manifest()
        value['checks']['stableCapture'] = False
        with self.assertRaises(runtime.CaptureError):
            runtime.validate_manifest(value)

    def test_base_change_and_unmanaged_install_rejected(self):
        baseline = {'base': {'version': '1', 'filesHash': 'a', 'managed': False}}
        for current in [{}, {'base': {'version': '2', 'filesHash': 'a', 'managed': False}},
                        {**baseline, 'extra': {'version': '1', 'filesHash': 'b', 'managed': False}}]:
            with self.assertRaises(runtime.CaptureError):
                runtime.package_delta(baseline, current)
        current = {**baseline, 'extra': {'version': '1', 'filesHash': 'b', 'managed': True}}
        self.assertEqual(runtime.package_delta(baseline, current), [
            {'name': 'extra', 'version': '1', 'filesHash': 'b'}])

    def test_content_hash_stable_and_sensitive(self):
        one = [('demo/__init__.py', hashlib.sha256(b'x=1').hexdigest()),
               ('demo-1.dist-info/METADATA', hashlib.sha256(b'Name: demo').hexdigest())]
        self.assertEqual(runtime.content_hash(one), runtime.content_hash(list(reversed(one))))
        self.assertNotEqual(runtime.content_hash(one), runtime.content_hash(one[:1]))

    def test_wheel_fingerprint_excludes_installer_generated_files(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'demo-1.0-py3-none-any.whl'
            with zipfile.ZipFile(path, 'w') as wheel:
                wheel.writestr('demo/__init__.py', 'VALUE = 1\n')
                wheel.writestr('demo-1.0.dist-info/METADATA', 'Name: demo\nVersion: 1.0\n')
                wheel.writestr('demo-1.0.dist-info/RECORD', 'generated')
            expected = runtime.content_hash([
                ('demo/__init__.py', hashlib.sha256(b'VALUE = 1\n').hexdigest()),
                ('demo-1.0.dist-info/METADATA', hashlib.sha256(b'Name: demo\nVersion: 1.0\n').hexdigest())])
            self.assertEqual(runtime.wheel_fingerprint(path), expected)

    def test_wheel_unsafe_paths_and_data_scripts_rejected(self):
        for entry in ['../escape', '/absolute', 'demo-1.data/scripts/executable',
                      'demo-1.data/data/secret', 'demo\\evil.py']:
            with tempfile.TemporaryDirectory() as directory:
                path = pathlib.Path(directory) / 'bad.whl'
                with zipfile.ZipFile(path, 'w') as wheel:
                    wheel.writestr(entry, 'bad')
                with self.assertRaises(runtime.CaptureError):
                    runtime.wheel_fingerprint(path)

    def test_wheel_purelib_mapping_matches_installed_layout(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'demo.whl'
            with zipfile.ZipFile(path, 'w') as wheel:
                wheel.writestr('demo-1.data/purelib/demo.py', 'hello')
            expected = runtime.content_hash([('demo.py', hashlib.sha256(b'hello').hexdigest())])
            self.assertEqual(runtime.wheel_fingerprint(path), expected)

    def test_installed_file_modification_is_detected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'module.py'
            path.write_bytes(b'original')
            import base64
            recorded = base64.urlsafe_b64encode(hashlib.sha256(b'original').digest()).rstrip(b'=').decode()
            self.assertEqual(runtime.checked_file_hash(path, 'sha256', recorded), hashlib.sha256(b'original').hexdigest())
            path.write_bytes(b'modified')
            with self.assertRaises(runtime.CaptureError):
                runtime.checked_file_hash(path, 'sha256', recorded)

    def test_symlinks_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            target = pathlib.Path(directory) / 'target'
            target.write_bytes(b'value')
            link = pathlib.Path(directory) / 'link'
            link.symlink_to(target)
            with self.assertRaises(runtime.CaptureError):
                runtime.checked_file_hash(link, '', '')


if __name__ == '__main__':
    unittest.main()
