"""Validate the real installed vLLM constructor without loading weights or GPU."""
import inspect
import tempfile

from vllm import AsyncLLMEngine
from vllm.engine.arg_utils import AsyncEngineArgs

from assistant_serve.engine import engine_arguments


if __name__ == "__main__":
    # vLLM resolves nonexistent paths as Hub IDs during argument construction.
    # Use an existing empty local directory; this test must not fetch a model.
    with tempfile.TemporaryDirectory() as model_path:
        args = engine_arguments(model_path)
        inspect.signature(AsyncEngineArgs).bind(**args)
        config = AsyncEngineArgs(**args)
    assert config.enable_prefix_caching is False
    assert config.enable_log_requests is False
    assert config.max_model_len == 8192 and config.max_num_seqs == 2
    assert config.cpu_offload_gb == 0
    for method in ("generate", "abort", "check_health", "shutdown"):
        assert callable(getattr(AsyncLLMEngine, method))
    print("ENGINE_API_SMOKE_OK")
