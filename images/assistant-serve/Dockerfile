# Required: an internally mirrored and digest-pinned vLLM base image.
# Candidate: official v0.29.0-cu129. This does not alter host GPU drivers.
# Dependency/security gates and real GPU acceptance are required before release.
ARG ASSISTANT_BASE_IMAGE
FROM ${ASSISTANT_BASE_IMAGE}

USER root
ARG ASSISTANT_BASE_IMAGE
ARG RAY_VERSION=2.58.0
ARG VLLM_VERSION=0.29.0
ARG TORCH_CUDA_VERSION=12.9
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple

COPY images/assistant-serve/requirements.txt /opt/assistant/requirements.txt
RUN ASSISTANT_BASE_REF="${ASSISTANT_BASE_IMAGE}" python3 -c 'import os, re; assert re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", os.environ["ASSISTANT_BASE_REF"]), "assistant base requires an immutable digest"' \
 && VLLM_EXPECTED="${VLLM_VERSION}" CUDA_EXPECTED="${TORCH_CUDA_VERSION}" \
    python3 -c 'import importlib.metadata as m, os, torch; from packaging.version import Version; assert Version(m.version("vllm")).public == os.environ["VLLM_EXPECTED"]; assert torch.version.cuda == os.environ["CUDA_EXPECTED"]' \
 && python3 -c 'import importlib.metadata as m; native={"vllm", "torch", "torchaudio", "torchvision", "torchcodec", "torch-c-dlpack-ext", "triton", "tilelang", "apache-tvm-ffi", "quack-kernels", "humming-kernels", "pynvvideocodec", "deep-ep", "nccl4py"}; names={d.metadata["Name"].lower().replace("_", "-"):d.version for d in m.distributions()}; pins=sorted(name+"=="+version for name,version in names.items() if name != "nvidia-nccl-cu12" and (name in native or name.startswith(("nvidia-", "flashinfer-", "cuda-", "nixl", "tokenspeed-", "cupy-")))); print("\n".join(pins))' > /opt/assistant/native-constraints.txt \
 && python3 -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      -c /opt/assistant/native-constraints.txt \
      "ray[serve]==${RAY_VERSION}" "vllm==${VLLM_VERSION}" -r /opt/assistant/requirements.txt \
 && python3 -m pip check \
 && RAY_EXPECTED="${RAY_VERSION}" VLLM_EXPECTED="${VLLM_VERSION}" CUDA_EXPECTED="${TORCH_CUDA_VERSION}" \
    python3 -c 'import importlib.metadata as m, os, torch; from packaging.version import Version; assert m.version("ray") == os.environ["RAY_EXPECTED"]; assert Version(m.version("ray")) >= Version("2.56.0"); assert Version(m.version("vllm")).public == os.environ["VLLM_EXPECTED"]; assert torch.version.cuda == os.environ["CUDA_EXPECTED"]' \
 && python3 -m pip freeze --all > /opt/assistant/resolved-requirements.txt \
 && mkdir -p /opt/assistant /models /home/assistant \
 && chown 1000:1000 /home/assistant

COPY images/assistant-serve/assistant_serve /opt/assistant/assistant_serve
COPY images/assistant-serve/tests /opt/assistant/tests
ENV PYTHONPATH=/opt/assistant \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    HOME=/home/assistant \
    HF_HOME=/tmp/huggingface \
    HF_HUB_OFFLINE=1 \
    TRANSFORMERS_OFFLINE=1 \
    HF_HUB_DISABLE_TELEMETRY=1 \
    VLLM_NO_USAGE_STATS=1 \
    VLLM_LOGGING_LEVEL=WARNING \
    VLLM_ENABLE_CUDA_COMPATIBILITY=0 \
    VLLM_WORKER_MULTIPROC_METHOD=spawn \
    RAY_USAGE_STATS_ENABLED=0 \
    DO_NOT_TRACK=1 \
    ASSISTANT_MODEL_PATH=/models/Qwen3-8B-AWQ
RUN python3 -m unittest discover -s /opt/assistant/tests -v \
 && python3 /opt/assistant/tests/ingress_smoke.py -v \
 && python3 /opt/assistant/tests/engine_api_smoke.py \
 && python3 -c 'import assistant_serve.app; from vllm import AsyncLLMEngine; from vllm.engine.arg_utils import AsyncEngineArgs'

USER 1000:1000
WORKDIR /opt/assistant
# Exercise imports with the runtime identity as well as the build identity.
RUN python3 -c 'import assistant_serve.app; from vllm import AsyncLLMEngine; from vllm.engine.arg_utils import AsyncEngineArgs'
EXPOSE 8000
ENTRYPOINT []
CMD ["python3", "-m", "assistant_serve.app"]
