#!/usr/bin/env bash
# Install the toehold-podman wrapper on a running copy-appliance guest over SSH.
# Requires podman already baked into /opt/toehold/appliance-root.
# Usage: fix-appliance-podman-remote.sh <appliance-ip> [namespace] [provider]
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
set -eux
test -x /opt/toehold/appliance-root/usr/bin/podman
install -m 755 /dev/stdin /usr/local/bin/toehold-podman <<'INNER'
#!/bin/bash
set -euo pipefail
root=/opt/toehold/appliance-root
conf="${root}/etc/containers"
export PATH="${root}/usr/bin:${root}/usr/sbin:${PATH}"
if [[ -x "${root}/usr/bin/conmon" ]]; then
  export CONMON_BINARY="${root}/usr/bin/conmon"
fi
exec env \
  LD_LIBRARY_PATH="${root}/usr/lib64:${root}/usr/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}" \
  CONTAINERS_STORAGE_CONF="${conf}/storage.conf" \
  CONTAINERS_CONF="${conf}/containers.conf" \
  CONTAINERS_POLICY="${conf}/policy.json" \
  "${root}/usr/bin/podman" --storage-driver vfs "$@"
INNER
ln -sfn /usr/local/bin/toehold-podman /usr/local/bin/podman
ln -sfn /usr/local/bin/toehold-podman /usr/bin/podman
podman --version
podman info --format 'driver={{.Store.GraphDriverName}}'
podman image exists localhost/forklift-copy-appliance:fe964d5ed8f0; echo missing_exit=$?
REMOTE

echo "toehold-podman wrapper installed on ${address}"
