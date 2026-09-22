# RayTrain assistant idle inference reference

This standalone reference is not included by the production Helm chart. Replace every `PLACEHOLDER_*` value and review the full server-side diff before applying. It remains disabled by default.

## Runtime and admission

The runtime config must explicitly select `runtimeType: "pod"`. Missing or unknown runtime types are rejected; a legacy RayService renderer remains for compatibility tests but is not used by these deployment manifests. The runtime is one ordinary Pod running `python3 -m assistant_serve.standalone`, with exactly 1 GPU, 4 CPU, 16Gi memory, no Ray process or Ray control ports.

The existing Kueue Pod integration must be enabled before activation. Verify its live configuration; do not change global Kueue settings as part of this deployment. The Pod is created with `kueue.x-k8s.io/queue-name`, `kueue.x-k8s.io/managed=true`, and the `kueue.x-k8s.io/admission` scheduling gate. Only Kueue removes that gate. The assistant controller cannot patch Pods or Workloads. Readiness requires an admitted Workload whose controller owner is this exact Pod name and UID, the gate removed, and Pod Ready. A matching label alone cannot grant admission. See [Kueue plain Pod behavior](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/).

The policy is unchanged: 10 minutes of verified idle capacity, training demand first, a gate lease of at most 3 seconds, 15 seconds of draining, and at most 1 hour per instance. Pod `activeDeadlineSeconds: 3600` adds a kubelet-side lifetime bound. The independent reaper deletes only the fixed owned Pod by UID precondition when the controller lease expires or the lifetime bound is exceeded. Orphan Workloads must finish before recreation.

The existing List+Watch observation cache covers RayJobs, RayClusters, Workloads, Pods and Nodes. A separate fixed HTTPS demand endpoint includes pending platform database training/workspace requests that have no Kueue object yet. It returns counts, `hasDemand`, and `observedAt`, never user records. A failed request, TLS/auth failure, stale observation, malformed response or cache failure closes the gate and reclaims the owned Pod; it is never interpreted as zero demand.

## Required prepared resources

- A dedicated `raytrain-assistant-` namespace. The ResourceQuota caps this namespace at one requested GPU; it does not change the local team's quota.
- An existing LocalQueue pointing to `cluster-gpu-queue`; do not modify existing training queues, priorities, preemption, jobs, storage or CNI.
- An existing image pull Secret. `ImagePullSecrets: ["harbor-registry"]` configures inference; controller/reaper `imagePullSecrets` references `PLACEHOLDER_IMAGE_PULL_SECRET_NAME`. No manifest exports, copies or creates Harbor credentials.
- `ModelPVC`, mounted read-only at `/models`. An `ebs-ssd` RWO PVC can constrain scheduling by zone and node affinity. Prepare the pinned image/model on the allowed worker nodes and retain the 10-minute startup budget.
- Worker node allowlist and existing production GPU/cache-ready/4090 labels. Dedicated tenant nodes and their taints are excluded. No hostPath, host network, privileged container, user storage, or ServiceAccount token is mounted into inference.
- Existing inference TLS Secret (`TLSSecretName`, keys `tls.crt`, `tls.key`), bearer Secret (`AuthSecretName`, key `token`), and controller CA Secret (`GateCASecretName`, key `ca.crt`). These are mounted read-only at `/run/assistant/tls`, `/run/assistant/auth`, `/run/assistant/gate-ca`. Inference runs as 1000:1000 with fsGroup 1000 and Secret mode 0440.
- Existing controller TLS Secret `PLACEHOLDER_CONTROLLER_TLS_SECRET_NAME`, mounted at `/run/assistant/tls`; controller runs as 65532 with fsGroup 65532. Certificate SAN must cover the controller Service DNS. Its `/gate` and `/status` endpoints are GET-only HTTPS on 8443.
- Existing demand credential Secret `PLACEHOLDER_DEMAND_AUTH_SECRET_NAME`, key `token`, mounted at `/run/assistant/demand`. It contains only the derived 64-character hex HMAC token, never the backend's root authentication key. The fixed demand URL is `https://raytrain.wellspiking.ai/api/v1/internal/assistant-idle/training-demand`; redirects are rejected, normal system certificate trust is required. Optional `demandCAFile` may reference an additional mounted CA. Tokens are not logged.

## Internal traffic

The static `assistant-idle-inference` ClusterIP Service selects `assistant-role=inference`, exposes only HTTPS 8443, and routes to the same Pod port. Certificate SAN must cover this Service's DNS. `POST /v1/chat/completions` requires the inference bearer token; no user prompt crosses plaintext HTTP. HTTPS `/livez` reports only engine health and is the Pod readiness probe, so readiness does not depend on an already-open gate. `/healthz` additionally checks the gate.

NetworkPolicy manifests contain no Ray ports. They are defense in depth; their presence is not evidence that the CNI enforces them. This design does not change CNI configuration. TLS validation and bearer authentication remain necessary even where policy is not enforced. Replace `PLACEHOLDER_KUBERNETES_API_SERVER_CIDR` with the observed API endpoint range/port, and `PLACEHOLDER_RAYTRAIN_BACKEND_HTTPS_CIDR` with the fixed HTTPS demand endpoint address. Keep exact endpoint rules, not a whole node subnet. DNS is permitted for service resolution.

The controller's read permissions observe existing workloads. Write permissions are limited to creation and fixed-name UID-checked deletion of its own namespace Pod plus its Lease. The reaper can only get/delete that fixed Pod and read its Lease. No Role may mutate user workloads or Kueue admission.

## Verification before enabling

Run controller `--mode=inspect` with the prepared config and credentials; it is read-only. Check the demand endpoint and TLS trust, then use only a separately authorized dedicated acceptance run to prove scheduling-gate removal and exact Workload Pod ownership/admission. Verify backend-to-inference TLS/auth, direct unauthenticated rejection, HTTPS gate access, demand-triggered draining/reclaim, lease-expiry reaping, and GPU release. Never restart or cancel a user's training for acceptance. Keep the assistant disabled if any required observation or admission evidence is missing.

Controller command: `--config=/etc/assistant-idle/config.json --mode=controller --listen=:8443`. Reaper command: `--config=/etc/assistant-idle/config.json --mode=reaper`.
