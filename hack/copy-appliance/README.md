# Copy appliance integration testing

End-to-end checklist for toehold templates, SSH keys, copy appliances, export
release/reattach, and plan-driven migrations on OpenShift with a vSphere source
provider.

## Test tiers (quick reference)

| Tier | What | Script / command |
|------|------|------------------|
| T0 | Local unit tests | `./hack/copy-appliance/run-unit-tests.sh` |
| Prereqs | FC, TLS, ImageStream, toehold | `./hack/copy-appliance/check-prereqs.sh openshift-mtv <provider>` |
| T1 | Manual CopyAppliance deploy | `./hack/copy-appliance/t1-deploy-copyappliance.sh` |
| T2 | Release / reattach cycle | `./hack/copy-appliance/t2-export-cycle.sh <name> openshift-mtv` |
| T3 | Cold plan migration | Plan without `spec.warm`; watch with `watch-migration-copyappliance.sh` |
| T4 | Warm plan migration | [plan_warm_copyappliance sample](../../operator/config/samples/forklift_v1beta1_plan_warm_copyappliance.yaml) |

Coordinate with toehold template testing: when `ToeholdTemplate <provider>-toehold`
reaches `Succeeded`, use the same `template:` inventory path and provider SSH
secrets for T1 onward.

## Prerequisites

- OpenShift cluster with MTV/Forklift installed
- KubeVirt worker nodes (for toehold build pods)
- vSphere source provider connected and inventory ready
- `kubectl`, `oc`, `podman` or `docker`, `certtool` (`gnutls-utils`)

## 1. Enable toehold and copy appliance settings

Patch the `ForkliftController` for the feature gate and images (FQINs):

```yaml
spec:
  feature_toehold: "true"
  toehold_builder_image_fqin: quay.io/kubev2v/toehold-builder:latest
  toehold_base_disk_container_image: registry.redhat.io/rhel9/rhel-guest-image:latest
  copy_appliance_container_image: quay.io/kubev2v/nbd-container:latest
```

Set placement on the **vSphere Provider** (`spec.settings`):

```yaml
spec:
  settings:
    toeholdDatastore: datastore1
    toeholdFolder: /Datacenter/vm
    toeholdNetwork: "VM Network"
    copyApplianceResourcePool: /Datacenter/host/my-cluster/Resources
```

The controller deployment receives the images as `FEATURE_TOEHOLD`, `TOEHOLD_*`, and
`COPY_APPLIANCE_CONTAINER_IMAGE` environment variables. Placement is read from the Provider.

### Resource pool (appliance clone, not template)

The **toehold template** only defines the appliance shape (root disk, network, CPU/RAM). It is uploaded as a template object; the toehold uploader discovers a pool for the import internally.

The **copy appliance VM** is a **clone** of that template. The clone is placed in the resource pool named by either `CopyAppliance.spec.resourcePool` or `Provider.spec.settings.copyApplianceResourcePool` when the spec omits it.

Secrets are **not** configured manually. For each vSphere provider the controller creates:

- `toehold-ssh-keys-<provider>-public` — injected into the template by `virt-customize --ssh-inject`
- `toehold-ssh-keys-<provider>-private` — used by the CopyAppliance reconciler

The private secret also holds the mutual-TLS material the appliance serves its exports with,
generated per provider alongside the keys: `ca-cert.pem`, `server-cert.pem`, `server-key.pem`,
`client-cert.pem` and `client-key.pem`. The server certificate is issued for the logical name
`nbd-server` rather than an IP, because the appliance is cloned on demand and its address is
not known when the certificate is issued; the controller verifies that name when querying
exports.

## 2. Build and publish the nbd-container image

```bash
cd pkg/nbd-container
make image
# Tag and push to the cluster image stream, e.g.:
# oc tag --source=docker localhost/nbd-container:latest \
#   openshift-mtv/copy-appliance:latest
```

`copy_appliance_container_image` should be a fully-qualified pull spec (FQIN).
An ImageStreamTag in the controller namespace is still accepted.

### ImageStream `referencePolicy: Local` (required)

OpenShift ImageStream tags can set how other components resolve the pull spec:

| `referencePolicy.type` | `ImageStreamTag.image.dockerImageReference` | Example |
|------------------------|-----------------------------------------------|---------|
| **`Source`** (OpenShift default) | Original external location where the image was imported from | `registry.redhat.io/.../nbd-container@sha256:…` or `quay.io/...` |
| **`Local`** | Cluster internal registry mirror | `image-registry.openshift-image-registry.svc:5000/openshift-mtv/nbd-container@sha256:…` |

