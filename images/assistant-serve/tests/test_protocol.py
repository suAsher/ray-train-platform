import json
import unittest

from assistant_serve.errors import ServiceError
from assistant_serve.protocol import ENVELOPE_PREFIX, answer_text, prepare_request
from helpers import Tokenizer, request


class ProtocolTests(unittest.TestCase):
    def test_exact_template_tokens_and_question_are_preserved(self):
        tokenizer = Tokenizer(multiplier=2)
        prepared = prepare_request(request(), tokenizer)
        self.assertEqual(prepared.question, "解释任务状态")
        self.assertFalse(prepared.evidence_truncated)
        self.assertLessEqual(len(prepared.token_ids) + prepared.max_tokens, 8192)
        self.assertEqual(tokenizer.kwargs, {"tokenize": True, "return_dict": False,
                                          "add_generation_prompt": True, "enable_thinking": False})
        envelope = json.loads(tokenizer.messages[1]["content"][len(ENVELOPE_PREFIX):])
        self.assertEqual(envelope["question"], prepared.question)

    def test_only_evidence_is_trimmed_using_tokenizer(self):
        question = "问题不得被静默裁剪" * 10
        evidence = [{"index": i + 1, "id": "doc:" + str(i), "title": "证据", "excerpt": "内容" * 1600} for i in range(3)]
        tokenizer = Tokenizer(multiplier=2)
        prepared = prepare_request(request(question, evidence), tokenizer)
        self.assertTrue(prepared.evidence_truncated)
        self.assertEqual(prepared.question, question)
        self.assertEqual(tokenizer.messages[0]["content"], "只读助手，只根据证据回答。")
        envelope = json.loads(tokenizer.messages[1]["content"][len(ENVELOPE_PREFIX):])
        self.assertEqual(envelope["question"], question)
        self.assertLessEqual(len(prepared.token_ids) + prepared.max_tokens, 8192)
        self.assertTrue(set(prepared.evidence_ids).issubset({"doc:0", "doc:1", "doc:2"}))
        self.assertEqual(evidence[0]["excerpt"], "内容" * 1600)

    def test_oversized_question_is_rejected_instead_of_truncated(self):
        with self.assertRaises(ServiceError) as raised:
            prepare_request(request("问" * 4000), Tokenizer(multiplier=3))
        self.assertEqual(raised.exception.code, "context_too_long")

    def test_rejects_unbounded_or_untrusted_request_options(self):
        invalid = [
            request(model="another-model"), request(stream=True), request(max_tokens=1501),
            request(max_tokens=True), request(max_tokens=0), request(tools=[]),
            request(files=[]), request(base_url="https://untrusted.invalid"),
            request(chat_template="untrusted"), request(thinking={"type": "enabled"}),
            request(messages=[{"role": "user", "content": [{"type": "image_url", "image_url": {"url": "https://untrusted.invalid"}}]}]),
            request(messages=[{"role": "system", "content": "system"}, {"role": "user", "content": "not an evidence envelope"}]),
            request("x" * 4001), request(evidence=[{}] * 9), b"x" * (128 * 1024 + 1),
            b'{"model":"Qwen3-8B-AWQ","model":"other"}', b"null", b"{} {}",
        ]
        for body in invalid:
            with self.subTest(body=body[:80]), self.assertRaises(ServiceError):
                prepare_request(body, Tokenizer())

    def test_disabled_thinking_compatibility_does_not_enable_model_thinking(self):
        tokenizer = Tokenizer()
        prepare_request(request(thinking={"type": "disabled"}), tokenizer)
        self.assertFalse(tokenizer.kwargs["enable_thinking"])

    def test_thinking_blocks_never_reach_answer(self):
        for source, expected in [
            ("<think>private\nreasoning</think>公开答案", "公开答案"),
            ("<think>private without end", ""),
            ("private prefix</think>公开答案", "公开答案"),
            ("答案<think>private</think>结尾", "答案结尾"),
            ("answer", "answer"),
        ]:
            with self.subTest(source=source):
                self.assertEqual(answer_text(source), expected)


if __name__ == "__main__":
    unittest.main()
