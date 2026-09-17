#!/usr/bin/env bash
# Watch CopyAppliance phase, export request convergence, and exports.
set -euo pipefail

name="${1:?usage: watch-copyappliance.sh <name> [namespace]}"
namespace="${2:-openshift-mtv}"
interval="${3:-3}"

watch -n "${interval}" "oc get copyappliance '${name}' -n '${namespace}' -o json | jq -r '
  \"phase: \" + (.status.phase // \"<none>\"),
  \"ready: \" + ((.status.conditions[]? | select(.type==\"Ready\") | .status) // \"<none>\"),
  \"spec.exportRequest: \" + (.spec.exportRequest | tostring),
  \"status.observedExportRequest: \" + (.status.observedExportRequest | tostring),
  \"exports: \" + ((.status.exports | length) | tostring),
  (.status.exports[]? | \"  \" + .vmdkPath + \" port=\" + (.port|tostring))
'"
