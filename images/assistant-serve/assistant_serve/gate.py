def validate_gate_url(url):
    raise NotImplementedError("fixed gate URL boundary not implemented")


class GateState:
    def update(self, payload, now_wall, now_monotonic):
        raise NotImplementedError("gate freshness not implemented")

    def is_open(self, now_monotonic):
        raise NotImplementedError("gate fail-closed check not implemented")
