# Toehold e2e testing on nike09

Lab environment for end-to-end validation of the Toehold controller.

## Lab inventory

| Resource | Value |
|---|---|
| OpenShift API | `https://api.nike09.mtv.local:6443` |
| Console | `https://console-openshift-console.apps.nike09.mtv.local` |
| `/etc/hosts` | `10.37.160.79 api.nike09.mtv.local console-openshift-console.apps.nike09.mtv.local oauth-openshift.apps.nike09.mtv.local` |
| MTV namespace | `openshift-mtv` |
| vCenter | `https://10.37.160.51/sdk` (8.0.3) |
| Existing Provider CR | `vsphere` in `openshift-mtv` |
| Cluster nodes | 3 compact master+worker nodes (`nike09-master-{1,2,3}`) |

**Security:** store credentials in `deploy/toehold/e2e-nike09.env` (gitignored). Rotate any credentials that were shared in chat or tickets.

## What is already on nike09

- MTV operator **2.12.7** (stock release — does **not** include the Toehold controller)
- vSphere Provider `vsphere` pointing at `10.37.160.51` — Ready

You must deploy a **custom controller build** from `feature/toehold-controller` before testing.

## Phase 0 — Build and push images

From the feature branch:

```bash
export REGISTRY=quay.io REGISTRY_ORG=<your-org> REGISTRY_TAG=toehold-dev

make build-controller-image push-controller-image
make build-toehold-importer-image push-toehold-importer-image
```

## Phase 1 — Deploy toehold-enabled controller

### 1. Apply the Toehold CRD

```bash
oc apply -f operator/config/crd/bases/forklift.konveyor.io_toeholds.yaml
```

### 2. Patch the controller deployment

```bash
source deploy/toehold/e2e-nike09.env

oc -n openshift-mtv set image deployment/forklift-controller \
  main="${CONTROLLER_IMAGE}"

oc -n openshift-mtv set env deployment/forklift-controller \
  TOEHOLD_IMPORTER_IMAGE="${TOEHOLD_IMPORTER_IMAGE}" \
  TOEHOLD_BIB_IMAGE="quay.io/centos-bootc/bootc-image-builder:latest"

oc -n openshift-mtv rollout status deployment/forklift-controller
```

### 3. Prepare worker nodes for BIB hostPath caches

On **each** node (all three are schedulable workers on nike09):

```bash
for node in nike09-master-1 nike09-master-2 nike09-master-3; do
  oc debug node/"$node" -- chroot /host bash -c '
    mkdir -p /var/tmp/nbdkit-bib-store /var/tmp/nbdkit-bib-rpmmd /var/tmp/nbdkit-bib-containers
    chmod 1777 /var/tmp/nbdkit-bib-store /var/tmp/nbdkit-bib-rpmmd /var/tmp/nbdkit-bib-containers
  '
done
```

### 4. Cluster prep in target namespace

```bash
oc create namespace toehold-e2e --dry-run=client -o yaml | oc apply -f -
oc apply -f deploy/toehold/cluster-prep.yaml -n toehold-e2e
```

### 5. Registry pull secret for bootc image

If the bootc image is private:

```bash
oc create secret docker-registry nbdkit-appliance-quay \
  --docker-server=quay.io \
  --docker-username=<user> \
  --docker-password=<token> \
  -n toehold-e2e
```

### 6. Verify SSH key secrets for the vsphere provider

The controller injects the provider's SSH public key into the cloned VM cloud-init. Keys must exist:

```bash
oc get secret -n openshift-mtv offload-ssh-keys-vsphere-public offload-ssh-keys-vsphere-private
```

If missing, generate and create them (see vsphere-copy-offload-populator README).

## Phase 2 — Run the e2e test (template + VM only)

Verify vCenter inventory names match the manifest (`datastore`, `folder`, `network`):

```bash
# Optional: inspect provider inventory via MTV UI or kubectl-mtv
kubectl-mtv get provider vsphere -n openshift-mtv
```

Apply the Toehold CR:

```bash
oc apply -f deploy/toehold/e2e-nike09.yaml
```

Watch progress:

```bash
oc get toehold -n openshift-mtv -w
oc describe toehold nbdkit-toehold -n openshift-mtv
```

Expected stage progression:

```
EnsurePrerequisites → EnsureTemplate → BuildAndUpload → EnsureVM → CloneVM → ConfigureVM → Finished
```

Inspect the build Job:

```bash
oc get jobs -n toehold-e2e
oc logs -n toehold-e2e job/nbdkit-toehold-build -c bootc-vmdk    # BIB: bootc → VMDK
oc logs -n toehold-e2e job/nbdkit-toehold-build -c import       # importer: VMDK → vCenter template
```

Verify in vCenter (`https://10.37.160.51/ui`):

- Template `nbdkit-toehold` exists with content-hash annotation
- VM `nbdkit-toehold-01` is cloned and powered on

## Phase 3 — Reuse and rebuild tests

| Test | Action | Expected |
|---|---|---|
| Template reuse | Delete Toehold CR, re-apply same spec | `status.template.reused: true`, no build Job |
| Force rebuild | `spec.forceRebuild: true` | Old template/VM destroyed, new Job runs |
| Bootc image change | Update `spec.bootcImage` tag | `ToeholdRebuildRequired` condition, template rebuilt |

## Phase 4 — NBD disk export (optional)

Add `disks` and `nbd` sections to the Toehold spec (see `operator/config/samples/forklift_v1beta1_toehold.yaml`). Requires:

- Existing source VM + VMDK paths in vCenter
- Guest SSH reachable after power-on
- Provider SSH private key secret

## Automated setup script

```bash
cp deploy/toehold/e2e-nike09.env.example deploy/toehold/e2e-nike09.env
# edit e2e-nike09.env with credentials

./hack/toehold-e2e-nike09.sh setup    # namespace, cluster-prep, CRD, controller patch
./hack/toehold-e2e-nike09.sh run      # apply Toehold CR and watch
./hack/toehold-e2e-nike09.sh status   # print CR + Job status
./hack/toehold-e2e-nike09.sh logs     # tail build Job logs
./hack/toehold-e2e-nike09.sh teardown # delete Toehold CR and build Job
```

## Troubleshooting

| Symptom | Check |
|---|---|
| `failed to find environment variable TOEHOLD_IMPORTER_IMAGE` | Controller env vars not set |
| Build Job pending | Node selector, resource limits, privileged SCC on `toehold-builder` SA |
| BIB initContainer fails | hostPath dirs missing, KVM unavailable, registry auth |
| Importer fails | vCenter creds secret, datastore/folder/network names, govc in image |
| VM clone fails | Template not found, insufficient datastore space |
| SSH timeout after power-on | `offload-ssh-keys-vsphere-*` secrets, guest cloud-init, firewall |
