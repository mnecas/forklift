# Toehold controller cluster prerequisites

The toehold build Job requires:

- OpenShift worker nodes with hostPath directories:
  - `/var/tmp/nbdkit-bib-store`
  - `/var/tmp/nbdkit-bib-rpmmd`
  - `/var/tmp/nbdkit-bib-containers`
- Privileged SCC for the `toehold-builder` service account in the **same namespace as the Toehold CR**:
  - `oc apply -f deploy/toehold/cluster-prep.yaml -n <toehold-namespace>`
- `TOEHOLD_UPLOADER_IMAGE` and `TOEHOLD_BIB_IMAGE` on the forklift-controller deployment
  (set automatically via OLM `relatedImages` when installed from the operator bundle)
- Build the uploader image locally: `make build-toehold-uploader-image push-toehold-uploader-image`
- A vSphere `Provider` CR with credentials and SSH key secrets (`offload-ssh-keys-<provider>-public` and `-private`)
- For NBD exports: `spec.disks` with existing VMDK paths to attach, plus guest SSH access on the appliance VM

See `operator/config/samples/forklift_v1beta1_toehold.yaml` for a sample `Toehold` CR.

**Namespace:** Create the Toehold CR in the namespace where the build Job runs (for example `toehold-e2e`). The Provider reference can point at another namespace (`spec.provider.namespace`). The build Job and vCenter creds Secret are owned by the Toehold CR and are garbage-collected when it is deleted.

**Recovery:** If a Toehold reaches `Failed` phase, the controller stops reconciling. Delete the CR and re-apply to retry (or set `spec.forceRebuild: true` on a fresh CR).

**Downstream CDI:** Toehold publishes `status.nbd[].uri` for migration disks; Plan/CDI importer wiring is a separate follow-up.

## E2E testing

Lab e2e guide for the nike09 OpenShift cluster + vCenter 10.37.160.51:
[`docs/toehold-e2e-nike09.md`](toehold-e2e-nike09.md)
