#!/usr/bin/env bash
# Configure only the three IDC NFS names required by Ray Train Platform.
# Run as root on every GPU node after the node is joined to the cluster.
set -euo pipefail

readonly begin_marker="# BEGIN raytrain-idc-nfs"
readonly end_marker="# END raytrain-idc-nfs"

# Use systemd-resolved's stub resolver. Its existing split DNS routes Volc
# domains to VKE DNS. The exact hosts below prevent the VKE wildcard response
# from winning for these IDC NFS names in kubelet's host network namespace.
test -s /run/systemd/resolve/stub-resolv.conf
ln -sfn /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
systemctl restart systemd-resolved

cp /etc/hosts /etc/hosts.raytrain-idc.bak
sed -i "/${begin_marker}/,/${end_marker}/d" /etc/hosts
cat >>/etc/hosts <<'EOF'
# BEGIN raytrain-idc-nfs
192.168.116.24 storage.westwell-lab.info
192.168.116.25 storage.westwell-lab.info
192.168.116.26 storage.westwell-lab.info
192.168.116.19 nfs-mixed-yrfs.wellspiking.ai
192.168.116.20 nfs-mixed-yrfs.wellspiking.ai
192.168.116.21 nfs-mixed-yrfs.wellspiking.ai
192.168.117.19 nfs-flash-yrfs.wellspiking.ai
192.168.117.20 nfs-flash-yrfs.wellspiking.ai
192.168.117.21 nfs-flash-yrfs.wellspiking.ai
# END raytrain-idc-nfs
EOF

for host in storage.westwell-lab.info nfs-mixed-yrfs.wellspiking.ai nfs-flash-yrfs.wellspiking.ai; do
  printf '%s -> ' "${host}"
  getent ahostsv4 "${host}" | awk 'NR == 1 { print $1 }'
done
