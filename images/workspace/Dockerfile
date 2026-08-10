# Interactive debug workspace image.
#
# The dev RayCluster head starts `ray start` and then execs JupyterLab, so the
# image must actually contain jupyterlab — the plain Ray image does not, and the
# head crash-loops with "exec: jupyter: not found".
ARG RAY_BASE_IMAGE=rayproject/ray:2.35.0-py310
FROM ${RAY_BASE_IMAGE}

ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple

USER root
RUN pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      jupyterlab==4.2.5 \
      notebook==7.2.2 \
      ipywidgets==8.1.5 \
 && chown -R ray:users /home/ray

USER ray
WORKDIR /home/ray

# The platform proxies JupyterLab through the API, so it is served on a base
# path and must not open a browser or demand its own token.
ENV JUPYTER_ALLOW_INSECURE_WRITES=1
EXPOSE 8888
