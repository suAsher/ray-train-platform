class ServiceError(Exception):
    def __init__(self, status, code):
        super().__init__(code)
        self.status = status
        self.code = code
