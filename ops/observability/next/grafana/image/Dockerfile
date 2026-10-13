# Build on the existing builder only; deploy the resulting Harbor digest.
ARG GRAFANA_BASE=harbor.wellspiking.ai/hub/grafana/grafana:13.1.1@sha256:7cb8c64c4d57a57e734073f3cc94620adb24a0acb929bd80ba9f14017e3a975b
FROM ${GRAFANA_BASE}
USER root
RUN mkdir -p /opt/grafana/plugins && chown 472:0 /opt/grafana/plugins
USER 472
RUN grafana cli --pluginsDir /opt/grafana/plugins plugins install grafana-clickhouse-datasource 4.22.1
ENV GF_PATHS_PLUGINS=/opt/grafana/plugins
