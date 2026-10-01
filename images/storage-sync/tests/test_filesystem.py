"""IDC descriptor traversal and browse contracts; execute on the builder."""
import os
import tempfile
import unittest
from pathlib import Path

from storage_sync.filesystem import browse_idc, open_verified, scan_idc
from storage_sync.model import SyncError


class FilesystemSafetyTests(unittest.TestCase):
    def test_file_basename_and_selected_directory_contents(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'data' / 'nested').mkdir(parents=True)
            (root / 'data' / 'nested' / '中文 %.bin').write_bytes(b'payload')
            single = scan_idc(root, 'data/nested/中文 %.bin', 'CONTENT')
            tree = scan_idc(root, 'data', 'CONTENT')
            self.assertEqual([entry.relative_path for entry in single], ['中文 %.bin'])
            self.assertEqual([entry.relative_path for entry in tree], ['nested/中文 %.bin'])
            with open_verified(tree[0]) as stream:
                self.assertEqual(stream.read(), b'payload')

    def test_root_and_intermediate_symlink_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / 'root'
            root.mkdir()
            (root / 'data').mkdir()
            (root / 'data' / 'file').write_bytes(b'payload')
            (base / 'root-link').symlink_to(root, target_is_directory=True)
            (root / 'data-link').symlink_to(root / 'data', target_is_directory=True)
            with self.assertRaises(SyncError):
                scan_idc(base / 'root-link', 'data/file', 'CONTENT')
            with self.assertRaises(SyncError):
                scan_idc(root, 'data-link/file', 'CONTENT')

    def test_nested_special_file_is_reported(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'nested').mkdir()
            os.mkfifo(root / 'nested' / 'fifo')
            with self.assertRaises(SyncError):
                scan_idc(root, '', 'CONTENT')

    def test_mtime_restored_modification_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / 'file'
            target.write_bytes(b'old')
            entry = scan_idc(root, 'file', 'CONTENT')[0]
            target.write_bytes(b'new')
            os.utime(target, ns=(entry.mtime_ns, entry.mtime_ns))
            with self.assertRaisesRegex(SyncError, 'SOURCE_CHANGED'):
                with open_verified(entry):
                    pass

    def test_modification_during_read_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / 'file'
            target.write_bytes(b'old')
            entry = scan_idc(root, 'file', 'CONTENT')[0]
            with self.assertRaisesRegex(SyncError, 'SOURCE_CHANGED'):
                with open_verified(entry) as stream:
                    self.assertEqual(stream.read(), b'old')
                    target.write_bytes(b'new')

    def test_replaced_parent_never_reads_symlink_target(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / 'root'
            (root / 'data').mkdir(parents=True)
            outside = base / 'outside'
            outside.mkdir()
            (root / 'data' / 'file').write_bytes(b'allowed')
            (outside / 'file').write_bytes(b'private')
            entry = scan_idc(root, 'data/file', 'CONTENT')[0]
            (root / 'data').rename(root / 'old-data')
            (root / 'data').symlink_to(outside, target_is_directory=True)
            with self.assertRaises(SyncError):
                with open_verified(entry):
                    self.fail('replaced parent must be rejected before reading')


class FilesystemBrowseTests(unittest.TestCase):
    def test_browse_one_level_bounded_and_roundtrip_cursor(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'a').write_bytes(b'a')
            (root / 'b').write_bytes(b'bb')
            (root / 'sub').mkdir()
            (root / 'sub' / 'nested').write_bytes(b'nested')
            first, cursor = browse_idc(root, '', limit=2)
            self.assertEqual([entry['name'] for entry in first], ['a', 'b'])
            self.assertEqual(first[0]['kind'], 'file')
            self.assertEqual(first[1]['sizeBytes'], 2)
            self.assertTrue(cursor)
            second, cursor = browse_idc(root, '', token=cursor, limit=2)
            self.assertEqual(second[0]['name'], 'sub')
            self.assertEqual(second[0]['kind'], 'directory')
            self.assertEqual(second[0]['relativePath'], 'sub')
            self.assertFalse(cursor)

    def test_browse_rejects_unsafe_entries_and_bad_parameters(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for kwargs in ({'path': '../escape'}, {'path': '/', 'limit': 1},
                           {'path': '', 'token': 'invalid'}, {'path': '', 'limit': 0},
                           {'path': '', 'limit': 1001}):
                with self.subTest(kwargs=kwargs), self.assertRaises(SyncError):
                    browse_idc(root, **kwargs)
            (root / 'link').symlink_to('/etc/passwd')
            with self.assertRaises(SyncError):
                browse_idc(root, '')

    def test_cursor_bound_to_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'a').write_bytes(b'a')
            (root / 'b').write_bytes(b'b')
            (root / 'sub').mkdir()
            _, token = browse_idc(root, '', limit=1)
            with self.assertRaises(SyncError):
                browse_idc(root, 'sub', token=token, limit=1)


if __name__ == '__main__':
    unittest.main()
