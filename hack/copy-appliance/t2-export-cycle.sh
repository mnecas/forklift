#!/usr/bin/env bash
# T2 — release then re-export cycle (warm reattach without a migration plan).
set -euo pipefail

name="${1:?usage: t2-export-cycle.sh <copyappliance-name> [namespace] [cycles]}"
namespace="${2:-openshift-mtv}"
cycles="${3:-1}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

phase="$(oc get copyappliance "${name}" -n "${namespace}" -o jsonpath='{.status.phase}')"
if [[ "${phase}" != "DeployCompleted" ]]; then
	echo "CopyAppliance must be DeployCompleted before T2 (current: ${phase})" >&2
	exit 1
fi

for i in $(seq 1 "${cycles}"); do
	echo "==> Cycle ${i}/${cycles}: Release"
	"${script_dir}/export-request.sh" Release "${name}" "${namespace}"
	until [[ "$(oc get copyappliance "${name}" -n "${namespace}" -o jsonpath='{.status.phase}')" == "Released" ]]; do
		sleep 3
		echo "  waiting for Released..."
	done
	echo "  Released OK"

	echo "==> Cycle ${i}/${cycles}: Export"
	"${script_dir}/export-request.sh" Export "${name}" "${namespace}"
	until [[ "$(oc get copyappliance "${name}" -n "${namespace}" -o jsonpath='{.status.phase}')" == "DeployCompleted" ]]; do
		sleep 3
		echo "  waiting for DeployCompleted..."
	done
	echo "  Export OK"
done

echo "T2 passed (${cycles} cycle(s))."
