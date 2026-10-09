# Native certificate consumption

cert-manager renews each role's leaf Secret. Envoy consumes the files through SDS;
this document concerns applications that retain client or peer TLS material in
memory: ClickHouse Keeper, ClickHouse, Redis/Sentinel, Nightingale and Alertmanager
gossip. CloudNativePG has its own certificate reload path and is excluded.

A small, dedicated CronJob checks the fixed `rotation-policy.json` every 15
minutes. It starts **suspended**. Its Python image digest, generated StatefulSet
names, PDB names and all readiness conditions must be verified before enabling it.
Deploy the script and policy as a ConfigMap named `native-certificate-rotation`
with keys `rotation.py` and `rotation-policy.json`; the manifest does not embed
private certificates or bootstrap credentials.

The order is Keeper → ClickHouse → Redis/Sentinel → Nightingale → Alertmanager.
One Pod is evicted at a time using the Kubernetes `policy/v1` Eviction API and a
UID/resource-version precondition. Each group must have all expected replicas
Ready before each eviction. Its exact named PDB must cover exactly those Pods,
have current status, and permit a disruption. The code does not delete Pods,
modify StatefulSet/Deployment templates, bypass a PDB, or access training
namespaces. A stopped or expired Lease holder must reacquire ownership before any
mutation. API calls use the in-cluster CA and projected service-account token.

The first run treats every unrecorded Pod as unknown and replaces it serially.
After a replacement with a new UID is Ready, and the certificate snapshot remains
unchanged, the Pod receives an annotation containing only aggregate public
certificate hashes. A subsequent run skips matching Pods. A failed run resumes
from the annotated replacements; unknown existing Pods are not assumed to have
loaded a certificate. A blocked PDB, non-Ready replica, operator rollout,
certificate change during replacement, unexpected parallel replacement, Lease
loss or timeout stops the run without evicting another Pod.

The default 130-second stable-secret interval allows the normal kubelet Secret
cache/projection interval to settle before replacing an application. This is a
conservative operational safeguard, not proof of a remote application's TLS
state. Initial rollout acceptance must inspect native TLS connections/peer
membership and confirm that the new certificate is used. A Ready Pod alone is
not a cryptographic proof. The automated controller records a completed orderly
restart; it does not claim to perform protocol-level native certificate probing.

Only leaf renewal is automatic. A CA hash change on a previously recorded Pod
fails closed: CA replacement requires a separate overlapping-trust migration.
The controller never reads a signing-key Secret. Kubernetes GET of a leaf Secret
necessarily returns its private-key field, but the script does not inspect,
hash, persist or print that field; API error bodies are also suppressed. RBAC
permits GET only on the listed leaf Secrets. Pod-list/annotation/eviction rights
are restricted to the three new product namespaces because Kubernetes RBAC does
not support selector-based Pod permissions.

Enable only after the root deployment procedure verifies the policy against the
live objects, pins the image, confirms all PDB budgets, and runs a forced renewal
acceptance. Keep at least 30 days between renewal and expiry. Job failure must be
visible to the monitoring system so a blocked rotation is repaired before leaf
expiry. Suspension or a permanently blocked PDB means renewal will not reach
these native consumers automatically; do not describe that state as complete
rotation automation.
