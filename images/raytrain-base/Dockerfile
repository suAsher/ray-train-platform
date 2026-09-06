# Explicit opt-in build, never replaces existing training environments.
# This foundation contains dependencies and platform runtime, not training source.
FROM harbor.wellspiking.ai/guofeng.su/ray-train-pytorch-ray-train@sha256:5bfa41f517c911e45e9856690ca66886f0b9c793b32938baa1dc24e697dc5a1d
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
USER root
COPY images/raytrain-base/constraints.txt /opt/raytrain/constraints.txt
RUN python3 -c "import sys, torch; assert sys.version_info[:2] == (3, 10); assert torch.__version__.split('+')[0] == '2.4.1'; assert torch.version.cuda == '12.1'" \
 && python3 -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      --constraint /opt/raytrain/constraints.txt \
      'ray[default,train,tune]==2.58.0' pyarrow==25.0.1 mlflow-skinny==3.14.0 \
      google-api-core==2.34.0 googleapis-common-protos==1.75.2 opentelemetry-exporter-otlp==1.44.0
COPY images/workspace/raytrain-launch.py /usr/local/bin/raytrain-launch
COPY images/workspace/raytrain-managed /usr/local/bin/raytrain-managed
COPY images/workspace/raytrain_runtime /usr/local/lib/raytrain-platform/raytrain_runtime
COPY images/raytrain-base/selfcheck.py /usr/local/bin/raytrain-selfcheck
RUN chmod 0755 /usr/local/bin/raytrain-launch /usr/local/bin/raytrain-managed /usr/local/bin/raytrain-selfcheck \
 && python3 -c "import pathlib, site; pathlib.Path(site.getsitepackages()[0], 'raytrain_platform.pth').write_text('/usr/local/lib/raytrain-platform\\n', encoding='utf-8')" \
 && install -d -o ray -g users /workspace
ENV RAY_TRAIN_V2_ENABLED=1
ENV PLATFORM_RAY_VERSION=2.58.0
ENV RAY_USAGE_STATS_ENABLED=0
USER ray
WORKDIR /workspace
# Required CPU gate. This does not validate GPU drivers, NCCL or training code.
RUN raytrain-selfcheck
