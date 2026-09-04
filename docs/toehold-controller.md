# Toehold controller cluster prerequisites

The toehold build Job requires:

- OpenShift worker nodes with hostPath directories:
  - `/var/tmp/nbdkit-bib-store`
  - `/var/tmp/nbdkit-bib-rpmmd`
  - `/var/tmp/nbdkit-bib-containers`
- Privileged SCC for the `toehold-builder` service account:
  - `oc apply -f deploy/toehold/cluster-prep.yaml -n <target-namespace>`
- `TOEHOLD_UPLOADER_IMAGE` and `TOEHOLD_BIB_IMAGE` on the forklift-controller deployment
  (set automatically via OLM `relatedImages` when installed from the operator bundle)
- Build the uploader image locally: `make build-toehold-uploader-image push-toehold-uploader-image`
- A vSphere `Provider` CR with credentials and SSH key secrets (`offload-ssh-keys-<provider>-public` and `-private`)
- For NBD exports: `spec.disks` with existing VMDK paths to attach, plus guest SSH access on the appliance VM

See `operator/config/samples/forklift_v1beta1_toehold.yaml` for a sample `Toehold` CR.
