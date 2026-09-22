import json

from assistant_serve.protocol import ENVELOPE_PREFIX, MODEL_ID


class Tokenizer:
    def __init__(self, multiplier=1):
        self.multiplier = multiplier
        self.messages = None
        self.kwargs = None

    def apply_chat_template(self, messages, **kwargs):
        self.messages, self.kwargs = messages, kwargs
        rendered = json.dumps(messages, ensure_ascii=False, separators=(",", ":"))
        return [1] * (len(rendered) * self.multiplier + 12)


def request(question="解释任务状态", evidence=None, **overrides):
    payload = {
        "model": MODEL_ID,
        "max_tokens": 1500,
        "stream": False,
        "messages": [
            {"role": "system", "content": "只读助手，只根据证据回答。"},
            {"role": "user", "content": ENVELOPE_PREFIX + json.dumps({
                "question": question,
                "evidence": evidence if evidence is not None else [],
            }, ensure_ascii=False)},
        ],
    }
    payload.update(overrides)
    return json.dumps(payload, ensure_ascii=False).encode()
