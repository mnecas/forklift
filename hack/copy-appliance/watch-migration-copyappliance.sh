#!/usr/bin/env bash
# Watch migration VM phase and linked CopyAppliance export state (T3/T4).
set -euo pipefail

migration="${1:?usage: watch-migration-copyappliance.sh <migration> [namespace] [target-namespace]}"
namespace="${2:-openshift-mtv}"
target_ns="${3:-}"

watch -n 5 bash -c "
set -euo pipefail
echo '=== Migration ${migration} (${namespace}) ==='
oc get migration '${migration}' -n '${namespace}' -o jsonpath='VM phase: {.status.vms[0].phase}{\"\\n\"}copyAppliance: {.status.vms[0].copyAppliance.name}{\"\\n\"}' 2>/dev/null || echo 'migration not found'
ca=\$(oc get migration '${migration}' -n '${namespace}' -o jsonpath='{.status.vms[0].copyAppliance.name}' 2>/dev/null || true)
if [[ -n \"\${ca}\" ]]; then
  echo '=== CopyAppliance \${ca} ==='
  oc get copyappliance \"\${ca}\" -n '${namespace}' -o jsonpath='phase: {.status.phase}{\"\\n\"}exportRequest: {.spec.exportRequest}{\"\\n\"}observed: {.status.observedExportRequest}{\"\\n\"}' 2>/dev/null || true
fi
if [[ -n '${target_ns}' ]]; then
  echo '=== DataVolume NBD annotations (${target_ns}) ==='
  oc get datavolume -n '${target_ns}' -o custom-columns=NAME:.metadata.name,NBD:.metadata.annotations.cdi\\.kubevirt\\.io/storage\\.import\\.vddk\\.nbdConnection 2>/dev/null || true
fi
"
