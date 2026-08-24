# DNS routing for VKE and IDC

The cluster uses split DNS rather than sending every query to one resolver:

- Volcengine service zones use the VKE VPC resolvers `100.96.0.2` and
  `100.96.0.3`.
- The CoreDNS root forwarder is set explicitly to the IDC resolvers
  `192.168.110.61` and `192.168.111.63` for every other external domain.
  Do not leave the root forwarder on `/etc/resolv.conf`: VKE node resolvers can
  return public addresses for IDC-only services such as `gitlab.qomolo.com`.

## Ordinary Pods

Apply the managed CoreDNS server blocks:

```bash
bash ops/dns/deploy-coredns-split-dns.sh --apply
bash ops/dns/deploy-coredns-split-dns.sh --check
```

The script is idempotent. Its check verifies both sides of the split: the three
Volcengine zones use VKE DNS and the root zone uses both IDC resolvers. If the
CoreDNS rollout fails, it restores the previous Corefile automatically. To
remove the managed blocks and restore the root forwarder to
`/etc/resolv.conf`:

```bash
bash ops/dns/deploy-coredns-split-dns.sh --revert
```

## Host-networked FSX Agent

The VKE FSX controller currently renders Agent Pods with `hostNetwork: true`
and `dnsPolicy: ClusterFirst`. Those Pods inherit the node resolver and do not
use CoreDNS, even though the FSXSet template requests
`ClusterFirstWithHostNet`.

Run the node split-DNS script from the node-pool bootstrap process on every
real node that can host FSX/TOS-backed Pods:

```bash
sudo bash ops/storage/shanghai-data-transfer/50-configure-node-split-dns.sh --apply
sudo bash ops/storage/shanghai-data-transfer/50-configure-node-split-dns.sh --check
```

This creates one systemd-resolved routing-only drop-in. It does not add a
default `~.` route, so IDC domains continue to use the node's existing link
DNS. New node pools must run the same bootstrap before they are made
schedulable for storage-backed workloads.

Rollback:

```bash
sudo bash ops/storage/shanghai-data-transfer/50-configure-node-split-dns.sh --revert
```

The FSX DNS and mount DaemonSets in `ops/mlflow/35-fsx-health-probe.yaml`
continuously distinguish resolver failures, stale mounts, and unschedulable
probe Pods.

## CoreDNS placement

CoreDNS is shared cluster infrastructure and does not request a GPU device. It
prefers the CPU control-plane pool and may fall back to the production GPU pool,
but it must not run on a virtual node. The reconciler keeps two replicas and
sets requests to `250m` CPU and `256Mi` memory (limits `2` CPU / `1Gi`). These
reservations are above measured steady-state usage while avoiding the former
`2 CPU / 4Gi` per-pod reservation that blocked zone-bound Loki replicas.

```bash
bash ops/dns/reconcile-coredns-placement.sh --apply
bash ops/dns/reconcile-coredns-placement.sh --check
```
