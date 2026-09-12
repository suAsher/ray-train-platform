import importlib.util
import io
import tempfile
import unittest
import zipfile
from pathlib import Path

spec = importlib.util.spec_from_file_location('safe_extract', Path(__file__).with_name('platform-safe-extract.py'))
extract = importlib.util.module_from_spec(spec)
spec.loader.exec_module(extract)

class SafeEvaluationExtractTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.archive = self.root / 'code.zip'
        self.destination = self.root / 'workspace'
    def write_zip(self, entries):
        with zipfile.ZipFile(self.archive, 'w') as archive:
            for name, value in entries.items():
                archive.writestr(name, value)
    def run_extract(self, *extra):
        return extract.main(['--archive', str(self.archive), '--destination', str(self.destination), *extra])
    def test_script_and_module_entrypoints_are_checked_without_executing_code(self):
        for entries, args in [({'worker.py': 'raise Exception("never execute")'}, ['--required-script','worker.py']), ({'package/__main__.py':'raise Exception("never execute")'}, ['--required-module','package'])]:
            self.write_zip(entries)
            self.run_extract(*args)
            for name in entries:
                (self.destination / name).unlink()
    def test_missing_or_escaping_entrypoint_is_rejected(self):
        for args in [['--required-script','missing.py'], ['--required-script','../outside.py'], ['--required-module','package;evil']]:
            self.write_zip({'worker.py':'pass'})
            with self.assertRaises(ValueError):
                self.run_extract(*args)
            for entry in self.destination.glob('*.py'):
                entry.unlink()
    def test_evaluation_expansion_limit_is_lower_without_changing_default(self):
        self.write_zip({'worker.py':'x'*100})
        with self.assertRaises(ValueError):
            self.run_extract('--max-uncompressed-bytes','50')
        self.assertFalse((self.destination/'worker.py').exists())
        self.run_extract()
        self.assertEqual((self.destination/'worker.py').stat().st_size,100)
    def test_archive_traversal_is_rejected(self):
        self.write_zip({'../outside.py':'pass'})
        with self.assertRaises(ValueError):
            self.run_extract('--max-uncompressed-bytes','268435456')
        self.assertFalse((self.root/'outside.py').exists())
    def test_invalid_limits_and_symlinks_are_rejected(self):
        for size in ['0', str(extract.MAX_UNCOMPRESSED_BYTES + 1)]:
            with self.assertRaises(ValueError):
                self.run_extract('--max-uncompressed-bytes',size)
        with zipfile.ZipFile(self.archive,'w') as archive:
            link=zipfile.ZipInfo('link.py')
            link.create_system=3
            link.external_attr=0o120777 << 16
            archive.writestr(link,'outside.py')
        with self.assertRaises(ValueError):
            self.run_extract()
        self.assertFalse((self.destination/'link.py').exists())

if __name__=='__main__':
    unittest.main()
