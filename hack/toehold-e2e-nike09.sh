#!/usr/bin/env bash
# Toehold e2e helper for nike09 lab.
# Usage: ./hack/toehold-e2e-nike09.sh <setup|run|status|logs|teardown>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${TOEHOLD_E2E_ENV:-$ROOT/deploy/toehold/e2e-nike09.env}"

die() { echo "error: $*" >&2; exit 1; }

load_env() {
  [[ -f "$ENV_FILE" ]] || die "env file not found: $ENV_FILE (copy from deploy/toehold/e2e-nike09.env.example)"
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  : "${MTV_NAMESPACE:=openshift-mtv}"
  : "${TOEHOLD_TARGET_NS:=toehold-e2e}"
  : "${PROVIDER_NAME:=vsphere}"
}

oc_login() {
  if ! oc whoami &>/dev/null; then
    [[ -n "${OCP_API_URL:-}" && -n "${OCP_USER:-}" && -n "${OCP_PASSWORD:-}" ]] \
      || die "not logged in and OCP_API_URL/OCP_USER/OCP_PASSWORD not set in env file"
    oc login --insecure-skip-tls-verify -u "$OCP_USER" -p "$OCP_PASSWORD" "$OCP_API_URL"
  fi
  echo "logged in as $(oc whoami) on $(oc whoami --show-server)"
}

cmd_setup() {
  load_env
  oc_login

  echo "==> applying Toehold CRD"
  oc apply -f "$ROOT/operator/config/crd/bases/forklift.konveyor.io_toeholds.yaml"

  echo "==> creating target namespace $TOEHOLD_TARGET_NS"
  oc create namespace "$TOEHOLD_TARGET_NS" --dry-run=client -o yaml | oc apply -f -

  echo "==> applying cluster prep (privileged SCC + toehold-builder SA)"
  oc apply -f "$ROOT/deploy/toehold/cluster-prep.yaml" -n "$TOEHOLD_TARGET_NS"

  if [[ -n "${CONTROLLER_IMAGE:-}" ]]; then
    echo "==> patching forklift-controller image"
    oc -n "$MTV_NAMESPACE" set image deployment/forklift-controller main="$CONTROLLER_IMAGE"
  fi

  uploader="${TOEHOLD_UPLOADER_IMAGE:-}"
  if [[ -n "$uploader" ]]; then
    echo "==> setting TOEHOLD_UPLOADER_IMAGE on controller"
    oc -n "$MTV_NAMESPACE" set env deployment/forklift-controller \
      TOEHOLD_UPLOADER_IMAGE="$uploader" \
      TOEHOLD_BIB_IMAGE="${TOEHOLD_BIB_IMAGE:-quay.io/centos-bootc/bootc-image-builder:latest}"
  else
    echo "warning: TOEHOLD_UPLOADER_IMAGE not set in env file; controller may fail to start toehold reconciler"
  fi

  echo "==> waiting for controller rollout"
  oc -n "$MTV_NAMESPACE" rollout status deployment/forklift-controller --timeout=300s

  echo "==> checking provider and SSH key secrets"
  oc get provider "$PROVIDER_NAME" -n "$MTV_NAMESPACE" -o jsonpath='provider {.metadata.name}: {.status.conditions[?(@.type=="Ready")].status}{"\n"}'
  oc get secret -n "$MTV_NAMESPACE" \
    "offload-ssh-keys-${PROVIDER_NAME}-public" \
    "offload-ssh-keys-${PROVIDER_NAME}-private" \
    &>/dev/null || die "SSH key secrets missing for provider $PROVIDER_NAME"

  echo "setup complete"
}

cmd_run() {
  load_env
  oc_login
  echo "==> applying Toehold CR"
  oc apply -f "$ROOT/deploy/toehold/e2e-nike09.yaml"
  echo "==> watching toehold status (Ctrl-C to stop)"
  oc get toehold nbdkit-toehold -n "$TOEHOLD_TARGET_NS" -w
}

cmd_status() {
  load_env
  oc_login
  echo "==> Toehold CR"
  oc get toehold -n "$TOEHOLD_TARGET_NS" -o wide 2>/dev/null || echo "(no toehold CRs)"
  oc describe toehold nbdkit-toehold -n "$TOEHOLD_TARGET_NS" 2>/dev/null || true
  echo ""
  echo "==> Build Jobs in $TOEHOLD_TARGET_NS"
  oc get jobs -n "$TOEHOLD_TARGET_NS" -l forklift.konveyor.io/toehold=nbdkit-toehold 2>/dev/null || true
  echo ""
  echo "==> Controller toehold env"
  oc -n "$MTV_NAMESPACE" get deployment forklift-controller \
    -o jsonpath='{range .spec.template.spec.containers[0].env[?(@.name=~"TOEHOLD.*")]}{.name}={.value}{"\n"}{end}' 2>/dev/null || true
}

cmd_logs() {
  load_env
  oc_login
  job="$(oc get jobs -n "$TOEHOLD_TARGET_NS" -l forklift.konveyor.io/toehold=nbdkit-toehold -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)" \
    || die "no build job found in $TOEHOLD_TARGET_NS"
  echo "==> bootc-vmdk (initContainer)"
  oc logs -n "$TOEHOLD_TARGET_NS" "job/$job" -c bootc-vmdk --tail=80 2>/dev/null || echo "(not started yet)"
  echo ""
  echo "==> upload (main container)"
  oc logs -n "$TOEHOLD_TARGET_NS" "job/$job" -c upload --tail=80 2>/dev/null || echo "(not started yet)"
}

cmd_teardown() {
  load_env
  oc_login
  echo "==> deleting Toehold CR (owned Job and creds Secret are garbage-collected)"
  oc delete -f "$ROOT/deploy/toehold/e2e-nike09.yaml" --ignore-not-found
  echo "teardown complete (template/VM retained per spec.retainTemplate default)"
}

usage() {
  cat <<EOF
Usage: $0 <command>

Commands:
  setup     Apply CRD, namespace, cluster-prep, patch controller
  run       Apply e2e Toehold CR and watch
  status    Show Toehold CR, Job, and controller env
  logs      Tail build Job container logs
  teardown  Delete Toehold CR and build Job

Environment:
  TOEHOLD_E2E_ENV   path to env file (default: deploy/toehold/e2e-nike09.env)
EOF
}

main() {
  case "${1:-}" in
    setup)    cmd_setup ;;
    run)      cmd_run ;;
    status)   cmd_status ;;
    logs)     cmd_logs ;;
    teardown) cmd_teardown ;;
    *)        usage; exit 1 ;;
  esac
}

main "$@"