CopyAppliance **LoadImage** resolves the tag, then pulls layers from that reference using the controller pod’s **service account token** and **`service-ca.crt`**. That authentication only works against the **integrated OpenShift registry**, not Quay or `registry.redhat.io`.

So for copy-appliance:

- **Runtime:** no extra registry pull secret is needed if the tag resolves to the internal registry (`Local`).
- **Install time:** MTV still uses the namespace `pull-secret` once, to import the image from `registry.redhat.io` into the ImageStream. That is bootstrap only, not CopyAppliance deploy.
- **Do not use `Source`** on the copy-appliance / nbd-container tag unless LoadImage is changed to pull from the internal registry by digest regardless of `dockerImageReference`.

**Recommendation for MTV operator packaging**

1. Ship (or reconcile) an ImageStream in `openshift-mtv`, e.g. `copy-appliance:latest`, imported from `registry.redhat.io/migration-toolkit-virtualization/...`.
2. Set **`referencePolicy: Local`** on that tag.
3. Point `ForkliftController.spec.copy_appliance_container_image` at the FQIN
   (or ImageStreamTag).
4. Nothing to document for TLS: the certificates are generated per provider into `toehold-ssh-keys-<provider>-private`, alongside the SSH keys.

Example ImageStream fragment:

```yaml
apiVersion: image.openshift.io/v1
kind: ImageStream
metadata:
  name: copy-appliance
  namespace: openshift-mtv
spec:
  lookupPolicy:
    local: true
  tags:
    - name: latest
      from:
        kind: DockerImage
        name: registry.redhat.io/migration-toolkit-virtualization/mtv-nbd-container-rhel9:latest
      referencePolicy:
        type: Local
```

After import, confirm the tag resolves internally:

```bash
oc get imagestreamtag copy-appliance:latest -n openshift-mtv \
  -o jsonpath='{.image.dockerImageReference}{"\n"}'
# Must start with image-registry.openshift-image-registry.svc:5000/
```

**Orchestrator note:** `nbd-orchestrator` is not a separate container image. It is built into the forklift-controller image and copied to the appliance over SSH during Configure. Only the per-disk **nbd-container** image goes through LoadImage.

## 3. Verify toehold template build

After the vSphere provider is ready, the controller creates a `ToeholdTemplate` named `<provider>-toehold`.

```bash
oc get toeholdtemplates -n openshift-mtv
oc describe toeholdtemplate <provider>-toehold -n openshift-mtv
```

Wait for `PHASE=Succeeded`. The build pod mounts the SSH public key secret and runs `virt-customize --ssh-inject root:file:...`.

Confirm SSH secrets exist:

```bash
oc get secret -n openshift-mtv | grep toehold-ssh-keys
```

## 4. Create a CopyAppliance

The template inventory path is typically `/Datacenter/vm/<provider>-toehold` (folder + template name from the toehold CR).

```yaml
apiVersion: forklift.konveyor.io/v1beta1
kind: CopyAppliance
metadata:
  name: test-appliance
  namespace: openshift-mtv
spec:
  provider:
    name: vcenter
    namespace: openshift-mtv
  secret:
    name: toehold-ssh-keys-vcenter-private
    namespace: openshift-mtv
  containerImage: copy-appliance:latest
  # resourcePool: optional when Provider.spec.settings.copyApplianceResourcePool is set
  template: /Datacenter/vm/vcenter-toehold
  datastore: datastore1
  folder: /Datacenter/vm
  attachDisks:
    - vmdkPath: "[datastore1] source-vm/disk-0.vmdk"
      diskKey: 2000
      serial: "6000C297-7d53-fad7-e8b4-5194193802f7"
      capacity: 17179869184
```

When created via `copyappliance.Build()`, `secret` is set automatically from the provider name.

Watch deploy phases:

```bash
oc get copyappliance test-appliance -o jsonpath='{.status.phase}{"\n"}'
oc get copyappliance test-appliance -o jsonpath='{.status.addresses}{"\n"}'
oc get copyappliance test-appliance -o jsonpath='{.status.exports}{"\n"}'
```

Terminal phase: `DeployCompleted` with `Ready=True` and populated `status.exports`.

Initial deploy seeds `spec.exportRequest: {target: Export, generation: 1}` when
the CR is created by a migration plan. Manual CRs should set generation `1`
on first export after deploy if testing release/reattach (see §6).

```bash
chmod +x hack/copy-appliance/*.sh
./hack/copy-appliance/check-prereqs.sh openshift-mtv vcenter
./hack/copy-appliance/t1-deploy-copyappliance.sh
```

## 5. Export release and reattach (T2)

