#!/usr/bin/env bash
# T0 — local unit tests for copy appliance and warm reattach integration.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"

echo "==> copyappliance controller"
go test ./pkg/controller/copyappliance/... -count=1

echo "==> warm copy appliance itinerary"
go test ./pkg/controller/plan/migrator/base/... -run CopyAppliance -count=1

echo "==> vsphere builder (copy appliance DataVolumes)"
go test ./pkg/controller/plan/adapter/vsphere/... -count=1 -ginkgo.focus "Copy appliance"

echo "T0 passed."
