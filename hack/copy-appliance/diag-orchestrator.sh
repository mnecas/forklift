#!/usr/bin/env bash
# Diagnose nbd-orchestrator on a copy-appliance guest (no secrets printed).
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
set -x
lsblk
echo '=== orchestrator ==='
systemctl status nbd-orchestrator --no-pager || true
journalctl -u nbd-orchestrator --no-pager -n 50 || true
echo '=== podman ==='
podman ps -a || true
echo '=== listeners ==='
ss -tlnp | grep -E '8443|10809' || true
REMOTE
