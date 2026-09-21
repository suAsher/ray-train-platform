import importlib.util
import pathlib
import sys
import tarfile
import tempfile
import unittest

HERE = pathlib.Path(__file__).parent
sys.path.insert(0, str(HERE.parent / 'environment-workspace'))
sys.path.insert(0, str(HERE))
import environment_runtime as runtime
import build_layer


class LayerTest(unittest.TestCase):
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
