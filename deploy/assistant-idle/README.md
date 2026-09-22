# RayTrain assistant idle inference reference

This directory is a standalone reference for the idle-GPU assistant inference controller. It is not included by the production Helm chart and must not be applied as-is.

Safety defaults:

- Replace every `PLACEHOLDER_*` value before rendering or applying.
- Create or copy the image pull Secret in the assistant namespace before applying these manifests. The reference config uses `ImagePullSecrets: ["harbor-registry"]` for Ray head/worker pods, and controller/reaper Deployments reference `imagePullSecrets` with `PLACEHOLDER_IMAGE_PULL_SECRET_NAME`. These manifests only reference an existing namespace Secret; they do not read, create, copy, or grant Harbor credentials.
- Prepare `ModelPVC` separately before enabling the controller. If the PVC uses `ebs-ssd` with RWO semantics, its zone and node affinity can constrain which GPU node may mount it; do not assume every allowed GPU node has the same model cache.
- Use a dedicated namespace for assistant inference resources. The included `ResourceQuota` caps that namespace at one requested GPU; this is a self-safety cap for the assistant namespace, not a change to any existing team quota.
- Keep the RayService suspended until the controller has verified idle capacity and the live CRD accepts the suspended RayService contract. The rendered RayService also sets `upgradeStrategy.type: None`, so recreate/upgrade waits for the old RayCluster and GPU to release instead of running two clusters.
- Do not change training ClusterQueues, RayJobs, RayClusters, tenant namespaces, quotas, or existing workloads. The included LocalQueue only references the existing `cluster-gpu-queue`.
- The controller and reaper run as separate ServiceAccounts. The controller owns only the fixed assistant RayService and a fixed Lease in its namespace. The reaper can only read the fixed Lease and get/delete the fixed RayService.
- The Serve worker mounts only the model PVC read-only at `/models`; the Ray head does not mount the model PVC, avoiding RWO multi-node conflicts. Neither pod mounts user or personal storage or a ServiceAccount token.
- Worker scheduling is restricted to an explicit node allowlist plus `platform.wellspiking.ai/gpu-pool=production`, `platform.wellspiking.ai/cache-ready=true`, and `accelerator=nvidia-rtx-4090`. The renderer rejects the dedicated tenant taint key `platform.wellspiking.ai/dedicated-tenant` as a tolerated taint and also requires that label to be absent.
- The generated RayService uses a ClusterIP head service. Ray dashboard/Serve REST stays enabled and binds 0.0.0.0 inside the cluster because KubeRay and Serve need it, but NetworkPolicy keeps it internal-only and there is no NodePort, LoadBalancer, or Ingress exposure. The backend may reach only the head Serve port 8000; KubeRay may reach only dashboard port 8265; Ray pods may talk only to fixed internal Ray ports and the controller gate.
- Replace `PLACEHOLDER_KUBERAY_OPERATOR_NAMESPACE` with the namespace that runs the KubeRay operator. The dashboard policy intentionally also pins that peer to the operator pod labels `app.kubernetes.io/name=kuberay-operator`, `app.kubernetes.io/component=kuberay-operator`, and `app.kubernetes.io/instance=kuberay`; do not split the namespaceSelector and podSelector into separate peers, because that would broaden access.
- Replace `PLACEHOLDER_KUBERNETES_API_SERVER_CIDR` with the actual API server endpoint range for your CNI before applying the egress policy. The placeholder is intentionally invalid.

Controller arguments expected by the runtime command:

```text
--config=/etc/assistant-idle/config.json --mode=controller|reaper --listen=:8080
```
