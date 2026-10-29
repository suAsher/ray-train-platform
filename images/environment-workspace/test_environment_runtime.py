"""Pure contract tests; run on the release builder, never the developer laptop."""
import hashlib
import importlib.util
import json
import pathlib
import stat
import tempfile
import unittest
from unittest import mock
import zipfile

HERE = pathlib.Path(__file__).parent
spec = importlib.util.spec_from_file_location('environment_runtime', HERE / 'environment_runtime.py')
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class RuntimeContractTest(unittest.TestCase):
    def setUp(self):
        # Tests unrelated to profile validation run against an explicit,
        # trusted fixture instead of relying on a production-specific fallback.
        if self._testMethodName.startswith('test_profile') or self._testMethodName.startswith('test_trusted_profile'):
            return
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = pathlib.Path(directory.name) / 'profile.json'
        profile = self.profile()
        path.write_text(json.dumps(profile))
        for name, value in [('PROFILE', path), ('ACTIVE_PROFILE', profile), ('BASE_IMAGE', profile['baseImage']), ('PROFILE_ERROR', False)]:
            patcher = mock.patch.object(runtime, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        loader = mock.patch.object(runtime, 'load_profile', return_value=profile)
        loader.start()
        self.addCleanup(loader.stop)

    def test_profile_is_required_without_legacy_base(self):
        with mock.patch.object(runtime, 'ACTIVE_PROFILE', None), mock.patch.object(runtime, 'load_profile', return_value=None):
            with self.assertRaises(runtime.CaptureError):
                runtime.verify_active_profile()

    def test_profile_allows_explicit_registry_pinned_bases(self):
        for repository in ['ghcr.io/example/training', 'registry.example.com:5000/team/base', 'docker.io/library/python', 'team/base:v1']:
            profile = {**self.profile(), 'baseImage': repository + '@sha256:' + 'a' * 64}
            with tempfile.TemporaryDirectory() as directory:
                path = pathlib.Path(directory) / 'profile.json'
                path.write_text(json.dumps(profile))
                with mock.patch.object(runtime, 'PROFILE', path), mock.patch.object(runtime, 'verify_profile_permissions'):
                    self.assertEqual(runtime.load_profile(), profile)

    def test_profile_requires_trusted_file_and_every_parent(self):
        def node(mode, owner=0):
            value = mock.Mock()
            value.lstat.return_value = mock.Mock(st_mode=mode, st_uid=owner)
            return value
        profile = node(stat.S_IFREG | 0o444)
        profile.parents = [node(stat.S_IFDIR | 0o755) for _ in range(3)]
        with mock.patch.object(runtime, 'PROFILE', profile):
            runtime.verify_profile_permissions()
            for parent in profile.parents:
                parent.lstat.assert_called_once()
            for mode, owner in [(stat.S_IFDIR | 0o775, 0), (stat.S_IFDIR | 0o757, 0),
                                (stat.S_IFDIR | 0o755, 1000), (stat.S_IFLNK | 0o777, 0),
                                (stat.S_IFREG | 0o444, 0)]:
                with self.subTest(parentMode=mode, parentOwner=owner):
                    profile.parents[1] = node(mode, owner)
                    with self.assertRaises(runtime.CaptureError) as caught:
                        runtime.verify_profile_permissions()
                    self.assertEqual(caught.exception.code, 'PACKAGE_MODIFIED')
            profile.parents = [node(stat.S_IFDIR | 0o755)]
            for mode, owner in [(stat.S_IFREG | 0o444, 1000), (stat.S_IFREG | 0o644, 0),
                                (stat.S_IFLNK | 0o777, 0), (stat.S_IFDIR | 0o444, 0)]:
                profile.lstat.return_value = mock.Mock(st_mode=mode, st_uid=owner)
                with self.assertRaises(runtime.CaptureError):
                    runtime.verify_profile_permissions()

    def profile(self):
        return {'schemaVersion': 1, 'baseImage': 'registry.example.com/training/cuda126-base@sha256:' + 'b' * 64,
                'basePython': '/usr/bin/python3.10', 'versions': {'python': '3.10', 'pytorch': '2.13.0+cu126', 'cuda': '12.6', 'ray': '2.58.0'}}

    def test_trusted_profile_selects_pinned_base_without_environment_override(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'profile.json'
            path.write_text(json.dumps(self.profile()))
            path.chmod(0o444)
            with mock.patch.object(runtime, 'PROFILE', path), mock.patch.object(runtime, 'verify_profile_permissions'), mock.patch.dict('os.environ', {'RAYTRAIN_BASE_IMAGE': 'evil:latest'}):
                self.assertEqual(runtime.load_profile(), self.profile())

    def test_profile_rejects_unpinned_image_python_path_and_unknown_fields(self):
        for field, value in [('baseImage', 'evil:latest'), ('baseImage', 'registry.example.com/training/../other@sha256:' + 'a' * 64),
                             ('basePython', '/workspace/python'), ('schemaVersion', 2), ('secret', 'x')]:
            profile = self.profile()
            profile[field] = value
            with tempfile.TemporaryDirectory() as directory:
                path = pathlib.Path(directory) / 'profile.json'
                path.write_text(json.dumps(profile))
                with mock.patch.object(runtime, 'PROFILE', path), mock.patch.object(runtime, 'verify_profile_permissions'):
                    with self.assertRaises(runtime.CaptureError):
                        runtime.load_profile()

    def test_profile_rejects_writable_or_symlink_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'profile.json'
            path.write_text(json.dumps(self.profile()))
            with mock.patch.object(runtime, 'PROFILE', path):
                with self.assertRaises(runtime.CaptureError):
                    runtime.load_profile()
                link = pathlib.Path(directory) / 'profile-link.json'
                link.symlink_to(path)
                with mock.patch.object(runtime, 'PROFILE', link):
                    with self.assertRaises(runtime.CaptureError):
                        runtime.load_profile()

    def test_profile_malformed_json_uses_bounded_package_modified_error(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'profile.json'
            path.write_text('{invalid-profile')
            with mock.patch.object(runtime, 'PROFILE', path), mock.patch.object(runtime, 'verify_profile_permissions'):
                with self.assertRaises(runtime.CaptureError) as caught:
                    runtime.load_profile()
            self.assertEqual(runtime.safe_error(caught.exception), {'code': 'PACKAGE_MODIFIED'})

    def test_profile_size_and_version_metadata_are_bounded(self):
        malformed = self.profile()
        malformed['versions']['cuda'] = '12.6 private value'
        for raw in [' ' * 4097, json.dumps(malformed)]:
            with tempfile.TemporaryDirectory() as directory:
                path = pathlib.Path(directory) / 'profile.json'
                path.write_text(raw)
                with mock.patch.object(runtime, 'PROFILE', path), mock.patch.object(runtime, 'verify_profile_permissions'):
                    with self.assertRaises(runtime.CaptureError):
                        runtime.load_profile()

    def test_profile_change_after_bootstrap_is_rejected(self):
        profile = self.profile()
        with mock.patch.object(runtime, 'ACTIVE_PROFILE', profile), mock.patch.object(runtime, 'load_profile', return_value={**profile, 'baseImage': runtime.BASE_IMAGE}):
            with self.assertRaises(runtime.CaptureError):
                runtime.verify_active_profile()

    def test_profile_base_accepts_matching_manifest_and_rejects_legacy_base(self):
        profile = self.profile()
        legacy = runtime.BASE_IMAGE
        with mock.patch.object(runtime, 'ACTIVE_PROFILE', profile), mock.patch.object(runtime, 'load_profile', return_value=profile), mock.patch.object(runtime, 'BASE_IMAGE', profile['baseImage']):
            value = self.manifest()
            runtime.validate_manifest(value)
            self.assertEqual(runtime.runtime_versions(), profile['versions'])
            value['baseImage'] = legacy
            with self.assertRaises(runtime.CaptureError):
                runtime.validate_manifest(value)

    def test_profile_hash_is_part_of_bootstrap_and_change_is_rejected(self):
        profile = self.profile()
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            base, path, env = root / 'base-python', root / 'profile.json', root / 'environment'
            base.write_bytes(b'python-binary')
            path.write_text(json.dumps(profile))
            (env / 'bin').mkdir(parents=True)
            (env / 'pyvenv.cfg').write_text('include-system-site-packages = true')
            for name in ('python', 'python3', 'python3.10'):
                (env / 'bin' / name).write_bytes(base.read_bytes())
            with mock.patch.object(runtime, 'PROFILE', path), mock.patch.object(runtime, 'ACTIVE_PROFILE', profile), mock.patch.object(runtime, 'load_profile', return_value=profile), mock.patch.object(runtime, 'ENVIRONMENT', env), mock.patch.object(runtime, 'BASE_PYTHON', base):
                expected = runtime.bootstrap_inventory()
                self.assertIn(str(path), expected)
                path.write_text('{}')
                with self.assertRaises(runtime.CaptureError):
                    runtime.verify_bootstrap(expected)

    def manifest(self):
        return {'schemaVersion': 1, 'baseImage': runtime.BASE_IMAGE,
                'pythonVersion': '3.10.16', 'packages': [
                    {'name': 'demo-package', 'version': '1.2.3', 'filesHash': 'a' * 64}],
                'checks': {'baseUnchanged': True, 'managedOnly': True,
                           'installedFilesVerified': True, 'stableCapture': True}}

    def test_error_categories_never_include_exception_message(self):
        import errno
        import subprocess
        cases = [
            (runtime.CaptureError('https://user:secret@private', 'WHEEL_UNAVAILABLE'), 'WHEEL_UNAVAILABLE'),
            (runtime.CaptureError('private detail', 'PACKAGE_MODIFIED'), 'PACKAGE_MODIFIED'),
            (OSError(errno.ENOSPC, 'secret path'), 'TEMP_STORAGE_FULL'),
            (subprocess.TimeoutExpired('secret command', 1), 'BUILD_TIMEOUT'),
            (ValueError('secret value'), 'BUILD_FAILED'),
        ]
        for error, code in cases:
            self.assertEqual(runtime.safe_error(error), {'code': code})

    def test_python_binary_must_match_base_and_bootstrap_record(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            base = root / 'base-python'
            base.write_bytes(b'known-base-python')
            env = root / 'environment'
            (env / 'bin').mkdir(parents=True)
            (env / 'pyvenv.cfg').write_text('include-system-site-packages = true')
            for name in ('python', 'python3', 'python3.10'):
                (env / 'bin' / name).write_bytes(base.read_bytes())
            with mock.patch.object(runtime, 'ENVIRONMENT', env), mock.patch.object(runtime, 'BASE_PYTHON', base):
                expected = runtime.bootstrap_inventory()
                runtime.verify_bootstrap(expected)
                (env / 'bin' / 'python3').write_bytes(b'replaced-python')
                with self.assertRaises(runtime.CaptureError) as caught:
                    runtime.verify_bootstrap(expected)
                self.assertEqual(caught.exception.code, 'PACKAGE_MODIFIED')

    def test_reserved_entrypoints_rejected_before_install(self):
        for name in ('python', 'python3', 'python3.11', 'ray', 'torchrun', 'raytrain-environment', 'pip3'):
            with self.assertRaises(runtime.CaptureError):
                runtime.reject_reserved_entrypoints('[console_scripts]\n' + name + ' = example:main\n')
        runtime.reject_reserved_entrypoints('[console_scripts]\nexample-cli = example:main\n')

    def test_untracked_file_outside_site_packages_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            env = pathlib.Path(directory)
            site = env / 'lib' / f'python{runtime.sys.version_info.major}.{runtime.sys.version_info.minor}' / 'site-packages'
            site.mkdir(parents=True)
            (env / 'personal-training-source.py').write_text('private code')
            with mock.patch.object(runtime, 'ENVIRONMENT', env):
                with self.assertRaises(runtime.CaptureError):
                    runtime.verify_managed_tree([])

    def test_kernel_share_data_uses_portable_fingerprint(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'kernel.whl'
            with zipfile.ZipFile(path, 'w') as wheel:
                wheel.writestr('kernel-1.data/data/share/jupyter/kernels/python3/kernel.json', '{}')
            expected = runtime.content_hash([('.data/share/jupyter/kernels/python3/kernel.json', hashlib.sha256(b'{}').hexdigest())])
            self.assertEqual(runtime.wheel_fingerprint(path), expected)

    def test_fixed_conda_base_missing_sources_are_fingerprinted_not_ignored(self):
        import types
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            package_path = runtime.metadata.PackagePath('missing-build-source.py')
            package_path.hash = None
            distribution = types.SimpleNamespace(
                files=[package_path],
                locate_file=lambda name: root / str(name), read_text=lambda name: None)
            absent = runtime.distribution_fingerprint(distribution, managed=False)
            with self.assertRaises(runtime.CaptureError):
                runtime.distribution_fingerprint(distribution, managed=True)
            (root / 'missing-build-source.py').write_text('new content')
            self.assertNotEqual(absent, runtime.distribution_fingerprint(distribution, managed=False))

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

    def test_managed_bootstrap_is_immutable_base_not_a_saved_dependency_delta(self):
        # Venv seed packages are fixed platform materials even though they live
        # inside the managed prefix. Only a user's added wheel is captured.
        baseline = {name: {'version': '1.0', 'filesHash': digit * 64, 'managed': True}
                    for name, digit in [('pip', 'a'), ('setuptools', 'b'), ('wheel', 'c')]}
        current = {**baseline, 'humanize': {'version': '4.11.0', 'filesHash': 'd' * 64, 'managed': True}}
        self.assertEqual(runtime.package_delta(baseline, current), [
            {'name': 'humanize', 'version': '4.11.0', 'filesHash': 'd' * 64}])
        for name in baseline:
            for field, value in [('version', '0.9'), ('filesHash', 'e' * 64), ('managed', False)]:
                with self.subTest(package=name, field=field):
                    modified = {**current, name: {**baseline[name], field: value}}
                    with self.assertRaises(runtime.CaptureError) as caught:
                        runtime.package_delta(baseline, modified)
                    self.assertEqual(caught.exception.code, 'PACKAGE_MODIFIED')

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
