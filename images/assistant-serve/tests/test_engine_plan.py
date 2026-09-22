import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from assistant_serve.engine import engine_arguments, model_directory


class EnginePlanTests(unittest.TestCase):
    def test_metadata_must_be_a_regular_file_before_reading(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            model = root / "Qwen3-8B-AWQ"
            model.mkdir()
            config = {"model_type": "qwen3", "architectures": ["Qwen3ForCausalLM"],
                      "quantization_config": {"quant_method": "awq", "bits": 4}}
            for name in ("tokenizer.json", "tokenizer_config.json", "model.safetensors"):
                (model / name).write_text("{}")
            original_read = Path.read_text
            for name in ("config.json", "model.safetensors.index.json"):
                (model / "config.json").write_text(json.dumps(config))
                metadata = model / name
                metadata.unlink(missing_ok=True)
                os.mkfifo(metadata)

                def read_checked(path, *args, **kwargs):
                    if path == metadata:
                        raise AssertionError("nonregular metadata must not be opened")
                    return original_read(path, *args, **kwargs)

                with patch.object(Path, "read_text", autospec=True, side_effect=read_checked):
                    with self.assertRaises(ValueError):
                        model_directory(str(model), root)
                metadata.unlink()

    def test_single_gpu_and_bounded_engine_configuration(self):
        args = engine_arguments("/models/Qwen3-8B-AWQ")
        self.assertEqual(args["tensor_parallel_size"], 1)
        self.assertEqual(args["max_num_seqs"], 2)
        self.assertEqual(args["max_model_len"], 8192)
        self.assertEqual(args["load_format"], "safetensors")
        self.assertEqual(args["quantization"], "awq")
        self.assertFalse(args["trust_remote_code"])
        self.assertFalse(args["enable_lora"])
        self.assertFalse(args["enable_log_requests"])
        self.assertEqual(args["cpu_offload_gb"], 0)

    def test_cross_request_prefix_cache_is_explicitly_disabled(self):
        # vLLM V1 enables this by default; shared assistant requests can carry
        # different tenants' authorized task evidence.
        self.assertIs(engine_arguments("/models/Qwen3-8B-AWQ").get("enable_prefix_caching"), False)

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
            index = model / "model.safetensors.index.json"
            for shard in ("../outside.safetensors", "/tmp/outside.safetensors"):
                index.write_text(json.dumps({"weight_map": {"layer": shard}}))
                with self.assertRaises(ValueError):
                    model_directory(str(model), root)
            fifo = model / "blocked.safetensors"
            os.mkfifo(fifo)
            index.write_text(json.dumps({"weight_map": {"layer": fifo.name}}))
            with self.assertRaises(ValueError):
                model_directory(str(model), root)
            fifo.unlink()
            index.unlink()
            (model / "tokenizer.json").unlink()
            outside = Path(directory) / "outside.json"
            outside.write_text("{}")
            (model / "tokenizer.json").symlink_to(outside)
            with self.assertRaises(ValueError):
                model_directory(str(model), root)


if __name__ == "__main__":
    unittest.main()
