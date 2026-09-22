import json
from pathlib import Path
import tempfile
import unittest

from assistant_serve.engine import engine_arguments, model_directory


class EnginePlanTests(unittest.TestCase):
    def test_single_gpu_and_bounded_engine_configuration(self):
        args = engine_arguments("/models/Qwen3-8B-AWQ")
        self.assertEqual(args["tensor_parallel_size"], 1)
        self.assertEqual(args["max_num_seqs"], 2)
        self.assertEqual(args["max_model_len"], 8192)
        self.assertEqual(args["load_format"], "safetensors")
        self.assertEqual(args["quantization"], "awq")
        self.assertFalse(args["trust_remote_code"])
        self.assertFalse(args["enable_lora"])
        self.assertTrue(args["disable_log_requests"])

    def test_local_model_must_be_complete_and_cannot_escape_mount(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "models"
            model = root / "Qwen3-8B-AWQ"
            model.mkdir(parents=True)
            config = {"model_type": "qwen3", "architectures": ["Qwen3ForCausalLM"], "quantization_config": {"quant_method": "awq", "bits": 4}}
            (model / "config.json").write_text(json.dumps(config))
            for name in ("tokenizer.json", "tokenizer_config.json", "model.safetensors"):
                (model / name).write_text("{}")
            self.assertEqual(model_directory(str(model), root), str(model))
            for invalid in ("Qwen/Qwen3-8B-AWQ", "https://example.invalid/model", str(Path(directory))):
                with self.assertRaises(ValueError):
                    model_directory(invalid, root)
            (model / "model.safetensors.index.json").write_text(json.dumps({"weight_map": {"layer": "missing.safetensors"}}))
            with self.assertRaises(ValueError):
                model_directory(str(model), root)
            (model / "model.safetensors.index.json").unlink()
            (model / "tokenizer.json").unlink()
            outside = Path(directory) / "outside.json"
            outside.write_text("{}")
            (model / "tokenizer.json").symlink_to(outside)
            with self.assertRaises(ValueError):
                model_directory(str(model), root)


if __name__ == "__main__":
    unittest.main()
