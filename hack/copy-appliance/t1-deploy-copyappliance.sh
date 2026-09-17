#!/usr/bin/env bash
# T1 — apply manual CopyAppliance and wait for DeployCompleted.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
manifest="${1:-${repo_root}/operator/config/samples/forklift_v1beta1_copyappliance.yaml}"
namespace="${NAMESPACE:-openshift-mtv}"

name="$(grep '^  name:' "${manifest}" | head -1 | awk '{print $2}')"
echo "Applying ${manifest} (name=${name})"
oc apply -f "${manifest}"

echo "Waiting for DeployCompleted (Ctrl+C to stop)..."
while true; do
	phase="$(oc get copyappliance "${name}" -n "${namespace}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")"
	ready="$(oc get copyappliance "${name}" -n "${namespace}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")"
	exports="$(oc get copyappliance "${name}" -n "${namespace}" -o jsonpath='{.status.exports}' 2>/dev/null || echo "")"
	echo "$(date -Is) phase=${phase} ready=${ready} exports=${exports}"
	if [[ "${phase}" == "DeployCompleted" && "${ready}" == "True" ]]; then
		echo "T1 passed."
		exit 0
	fi
	if [[ "${phase}" == "DeployFailed" ]]; then
		echo "T1 failed: DeployFailed" >&2
		exit 1
	fi
	sleep 5
done
