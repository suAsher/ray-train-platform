class AssistantRuntime:
    def __init__(self, engine, tokenizer, gate):
        self.engine, self.tokenizer, self.gate = engine, tokenizer, gate

    async def complete(self, body):
        raise NotImplementedError("bounded async inference not implemented")

    async def refresh_gate(self):
        raise NotImplementedError("gate revocation not implemented")

    async def healthz(self):
        raise NotImplementedError("readiness not implemented")

    async def livez(self):
        raise NotImplementedError("engine liveness not implemented")

    async def check_health(self):
        raise NotImplementedError("Ray Serve engine health not implemented")
