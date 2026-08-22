# DNS routing for VKE and IDC

The cluster uses split DNS rather than sending every query to one resolver:

- Volcengine service zones use the VKE VPC resolvers `100.96.0.2` and
  `100.96.0.3`.
- The existing CoreDNS root forwarder is retained for IDC and all other
  domains.

## Ordinary Pods

Apply the managed CoreDNS server blocks:

```bash
bash ops/dns/deploy-coredns-split-dns.sh --apply
bash ops/dns/deploy-coredns-split-dns.sh --check
```

The script is idempotent. If the CoreDNS rollout fails, it restores the
previous Corefile automatically. To remove only this project's managed blocks:

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