After `DeployCompleted`, exercise the warm precopy disk cycle without a
migration plan. The appliance VM stays powered on; only source VMDKs detach
and reattach.

**Release** (hot detach, clear exports):

```bash
./hack/copy-appliance/export-request.sh Release test-copy-appliance openshift-mtv
./hack/copy-appliance/watch-copyappliance.sh test-copy-appliance openshift-mtv
```

Pass: `status.phase=Released`, `observedExportRequest` matches
`{target: Release, generation: N}`, `status.exports` empty. In vCenter the
appliance VM remains powered on; source disks are detached; template root
disk remains.

**Re-export** (hot attach, orchestrator restart, refresh exports):

```bash
./hack/copy-appliance/export-request.sh Export test-copy-appliance openshift-mtv
```

Pass: phases `AttachDisks` → `WaitForAttachDisks` → `RestartOrchestrator` →
`WaitForExports` → `DeployCompleted`; exports repopulated (ports may change).

Run both steps in a loop:

```bash
./hack/copy-appliance/t2-export-cycle.sh test-copy-appliance openshift-mtv 2
```

## 6. Plan-driven migration (T3 cold / T4 warm)

**Cold (T3):** Use a normal plan (no `spec.warm`). With `feature_toehold` and
copy appliance settings, the controller creates one `CopyAppliance` per VM,
sets NBD URIs on DataVolumes, and tears down the CR at the end.

**Warm (T4):** Use `spec.warm: true` and CBT on the source VM. Sample:
`operator/config/samples/forklift_v1beta1_plan_warm_copyappliance.yaml`.

Expected warm precopy loop per VM:

1. Once: `CreateCopyAppliance` → `WaitForCopyAppliance` → `CreateDataVolumes`
2. Each cycle: `CopyDisks` → `ReleaseCopyAppliance` → `WaitForCopyApplianceReleased`
   → snapshot consolidation → `RefreshCopyAppliance` → `WaitForCopyAppliance`
   → snapshot + `CopyDisks`
3. Cutover: single `TeardownCopyAppliance`

Watch migration and linked CopyAppliance:

```bash
./hack/copy-appliance/watch-migration-copyappliance.sh <migration> openshift-mtv <target-namespace>
```

Verify DataVolume NBD annotations:

```bash
oc get datavolume -n <target-ns> \
  -o custom-columns=NAME:.metadata.name,NBD:.metadata.annotations.cdi\.kubevirt\.io/storage\.import\.vddk\.nbdConnection
```

**CDI note:** Disk copy requires a CDI build that reads
`cdi.kubevirt.io/storage.import.vddk.nbdConnection`. Appliance deploy can pass
without it; import may still use direct VDDK until CDI is updated.

## 7. Query exports manually (optional)

From a host with the client certs and `nbdc`/`curl`:

```bash
cd pkg/nbd-container
make certs HOST=<appliance-ip>
make query HOST=<appliance-ip>
```

## Troubleshooting

| Symptom | Check |
|---------|-------|
| Toehold build fails on SSH secret | Provider reconciled with `feature_toehold=true`? `toehold-ssh-keys-*-public` exists? |
| CopyAppliance stuck at `WaitForNetwork` | VMware Tools reporting guest IP? Template network matches vSphere port group? |
| CopyAppliance stuck at `Configure` / SSH errors | Template rebuilt after SSH keys were created? `forceRebuild: true` on ToeholdTemplate? |
| `WaitForExports` never completes | nbd-container image loaded? `toehold-ssh-keys-<provider>-private` holds the five `*.pem` keys? Firewall allows TCP 8443 and 10809+? |
| LoadImage fails with unauthorized / manifest errors | ImageStreamTag `dockerImageReference` pointing at Quay or `registry.redhat.io`? Set `referencePolicy: Local` and confirm internal registry URL. |
| CloneVM fails: resource pool not found | Set `Provider.spec.settings.copyApplianceResourcePool` to the real vCenter inventory path, or set `CopyAppliance.spec.resourcePool`. |
| Disk hash mismatch / rebuild loop | Expected when SSH keys are first added; let rebuild finish |
| Stuck in `ReleaseDisks` / `AttachDisks` | vSphere task failed? SCSI slots free on appliance? Source disk locked by snapshot? |
| `Released` but re-export fails at `WaitForExports` | Run `systemctl status nbd-orchestrator` on guest; firewall 8443 / 10809+ |
| Warm migration recreates appliance each cycle | Controller must include export/reattach code; itinerary should not teardown inside precopy loop |

## Rebuild template after SSH key changes

```bash
oc patch toeholdtemplate <provider>-toehold -n openshift-mtv \
  --type=merge -p '{"spec":{"forceRebuild":true}}'
```
