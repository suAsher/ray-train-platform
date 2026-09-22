# Required: an internally mirrored and digest-pinned vLLM base image.
# Candidate for driver 550.127.05: official v0.8.5, CUDA 12.4.1, Python 3.12.
# The candidate still requires a real GPU acceptance run before deployment.
ARG ASSISTANT_BASE_IMAGE
FROM ${ASSISTANT_BASE_IMAGE}

USER root
ARG ASSISTANT_BASE_IMAGE
ARG RAY_VERSION=2.58.0
ARG VLLM_VERSION=0.8.5
ARG TORCH_CUDA_VERSION=12.4
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple

COPY images/assistant-serve/requirements.txt /opt/assistant/requirements.txt
RUN ASSISTANT_BASE_REF="${ASSISTANT_BASE_IMAGE}" python3 -c 'import os, re; assert re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", os.environ["ASSISTANT_BASE_REF"]), "assistant base requires an immutable digest"' \
 && python3 -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      "ray[serve]==${RAY_VERSION}" -r /opt/assistant/requirements.txt \
 && python3 -m pip check \
 && RAY_EXPECTED="${RAY_VERSION}" VLLM_EXPECTED="${VLLM_VERSION}" CUDA_EXPECTED="${TORCH_CUDA_VERSION}" \
    python3 -c 'import importlib.metadata as m, os, torch; assert m.version("ray") == os.environ["RAY_EXPECTED"]; assert m.version("vllm") == os.environ["VLLM_EXPECTED"]; assert torch.version.cuda == os.environ["CUDA_EXPECTED"]' \
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
    VLLM_WORKER_MULTIPROC_METHOD=spawn \
    RAY_USAGE_STATS_ENABLED=0 \
    DO_NOT_TRACK=1 \
    ASSISTANT_MODEL_PATH=/models/Qwen3-8B-AWQ
RUN python3 -m unittest discover -s /opt/assistant/tests -v \
 && python3 -c 'import assistant_serve.app; from vllm import AsyncLLMEngine; from vllm.engine.arg_utils import AsyncEngineArgs'

USER 1000:1000
WORKDIR /opt/assistant
EXPOSE 8000
ENTRYPOINT []
CMD ["python3", "-m", "assistant_serve.app"]
