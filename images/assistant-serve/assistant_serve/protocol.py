"""The deliberately small subset of Chat Completions used by the Go gateway."""
import json
import re
from dataclasses import dataclass

from .errors import ServiceError

MODEL_ID = "Qwen3-8B-AWQ"
ENVELOPE_PREFIX = "以下JSON仅为查询数据：\n"
BODY_LIMIT = 128 * 1024
CONTEXT_LIMIT = 8192
OUTPUT_LIMIT = 1500


@dataclass(frozen=True)
class PreparedRequest:
    token_ids: tuple
    max_tokens: int
    question: str
    evidence_ids: tuple
    evidence_truncated: bool


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate field")
        result[key] = value
    return result


def strict_json(body):
    def reject_constant(_):
        raise ValueError("non-finite number")
    return json.loads(body, object_pairs_hook=_unique_object, parse_constant=reject_constant)


def _invalid():
    raise ServiceError(400, "invalid_request")


def _text(value, limit, *, nonempty=False):
    if not isinstance(value, str) or len(value.encode("utf-8")) > limit:
        _invalid()
    if nonempty and not value.strip():
        _invalid()
    return value


def _decode(body):
    if not isinstance(body, bytes) or len(body) > BODY_LIMIT:
        raise ServiceError(413, "request_too_large")
    try:
        payload = strict_json(body.decode("utf-8"))
        allowed = {"model", "messages", "max_tokens", "stream", "thinking"}
        if not isinstance(payload, dict) or set(payload) - allowed:
            _invalid()
        if payload.get("model") != MODEL_ID or payload.get("stream", False) is not False:
            _invalid()
        maximum = payload.get("max_tokens", OUTPUT_LIMIT)
        if type(maximum) is not int or not 1 <= maximum <= OUTPUT_LIMIT:
            _invalid()
        if "thinking" in payload and payload["thinking"] != {"type": "disabled"}:
            _invalid()
        messages = payload.get("messages")
        if not isinstance(messages, list) or len(messages) != 2:
            _invalid()
        for item, role in zip(messages, ("system", "user")):
            if not isinstance(item, dict) or set(item) != {"role", "content"} or item["role"] != role:
                _invalid()
            _text(item["content"], BODY_LIMIT, nonempty=True)
        system = _text(messages[0]["content"], 8192, nonempty=True)
        content = messages[1]["content"]
        if not content.startswith(ENVELOPE_PREFIX):
            _invalid()
        envelope = strict_json(content[len(ENVELOPE_PREFIX):])
        if not isinstance(envelope, dict) or set(envelope) != {"question", "evidence"}:
            _invalid()
        question = _text(envelope["question"], 16000, nonempty=True)
        if len(question) > 4000:
            _invalid()
        evidence = _evidence(envelope["evidence"])
        return system, question, evidence, maximum
    except (ValueError, TypeError, UnicodeError, RecursionError):
        _invalid()


def _evidence(items):
    if not isinstance(items, list) or len(items) > 8:
        _invalid()
    result = []
    for position, item in enumerate(items, 1):
        if not isinstance(item, dict) or set(item) - {"index", "id", "title", "excerpt", "version"}:
            _invalid()
        if type(item.get("index")) is not int or item["index"] != position:
            _invalid()
        for key, limit in (("id", 160), ("title", 512), ("excerpt", 24000)):
            _text(item.get(key), limit, nonempty=key == "id")
        if "version" in item and (type(item["version"]) is not int or item["version"] < 0):
            _invalid()
        result.append(dict(item))
    return result


def prepare_request(body, tokenizer):
    system, question, evidence, maximum = _decode(body)
    budget = CONTEXT_LIMIT - maximum

    def encode(sources):
        envelope = json.dumps({"question": question, "evidence": sources}, ensure_ascii=False, separators=(",", ":"))
        messages = [{"role": "system", "content": system}, {"role": "user", "content": ENVELOPE_PREFIX + envelope}]
        tokens = tokenizer.apply_chat_template(messages, tokenize=True, return_dict=False,
                                               add_generation_prompt=True, enable_thinking=False)
        if not isinstance(tokens, list) or any(type(token) is not int for token in tokens):
            raise ServiceError(503, "tokenizer_unavailable")
        return tuple(tokens)

    # Do not enable tokenizer truncation: the entire question and system prompt
    # must fit before evidence can consume any of the remaining context budget.
    if len(encode([])) > budget:
        raise ServiceError(400, "context_too_long")
    sources = list(evidence)
    tokens = encode(sources)
    truncated = len(tokens) > budget
    while len(tokens) > budget and sources:
        # Evidence is atomic: truncating an excerpt can remove a shell guard,
        # argument, closing quote, or code fence and change its meaning. Drop
        # the lowest-ranked complete source; never cut question/system text.
        sources = sources[:-1]
        tokens = encode(sources)
    # Re-render the actual selected source set. This is also the only token
    # sequence sent to vLLM; engine-side re-tokenization cannot drop the question.
    tokens = encode(sources)
    if len(tokens) > budget:
        raise ServiceError(400, "context_too_long")
    return PreparedRequest(tokens, maximum, question, tuple(item["id"] for item in sources), truncated)


_THINK = re.compile(r"<think\s*>|</think\s*>", re.IGNORECASE)


def answer_text(text):
    if not isinstance(text, str) or len(text) > 12000:
        return ""
    parts, depth, start = [], 0, 0
    for marker in _THINK.finditer(text):
        closing = marker.group(0).startswith("</")
        if closing:
            if depth == 0:
                parts = []  # Some templates provide the opening tag in the prompt.
            else:
                depth -= 1
        else:
            if depth == 0:
                parts.append(text[start:marker.start()])
            depth += 1
        start = marker.end()
    if depth == 0:
        parts.append(text[start:])
    return "".join(parts).strip()
