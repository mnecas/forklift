#!/usr/bin/env bash
# Stream the nbd-container image to a copy-appliance guest (workaround when
# controller LoadImage fails). Sets status.loadedImage on success.
set -euo pipefail

address="${1:?appliance IP required}"
namespace="${2:-openshift-mtv}"
provider="${3:-vsphere-nfc}"
name="${4:-test-copy-appliance}"
tag="${5:-nbd-container:toehold-dev-amd64}"

secret="toehold-ssh-keys-${provider}-private"
tmpdir=$(mktemp -d)
trap 'rm -rf "${tmpdir}"' EXIT

oc get secret "${secret}" -n "${namespace}" -o jsonpath='{.data.private-key}' | base64 -d > "${tmpdir}/private-key"
chmod 600 "${tmpdir}/private-key"

ref="$(oc get imagestreamtag "${tag}" -n "${namespace}" -o jsonpath='{.image.dockerImageReference}')"
digest="${ref##*@}"
short="${digest#sha256:}"
short="${short:0:12}"
loaded="localhost/forklift-copy-appliance:${short}"

echo "Streaming ${ref} to ${address} as ${loaded}"
podman pull --tls-verify=false "${ref}" 2>/dev/null || podman pull "${ref}"
podman save "${ref}" | ssh -i "${tmpdir}/private-key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "root@${address}" "podman load"

ssh -i "${tmpdir}/private-key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "root@${address}" "podman image exists ${loaded}"

oc patch copyappliance "${name}" -n "${namespace}" --subresource=status --type=merge \
  -p "{\"status\":{\"loadedImage\":\"${loaded}\",\"phase\":\"Configure\"}}"
echo "Preload complete; controller should continue from Configure."
