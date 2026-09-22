"""Offline single-GPU adapter for the pinned vLLM 0.8.5 runtime."""
import json
import os
from pathlib import Path

from .protocol import CONTEXT_LIMIT


def model_directory(value, root=Path("/models")):
    try:
        supplied = Path(value)
        if not supplied.is_absolute():
            raise ValueError()
        path = supplied.resolve(strict=True)
        if not path.is_dir() or not path.is_relative_to(root.resolve()) or path == root.resolve():
            raise ValueError()
        config_path = path / "config.json"
        if not config_path.resolve(strict=True).is_relative_to(path) or config_path.stat().st_size > 1024 * 1024:
            raise ValueError()
        config = json.loads(config_path.read_text())
        quantization = config.get("quantization_config", {})
        if config.get("model_type") != "qwen3" or quantization.get("quant_method") != "awq":
            raise ValueError()
        if config.get("architectures") != ["Qwen3ForCausalLM"] or quantization.get("bits") != 4:
            raise ValueError()
        required = [config_path, path / "tokenizer.json", path / "tokenizer_config.json"]
        weights = list(path.glob("*.safetensors"))
        if not weights:
            raise ValueError()
        index = path / "model.safetensors.index.json"
        if index.exists():
            if not index.resolve(strict=True).is_relative_to(path) or index.stat().st_size > 4 * 1024 * 1024:
                raise ValueError()
            weight_map = json.loads(index.read_text()).get("weight_map")
            if not isinstance(weight_map, dict) or not weight_map:
                raise ValueError()
            for name in weight_map.values():
                if not isinstance(name, str) or Path(name).name != name or not name.endswith(".safetensors"):
                    raise ValueError()
                required.append(path / name)
        for item in required + weights:
            if not item.is_file() or not item.resolve(strict=True).is_relative_to(path):
                raise ValueError()
        return str(path)
    except (OSError, ValueError, TypeError, AttributeError):
        raise ValueError("a complete local Qwen3 AWQ model is required under /models") from None


def engine_arguments(path):
    return {
        "model": path, "tokenizer": path, "trust_remote_code": False,
        "load_format": "safetensors", "quantization": "awq", "dtype": "half",
        "tensor_parallel_size": 1, "pipeline_parallel_size": 1,
        "distributed_executor_backend": "mp", "max_model_len": CONTEXT_LIMIT,
        "max_num_seqs": 2, "max_num_batched_tokens": CONTEXT_LIMIT,
        "gpu_memory_utilization": 0.85, "swap_space": 0,
        "enable_lora": False, "disable_log_requests": True, "disable_log_stats": True,
        "enforce_eager": True,
    }


class VLLMEngine:
    def __init__(self, path):
        # Hard-set before importing model libraries; deployment environment
        # cannot silently re-enable network downloads or telemetry.
        for key, value in {
            "HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1",
            "HF_HUB_DISABLE_TELEMETRY": "1", "DO_NOT_TRACK": "1",
            "VLLM_NO_USAGE_STATS": "1", "VLLM_LOGGING_LEVEL": "WARNING",
            "VLLM_WORKER_MULTIPROC_METHOD": "spawn",
        }.items():
            os.environ[key] = value
        from transformers import AutoTokenizer
        from vllm import AsyncLLMEngine, SamplingParams
        from vllm.engine.arg_utils import AsyncEngineArgs

        path = model_directory(path)
        self.tokenizer = AutoTokenizer.from_pretrained(path, local_files_only=True, trust_remote_code=False)
        self.sampling_class = SamplingParams
        self.engine = AsyncLLMEngine.from_engine_args(AsyncEngineArgs(**engine_arguments(path)))

    async def generate(self, token_ids, max_tokens, request_id):
        params = self.sampling_class(max_tokens=max_tokens, n=1, temperature=0.7, top_p=0.8, top_k=20)
        final = None
        # Tokens are already rendered with the local tokenizer's hard-disabled
        # thinking template. No user template, tool parser or URL loader exists.
        async for output in self.engine.generate({"prompt_token_ids": list(token_ids)}, params, request_id):
            final = output
        if final is None or not final.outputs:
            raise RuntimeError("engine returned no result")
        answer = final.outputs[0]
        return {"text": answer.text, "completion_tokens": len(answer.token_ids), "finish_reason": answer.finish_reason}

    async def abort(self, request_id):
        await self.engine.abort(request_id)

    async def check_health(self):
        # V1's v0.8.5 check_health only logs. Both V0 and V1 expose these
        # public properties; V0 may legitimately be idle before its first call.
        self._check_state()
        await self.engine.check_health()
        self._check_state()

    def _check_state(self):
        if self.engine.errored or self.engine.is_stopped:
            raise RuntimeError("engine_unhealthy")

    def close(self):
        # v0.8.5 may select V0 or V1 internally. Both have a lifecycle shutdown;
        # the controller still owns pod termination and GPU resource release.
        shutdown = getattr(self.engine, "shutdown_background_loop", None) or getattr(self.engine, "shutdown", None)
        if shutdown is not None:
            shutdown()
