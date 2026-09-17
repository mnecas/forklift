#!/usr/bin/env bash
# Create the mutual-TLS secret CopyAppliance expects in the controller namespace.
#
# Usage:
#   ./hack/copy-appliance/create-tls-secret.sh [namespace] [secret-name]
#
# Requires: certtool (gnutls-utils), kubectl, oc (for openshift internal registry only if pushing image)

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
namespace="${1:-openshift-mtv}"
secret_name="${2:-copy-appliance-tls}"
work="$(mktemp -d)"

trap 'rm -rf "${work}"' EXIT

"${repo_root}/pkg/nbd-container/hack/gen-certs.sh" "${work}" nbd-server

kubectl create secret generic "${secret_name}" \
  --namespace="${namespace}" \
  --from-file=ca-cert.pem="${work}/ca-cert.pem" \
  --from-file=server-cert.pem="${work}/server-cert.pem" \
  --from-file=server-key.pem="${work}/server-key.pem" \
  --from-file=client-cert.pem="${work}/client-cert.pem" \
  --from-file=client-key.pem="${work}/client-key.pem" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "Created secret ${namespace}/${secret_name}"
