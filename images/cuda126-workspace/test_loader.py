"""Native-driver-only image contracts; actual image probes run on the builder."""
import json
import os
from pathlib import Path
import unittest

FOLDER = Path(__file__).resolve().parent
PURGE = 'RUN --network=none apt-get purge --yes cuda-compat-12-6'


class NativeLoaderContract(unittest.TestCase):
    def setUp(self):
        self.dockerfile = (FOLDER / 'Dockerfile.base').read_text()
        self.tail = self.dockerfile.rsplit('RUN raytrain-cuda126-selfcheck', 1)[1]
        probe = os.environ.get('RAYTRAIN_CUDA126_LOADER_PROBE_JSON')
        self.probe = json.loads(Path(probe).read_text()) if probe else None

    def test_only_explicit_compat_package_is_purged_offline(self):
        self.assertIn('USER root\n' + PURGE + '\nUSER ray', self.tail)
        self.assertEqual(self.tail.count('apt-get'), 1)
        for forbidden in ('autoremove', '--force', 'apt-get update', 'apt-get install', 'rm -rf'):
            self.assertNotIn(forbidden, self.tail)
        if self.probe is not None:
            self.assertEqual(self.probe['osPackageDiff']['removed'], ['cuda-compat-12-6'])
            self.assertEqual(self.probe['osPackageDiff']['added'], [])
            self.assertEqual(self.probe['osPackageDiff']['changed'], [])

    def test_compat_driver_libraries_are_absent_from_actual_image(self):
        self.assertIn(PURGE, self.tail)
        if self.probe is not None:
            self.assertFalse(self.probe['cudaCompatInstalled'])
            self.assertEqual(self.probe['compatDriverLibraryPaths'], [])
            self.assertEqual(self.probe['ldCacheCompatLines'], [])

    def test_compiler_runtime_and_link_stubs_remain_without_loader_overrides(self):
        self.assertNotIn('LD_LIBRARY_PATH', self.tail)
        if self.probe is not None:
            self.assertTrue(self.probe['nvccUnchanged'])
            self.assertTrue(self.probe['cudartPackageUnchanged'])
            self.assertTrue(self.probe['compilationStubsRetained'])
            self.assertFalse(self.probe['ldLibraryPathContainsStubs'])


if __name__ == '__main__':
    unittest.main()
