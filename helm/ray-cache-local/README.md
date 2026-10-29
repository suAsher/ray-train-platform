# Optional node-local cache

This chart provisions disposable per-PVC cache directories. Never use these
volumes for checkpoints or the only copy of a dataset. It is not required by the
base RayTrain installation.

Before installing, resolve and review the actual multi-architecture digest of
each upstream image, for example:

```bash
docker buildx imagetools inspect docker.io/rancher/local-path-provisioner:v0.0.32
docker buildx imagetools inspect docker.io/library/busybox:1.36.1
docker buildx imagetools inspect quay.io/prometheus/node-exporter:v1.9.1
```

Put the resulting `repository@sha256:...` references in your private values file
under `images.provisioner`, `images.helper`, and (when monitoring is enabled)
`monitoring.nodeExporter.image` and `monitoring.collector.image`. Empty defaults
are intentional: mutable tags are rejected. The files under `tests/` contain
nonexistent render fixtures and must **never** be used for installation.

Edit `values-example-data1.yaml` and `values-example-data2.yaml` to match your
actual nodes and mounted cache roots. Defaults deny provisioning on unlisted
nodes. Inspect `helm template` output before installing each disk profile.
The provisioner's cleanup helpers can delete only approved PVC child paths;
keep `cache.allowedRoots` aligned with `nodePathMap`.
