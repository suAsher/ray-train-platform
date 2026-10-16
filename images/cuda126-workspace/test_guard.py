"""Source and real image-config contracts for the bounded CUDA minor branch.

Set RAYTRAIN_CUDA126_IMAGE_INSPECT_JSON on the builder to validate actual
docker image inspect output, without weakening NVIDIA's runtime checks.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import unittest

FOLDER = Path(__file__).resolve().parent
SOURCE = json.loads((FOLDER / 'nvidia-guard-source.json').read_text())
ORIGINAL = (FOLDER / 'nvidia-require-cuda-original.txt').read_text().strip()
ADDITION = SOURCE['additionalOrClause']
ENV_LINE = 'ENV NVIDIA_REQUIRE_CUDA="${NVIDIA_REQUIRE_CUDA} ' + ADDITION + '"'


def matches(expression, *, driver, cuda, brand='geforce', arch='8.9'):
    values = {'driver': driver, 'cuda': cuda, 'brand': brand, 'arch': arch}

    def term_matches(term):
        field, operator, expected = re.fullmatch(
            r'(driver|cuda|brand|arch)(>=|<=|=|>|<)([\w.]+)', term).groups()
        actual = values[field]
        if field != 'brand':
            actual = tuple(int(p) for p in actual.split('.'))
            expected = tuple(int(p) for p in expected.split('.'))
        return {'=': actual == expected, '>=': actual >= expected, '<=': actual <= expected,
                '>': actual > expected, '<': actual < expected}[operator]

    return any(all(term_matches(term) for term in branch.split(','))
               for branch in expression.split())


class NvidiaGuardContract(unittest.TestCase):
    def setUp(self):
        self.dockerfile = (FOLDER / 'Dockerfile.base').read_text()
        inspect_file = os.environ.get('RAYTRAIN_CUDA126_IMAGE_INSPECT_JSON')
        if inspect_file:
            inspected = json.loads(Path(inspect_file).read_text())[0]
            environment = dict(item.split('=', 1) for item in inspected['Config']['Env'])
            self.assertNotIn('NVIDIA_DISABLE_REQUIRE', environment)
            self.guard = environment['NVIDIA_REQUIRE_CUDA']
        else:
            self.guard = ORIGINAL + (' ' + ADDITION if ENV_LINE in self.dockerfile else '')

    def test_original_is_taken_from_pinned_official_parent_config(self):
        self.assertEqual(hashlib.sha256(ORIGINAL.encode()).hexdigest(), SOURCE['guardSHA256'])
        self.assertEqual(SOURCE['parentManifestDigest'],
                         'sha256:1608a19a5d6f013d36abfb9ad50a42b4c0ef86f4ab48e351c6899f0280b946c1')

    def test_final_config_only_append_follows_every_rootfs_instruction(self):
        self.assertEqual(self.dockerfile.strip().splitlines()[-1], ENV_LINE)
        self.assertNotIn('NVIDIA_DISABLE_REQUIRE', self.dockerfile)
        self.assertEqual(self.dockerfile.count('ENV NVIDIA_REQUIRE_CUDA='), 1)

    def test_all_original_conditions_are_preserved_exactly_once(self):
        self.assertEqual(self.guard, ORIGINAL + ' ' + ADDITION)
        self.assertEqual(self.guard.split()[:-1], ORIGINAL.split())

    def test_new_branch_accepts_current_fleet_and_exact_lower_bound(self):
        added = self.guard[len(ORIGINAL):].strip()
        for driver in ('550.127.05', '550.144.03'):
            with self.subTest(driver=driver):
                self.assertTrue(matches(added, driver=driver, cuda='12.4'))

    def test_new_branch_does_not_accept_other_drivers_devices_or_apis(self):
        added = self.guard[len(ORIGINAL):].strip()
        negative = [
            {'driver': '550.127.04'}, {'driver': '549.99'}, {'driver': '525.60.12'},
            {'driver': '551.0'}, {'cuda': '12.3'}, {'brand': 'tesla'},
            {'arch': '8.6'}, {'arch': '9.0'},
        ]
        for changed in negative:
            with self.subTest(changed=changed):
                self.assertFalse(matches(added, **{'driver': '550.144.03', 'cuda': '12.4', **changed}))

    def test_original_cuda126_path_and_original_brand_paths_remain(self):
        self.assertTrue(matches(self.guard, driver='560.35.05', cuda='12.6', brand='geforce', arch='9.0'))
        self.assertTrue(matches(self.guard, driver='550.144.03', cuda='12.4', brand='tesla', arch='8.6'))


if __name__ == '__main__':
    unittest.main()
