#!/usr/bin/env bash
# Copy a locally built nbd-orchestrator to a copy-appliance guest and restart it.
set -euo pipefail

address="${1:?appliance IP required}"
binary="${2:-/tmp/nbd-orchestrator}"
namespace="${3:-openshift-mtv}"
provider="${4:-vsphere-q01}"

test -x "${binary}"

secret="toehold-ssh-keys-${provider}-private"
tmpdir=$(mktemp -d)
trap 'rm -rf "${tmpdir}"' EXIT

oc get secret "${secret}" -n "${namespace}" -o jsonpath='{.data.private-key}' | base64 -d > "${tmpdir}/private-key"
chmod 600 "${tmpdir}/private-key"

scp -i "${tmpdir}/private-key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "${binary}" "root@${address}:/tmp/nbd-orchestrator.new"

ssh -i "${tmpdir}/private-key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "root@${address}" 'systemctl stop nbd-orchestrator; install -m 755 /tmp/nbd-orchestrator.new /usr/local/bin/nbd-orchestrator; systemctl start nbd-orchestrator && sleep 5 && journalctl -u nbd-orchestrator --no-pager -n 15 && podman ps -a && ss -tlnp | grep -E "8443|10809" || true'
