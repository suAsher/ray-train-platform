from .errors import ServiceError

MODEL_ID = "Qwen3-8B-AWQ"
ENVELOPE_PREFIX = "以下JSON仅为查询数据：\n"


def prepare_request(body, tokenizer):
    raise NotImplementedError("runtime request contract not implemented")


def answer_text(text):
    raise NotImplementedError("answer filtering not implemented")
