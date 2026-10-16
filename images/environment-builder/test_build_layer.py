import importlib.util
import pathlib
import json
import hashlib
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).parent
sys.path.insert(0, str(HERE.parent / 'environment-workspace'))
sys.path.insert(0, str(HERE))
import environment_runtime as runtime
import build_layer


class LayerTest(unittest.TestCase):
    def test_profile_export_is_present_in_actual_layer_and_remains_read_only(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            env, helper, context = root / 'environment', root / 'helpers', root / 'context'
            for path in (env, helper, context):
                path.mkdir()
            (env / 'module.py').write_text('VALUE = 1')
            for name in build_layer.FIXED_FILES:
                (helper / name).write_text('trusted helper')
            baseline, profile = root / 'baseline.json', root / 'profile.json'
            baseline.write_text('{}')
            profile.write_text('{"schemaVersion":1}')
            for name in ('capture.json', 'requirements.lock', 'materials.json'):
                (context / name).write_text('{}')
            archive_path = root / 'layer.tar'
            with mock.patch.object(build_layer, 'ENVIRONMENT', env), mock.patch.object(build_layer, 'RUNTIME_ROOT', helper), mock.patch.object(build_layer, 'BASELINE', baseline), mock.patch.object(runtime, 'ACTIVE_PROFILE', {'schemaVersion': 1}), mock.patch.object(runtime, 'PROFILE', profile), mock.patch.object(runtime, 'verify_active_profile') as verify:
                digest = build_layer.export_layer(archive_path, context)
            verify.assert_called_once()
            self.assertEqual(digest, 'sha256:' + hashlib.sha256(archive_path.read_bytes()).hexdigest())
            with tarfile.open(archive_path) as archive:
                member = archive.getmember('etc/raytrain/environment-profile.json')
                self.assertEqual((member.uid, member.gid, member.mode), (0, 0, 0o444))
                self.assertEqual(archive.extractfile(member).read(), profile.read_bytes())
                self.assertIn('opt/raytrain/environment-baseline.json', archive.getnames())
                self.assertIn('usr/local/lib/raytrain-environment/environment_runtime.py', archive.getnames())

    def test_build_result_labels_profile_versions_without_changing_capture_contract(self):
        versions = {'python': '3.10', 'pytorch': '2.13.0+cu126', 'cuda': '12.6', 'ray': '2.58.0'}
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            baseline = root / 'baseline.json'
            baseline.write_text(json.dumps({'bootstrap': {}}))
            def export(destination, context):
                destination.write_bytes(b'checked layer')
                return 'sha256:' + hashlib.sha256(destination.read_bytes()).hexdigest()
            with mock.patch.object(build_layer, 'read_manifest', return_value={'packages': []}), mock.patch.object(build_layer.sys, 'prefix', str(build_layer.ENVIRONMENT)), mock.patch.object(build_layer, 'clean_operation_artifacts'), mock.patch.object(build_layer, 'prepare', return_value={'captureSha256': 'a' * 64}), mock.patch.object(build_layer, 'BASELINE', baseline), mock.patch.object(build_layer, 'verify_bootstrap'), mock.patch.object(build_layer.subprocess, 'run', return_value=mock.Mock(returncode=0)), mock.patch.object(build_layer, 'export_layer', side_effect=export), mock.patch.object(runtime, 'runtime_versions', return_value=versions):
                report = build_layer.build(root / 'capture.json', root, 'https://mirror/simple')
            self.assertEqual(report['checks']['runtimeVersions'], versions)
            self.assertEqual(report['checks']['gpuValidation'], 'not_run')
            self.assertEqual(json.loads((root / 'build-result.json').read_text()), report)

    def test_profile_export_retains_root_read_only_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            profile = root / 'profile.json'
            profile.write_text('{}')
            with tarfile.open(root / 'layer.tar', 'w') as archive:
                build_layer.add_path(archive, profile, 'etc/raytrain/environment-profile.json', mode=0o444)
            with tarfile.open(root / 'layer.tar') as archive:
                self.assertEqual(archive.next().mode, 0o444)

    def test_builder_default_preserved_and_existing_venv_not_recreated(self):
        dockerfile = (HERE / 'Dockerfile').read_text()
        self.assertIn('ARG ENVIRONMENT_BASE_IMAGE=harbor.wellspiking.ai/guofeng.su/raytrain-base@sha256:', dockerfile)
        self.assertIn('FROM ${ENVIRONMENT_BASE_IMAGE}', dockerfile)
        self.assertIn('test -x /opt/raytrain/environment/bin/python ||', dockerfile)
        self.assertIn('RUN "${ENVIRONMENT_BASE_PYTHON}" -c', dockerfile)
        self.assertIn("'pytorch':'2.13.0+cu126'", dockerfile)

    def test_symlink_stays_inside_managed_environment(self):
        build_layer.safe_venv_link('opt/raytrain/environment/lib64', 'lib')
        build_layer.safe_venv_link('opt/raytrain/environment/bin/tool', '../lib/tool')
        for link in ['/home/ray/anaconda3/bin/python3', '../../../../etc/passwd', '../../other/secret']:
            with self.assertRaises(runtime.CaptureError):
                build_layer.safe_venv_link('opt/raytrain/environment/bin/python3', link)

    def test_layer_member_is_normalized_and_has_no_host_owner(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            package = root / 'module.py'
            package.write_text('VALUE = 1\n')
            package.chmod(0o666)
            archive_path = root / 'layer.tar'
            with tarfile.open(archive_path, 'w') as archive:
                build_layer.add_path(archive, package, 'opt/raytrain/environment/lib/python3.10/site-packages/demo.py')
            with tarfile.open(archive_path) as archive:
                member = archive.next()
                self.assertEqual(member.uid, 0)
                self.assertEqual(member.gid, 0)
                self.assertEqual(member.mode, 0o644)
                self.assertEqual(member.mtime, 0)
                self.assertEqual(archive.extractfile(member).read(), b'VALUE = 1\n')

    def test_fixed_files_do_not_include_editors_or_user_directories(self):
        for target in build_layer.FIXED_FILES.values():
            self.assertTrue(target.startswith(('usr/local/', 'opt/raytrain/')))
            for forbidden in ('workspace', 'home/ray', 'editor', 'jupyter', 'code-server', 'credentials'):
                self.assertNotIn(forbidden, target)

    def test_retry_cleanup_only_removes_owned_artifacts_without_following_links(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            artifacts = root / 'artifacts'
            artifacts.mkdir()
            other = root / 'outside'
            other.mkdir()
            (other / 'keep').write_text('keep')
            (artifacts / 'context').symlink_to(other, target_is_directory=True)
            (artifacts / 'unrelated').write_text('retain')
            (artifacts / 'layer.tar.partial').write_text('partial')
            with mock.patch.object(pathlib.Path, 'resolve', return_value=pathlib.Path('/artifacts')):
                build_layer.clean_operation_artifacts(artifacts)
            self.assertTrue((other / 'keep').exists())
            self.assertTrue((artifacts / 'unrelated').exists())
            self.assertFalse((artifacts / 'context').exists())
            self.assertFalse((artifacts / 'layer.tar.partial').exists())

    def test_retry_cleanup_rejects_user_workspace(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(runtime.CaptureError):
                build_layer.clean_operation_artifacts(pathlib.Path(directory))

    def test_failed_build_writes_safe_termination_code(self):
        with tempfile.TemporaryDirectory() as directory:
            result = pathlib.Path(directory) / 'termination'
            args = ['build', '--manifest', '/snapshot/capture.json', '--artifacts', '/artifacts',
                    '--index-url', 'https://mirror/simple', '--result', str(result)]
            with mock.patch.object(sys, 'argv', args), mock.patch.object(build_layer, 'build', side_effect=runtime.CaptureError('sensitive detail', 'PACKAGE_MODIFIED')):
                with self.assertRaises(SystemExit) as stopped:
                    build_layer.main()
            self.assertEqual(stopped.exception.code, 1)
            self.assertEqual(result.read_text(), '{"code":"PACKAGE_MODIFIED"}')

    def test_special_file_rejected(self):
        import os
        with tempfile.TemporaryDirectory() as directory:
            fifo = pathlib.Path(directory) / 'pipe'
            os.mkfifo(fifo)
            with tarfile.open(pathlib.Path(directory) / 'layer.tar', 'w') as archive:
                with self.assertRaises(runtime.CaptureError):
                    build_layer.add_path(archive, fifo, 'opt/raytrain/environment/pipe')


if __name__ == '__main__':
    unittest.main()
