#!/usr/bin/env bash
# Rescan SCSI buses on a copy-appliance guest and restart nbd-orchestrator.
set -euo pipefail

address="${1:?appliance IP required}"
namespace="${2:-openshift-mtv}"
provider="${3:-vsphere-q01}"
secret="toehold-ssh-keys-${provider}-private"
tmpdir=$(mktemp -d)
trap 'rm -rf "${tmpdir}"' EXIT

oc get secret "${secret}" -n "${namespace}" -o jsonpath='{.data.private-key}' | base64 -d > "${tmpdir}/private-key"
chmod 600 "${tmpdir}/private-key"

ssh -i "${tmpdir}/private-key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "root@${address}" bash -s <<'REMOTE'
set -eux
for host in /sys/class/scsi_host/host*; do
  echo "- - -" > "${host}/scan"
done
sleep 2
lsblk
systemctl restart nbd-orchestrator
sleep 3
systemctl is-active nbd-orchestrator
journalctl -u nbd-orchestrator --no-pager -n 10
podman ps -a
REMOTE
