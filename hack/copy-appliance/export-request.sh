#!/usr/bin/env bash
# Patch CopyAppliance spec.exportRequest (Release or Export).
# Generation is auto-incremented from the current spec unless GENERATION is set.
set -euo pipefail

target="${1:?usage: export-request.sh <Release|Export> <copyappliance-name> [namespace]}"
name="${2:?usage: export-request.sh <Release|Export> <copyappliance-name> [namespace]}"
namespace="${3:-openshift-mtv}"

case "${target}" in
Release|Export) ;;
*)
	echo "target must be Release or Export" >&2
	exit 1
	;;
esac

current="$(oc get copyappliance "${name}" -n "${namespace}" -o jsonpath='{.spec.exportRequest.generation}' 2>/dev/null || echo 0)"
if [[ -z "${current}" || "${current}" == "null" ]]; then
	current=0
fi
generation="${GENERATION:-$((current + 1))}"

echo "Patching ${name}: target=${target} generation=${generation}"
oc patch copyappliance "${name}" -n "${namespace}" --type=merge -p "{
  \"spec\": {
    \"exportRequest\": {
      \"target\": \"${target}\",
      \"generation\": ${generation}
    }
  }
}"

echo "Watch with: hack/copy-appliance/watch-copyappliance.sh ${name} ${namespace}"
