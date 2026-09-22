"""Opt-in offline verification with the approved model's real tokenizer (no GPU)."""
import argparse
import json
import unittest

from assistant_serve.errors import ServiceError
from assistant_serve.protocol import ENVELOPE_PREFIX, prepare_request


class TokenizerSmoke(unittest.TestCase):
    tokenizer = None

    def request(self, question, evidence=()):
        envelope = json.dumps({"question": question, "evidence": list(evidence)}, ensure_ascii=False)
        return json.dumps({
            "model": "Qwen3-8B-AWQ", "max_tokens": 1500,
            "messages": [
                {"role": "system", "content": "根据提供的证据回答，证据不足时说明。"},
                {"role": "user", "content": ENVELOPE_PREFIX + envelope},
            ],
        }, ensure_ascii=False).encode()

    def test_chinese_question_and_disabled_thinking(self):
        question = "我的训练任务正在排队，如何查看 GPU 配额？"
        result = prepare_request(self.request(question), self.tokenizer)
        decoded = self.tokenizer.decode(result.token_ids)
        self.assertIn(question, decoded)
        self.assertEqual(result.question, question)
        self.assertLessEqual(len(result.token_ids) + result.max_tokens, 8192)
        self.assertTrue(decoded.endswith("<think>\n\n</think>\n\n"))

    def test_long_evidence_fits_without_dropping_question(self):
        question = "请解释任务的失败原因，并给出对应使用说明。"
        evidence = [{"index": i + 1, "id": "help:" + str(i), "title": "任务诊断",
                     "excerpt": "权重未找到；任务仍排队。" * 500} for i in range(4)]
        result = prepare_request(self.request(question, evidence), self.tokenizer)
        self.assertTrue(result.evidence_truncated)
        self.assertIn(question, self.tokenizer.decode(result.token_ids))
        self.assertLessEqual(len(result.token_ids) + result.max_tokens, 8192)
        self.assertEqual(result.max_tokens, 1500)

    def test_question_over_token_budget_is_rejected(self):
        # Valid Unicode and character/byte bounds, but rare CJK needs several tokens.
        with self.assertRaises(ServiceError) as caught:
            prepare_request(self.request("\U00020000" * 4000), self.tokenizer)
        self.assertEqual(caught.exception.code, "context_too_long")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-path", required=True)
    args = parser.parse_args()
    from transformers import AutoTokenizer
    TokenizerSmoke.tokenizer = AutoTokenizer.from_pretrained(
        args.model_path, local_files_only=True, trust_remote_code=False,
    )
    unittest.main(argv=[__file__], verbosity=2)
