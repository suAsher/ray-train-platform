import unittest

from assistant_serve.gate import GateState, validate_gate_url


class GateTests(unittest.TestCase):
    def test_gate_defaults_closed_and_has_local_freshness_ceiling(self):
        state = GateState()
        self.assertFalse(state.is_open(10))
        state.update({"allow": True, "epoch": "e1", "validUntil": "1970-01-01T00:01:50Z"}, 100, 10)
        self.assertTrue(state.is_open(12.9))
        self.assertFalse(state.is_open(13))

    def test_expiration_and_bad_payload_fail_closed(self):
        invalid = [None, {}, {"allow": "true", "epoch": "e1", "validUntil": "1970-01-01T00:01:50Z"},
                   {"allow": True, "epoch": "", "validUntil": "1970-01-01T00:01:50Z"},
                   {"allow": True, "epoch": "e1", "validUntil": "1970-01-01T00:01:39Z"},
                   {"allow": True, "epoch": "e1", "validUntil": "invalid"},
                   {"allow": True, "epoch": "e1", "validUntil": "1970-01-01T00:01:50"}]
        for payload in invalid:
            state = GateState()
            state.update({"allow": True, "epoch": "e1", "validUntil": "1970-01-01T00:01:50Z"}, 100, 10)
            state.update(payload, 100, 11)
            self.assertFalse(state.is_open(11))
        state = GateState()
        state.update({"allow": True, "epoch": "e1", "validUntil": "1970-01-01T00:01:41Z"}, 100, 10)
        self.assertFalse(state.is_open(11))

    def test_fixed_gate_url_disallows_external_hosts_credentials_and_redirect_targets(self):
        for address in ["http://controller.ns.svc.cluster.local:8080/gate", "http://127.0.0.1:8080/gate", "https://controller.ns.svc.cluster.local/gate"]:
            self.assertEqual(validate_gate_url(address), address)
        for address in ["http://evil.invalid/gate", "https://evil.invalid/gate", "http://user:secret@localhost/gate", "http://localhost/gate?url=evil", "http://localhost/gate#x", "http://localhost/not-gate", "file:///gate", "http://controller.ns.svc.cluster.local.evil/gate"]:
            with self.subTest(address=address), self.assertRaises(ValueError):
                validate_gate_url(address)


if __name__ == "__main__":
    unittest.main()
