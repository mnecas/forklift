#!/usr/bin/env bash
# Verify shared lab prerequisites for copy appliance testing.
set -euo pipefail

namespace="${1:-openshift-mtv}"
provider="${2:-}"

fail=0
ok() { echo "OK  $*"; }
warn() { echo "WARN $*"; }
bad() { echo "FAIL $*"; fail=1; }

command -v oc >/dev/null || { bad "oc not found"; exit 1; }

echo "==> ForkliftController toehold + copy appliance settings (${namespace})"
fc_toehold=false
fc_image=false
fc_tls=false
fc_pool=false
if fc_yaml="$(oc get forkliftcontroller -n "${namespace}" -o yaml 2>/dev/null)"; then
	echo "${fc_yaml}" | grep -q 'feature_toehold:.*true' && fc_toehold=true
	echo "${fc_yaml}" | grep -q 'copy_appliance_container_image:' && fc_image=true
	echo "${fc_yaml}" | grep -q 'copy_appliance_tls_secret:' && fc_tls=true
	echo "${fc_yaml}" | grep -q 'copy_appliance_resource_pool:' && fc_pool=true
else
	warn "cannot read ForkliftController in ${namespace}"
fi
deploy_env="$(oc get deployment forklift-controller -n "${namespace}" -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}' 2>/dev/null || true)"
[[ "${deploy_env}" == *"FEATURE_TOEHOLD=true"* ]] && fc_toehold=true
[[ "${deploy_env}" == *"COPY_APPLIANCE_CONTAINER_IMAGE="* ]] && [[ "${deploy_env}" != *"COPY_APPLIANCE_CONTAINER_IMAGE=\n"* ]] && fc_image=true
[[ "${deploy_env}" == *"COPY_APPLIANCE_TLS_SECRET="* ]] && fc_tls=true
[[ "${deploy_env}" == *"COPY_APPLIANCE_RESOURCE_POOL="* ]] && fc_pool=true
${fc_toehold} && ok "feature_toehold enabled" || bad "feature_toehold not true"
${fc_image} && ok "copy_appliance_container_image set" || bad "copy_appliance_container_image missing"
${fc_tls} && ok "copy_appliance_tls_secret set" || bad "copy_appliance_tls_secret missing"
${fc_pool} && ok "copy_appliance_resource_pool set" || warn "copy_appliance_resource_pool not set (set on CopyAppliance.spec.resourcePool instead)"

echo "==> TLS secret"
if oc get secret copy-appliance-tls -n "${namespace}" >/dev/null 2>&1; then
	ok "secret copy-appliance-tls exists"
else
	bad "secret copy-appliance-tls missing — run create-tls-secret.sh"
fi

echo "==> ImageStream (copy-appliance:latest or nbd-container tag from controller)"
img_tag=""
for candidate in copy-appliance:latest nbd-container:toehold-dev-amd64 nbd-container:toehold-diskid-e2e-amd64; do
	if ref="$(oc get imagestreamtag "${candidate}" -n "${namespace}" -o jsonpath='{.image.dockerImageReference}' 2>/dev/null)"; then
		img_tag="${candidate}"
		if [[ "${ref}" == image-registry.openshift-image-registry.svc:* ]]; then
			ok "ImageStreamTag ${candidate} resolves locally: ${ref}"
		else
			warn "ImageStreamTag ${candidate} may not be Local: ${ref}"
		fi
		break
	fi
done
if [[ -z "${img_tag}" ]]; then
	# Fall back to whatever COPY_APPLIANCE_CONTAINER_IMAGE names.
	controller_img="$(echo "${deploy_env}" | awk -F= '/^COPY_APPLIANCE_CONTAINER_IMAGE=/{print $2}')"
	if [[ -n "${controller_img}" ]] && ref="$(oc get imagestreamtag "${controller_img}" -n "${namespace}" -o jsonpath='{.image.dockerImageReference}' 2>/dev/null)"; then
		ok "ImageStreamTag ${controller_img}: ${ref}"
	else
		bad "no usable copy-appliance / nbd-container ImageStreamTag found"
	fi
fi

echo "==> ToeholdTemplate"
if [[ -n "${provider}" ]]; then
	th_name="${provider}-toehold"
	if oc get toeholdtemplate "${th_name}" -n "${namespace}" >/dev/null 2>&1; then
		phase="$(oc get toeholdtemplate "${th_name}" -n "${namespace}" -o jsonpath='{.status.phase}')"
		if [[ "${phase}" == "Succeeded" ]]; then
			ok "ToeholdTemplate ${th_name} phase=${phase}"
		else
			bad "ToeholdTemplate ${th_name} phase=${phase} (want Succeeded)"
		fi
	else
		warn "ToeholdTemplate ${th_name} not found (T1 can still use an existing vCenter template path in CopyAppliance.spec.template)"
	fi
else
	oc get toeholdtemplates -n "${namespace}" 2>/dev/null || warn "no toeholdtemplates (pass provider name as 2nd arg)"
fi

echo "==> SSH keys"
if [[ -n "${provider}" ]]; then
	if oc get secret "toehold-ssh-keys-${provider}-private" -n "${namespace}" >/dev/null 2>&1; then
		ok "toehold-ssh-keys-${provider}-private"
	else
		bad "toehold-ssh-keys-${provider}-private missing"
	fi
else
	oc get secret -n "${namespace}" 2>/dev/null | grep toehold-ssh-keys || warn "pass provider name to check SSH secrets"
fi

echo "==> Controller deployment (custom build)"
img="$(oc get deployment forklift-controller -n "${namespace}" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)"
if [[ -n "${img}" ]]; then
	ok "forklift-controller image: ${img}"
	warn "confirm this image includes export/reattach code from your branch"
else
	bad "forklift-controller deployment not found"
fi

if [[ "${fail}" -ne 0 ]]; then
	echo "Prerequisite check failed."
	exit 1
fi
echo "Prerequisite check passed."
