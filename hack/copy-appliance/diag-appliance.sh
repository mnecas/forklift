#!/usr/bin/env bash
# Diagnose copy-appliance guest SSH/podman (no secrets printed).
set -euo pipefail

address="${1:?appliance IP required}"
namespace="${2:-openshift-mtv}"
provider="${3:-vsphere-nfc}"
secret="toehold-ssh-keys-${provider}-private"
tmpdir=$(mktemp -d)
trap 'rm -rf "${tmpdir}"' EXIT

oc get secret "${secret}" -n "${namespace}" -o jsonpath='{.data.private-key}' | base64 -d > "${tmpdir}/private-key"
chmod 600 "${tmpdir}/private-key"

ssh -i "${tmpdir}/private-key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "root@${address}" bash -s <<'REMOTE'
set -x
df -h /
ls -la /usr/bin/podman /usr/local/bin/podman 2>&1 || true
podman --version 2>&1 || true
podman info --format 'driver={{.Store.GraphDriverName}} graphroot={{.Store.GraphRoot}}' 2>&1 || true
podman images 2>&1 || true
REMOTE
