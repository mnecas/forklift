package vsphere

import (
	"context"
	"fmt"
	"path"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	planapi "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/plan"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	planbase "github.com/kubev2v/forklift/pkg/controller/plan/adapter/base"
	"github.com/kubev2v/forklift/pkg/controller/plan/util"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/vsphere"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/settings"
	core "k8s.io/api/core/v1"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	cdi "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MigrationDiskImports builds MigrationDiskImport CRs for warm migration disks.
func (r *Builder) MigrationDiskImports(vmRef ref.Ref, secret *core.Secret, template *api.MigrationDiskImport, vddkConfigMap *core.ConfigMap) (imports []api.MigrationDiskImport, err error) {
	vm := &model.VM{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		return nil, liberr.Wrap(err, "vm", vmRef.String())
	}

	url := r.Source.Provider.Spec.URL
	thumbprint := r.Source.Provider.Status.Fingerprint
	url, thumbprint, err = r.applyHostsConfig(vmRef, url, thumbprint)
	if err != nil {
		return nil, err
	}

	vddkImage := settings.GetVDDKImage(r.Source.Provider.Spec.Settings)
	uuid := vm.UUID
	if r.canUseInstanceUUID() && vm.InstanceUUID != "" {
		uuid = vm.InstanceUUID
	}

	selector := k8slabels.SelectorFromSet(map[string]string{
		"vmID":      vmRef.ID,
		"migration": string(r.Migration.UID),
		"isDV":      "true",
	})
	dvList := &cdi.DataVolumeList{}
	err = r.Destination.Client.List(context.TODO(), dvList, &client.ListOptions{
		Namespace:     r.Plan.Spec.TargetNamespace,
		LabelSelector: selector,
	})
	if err != nil {
		return nil, liberr.Wrap(err)
	}

	for _, dv := range dvList.Items {
		backingFile := dv.Annotations[planbase.AnnDiskSource]
		if backingFile == "" {
			continue
		}
		claimName := dv.Name
		if dv.Status.ClaimName != "" {
			claimName = dv.Status.ClaimName
		}

		mdi := template.DeepCopy()
		if mdi.ObjectMeta.Annotations == nil {
			mdi.ObjectMeta.Annotations = map[string]string{}
		}
		mdi.ObjectMeta.GenerateName = fmt.Sprintf("mdi-%s-", sanitizeName(claimName))
		mdi.ObjectMeta.Annotations[planbase.AnnDiskSource] = backingFile
		if dv.Annotations[planbase.AnnDiskIndex] != "" {
			mdi.ObjectMeta.Annotations[planbase.AnnDiskIndex] = dv.Annotations[planbase.AnnDiskIndex]
		}

		importUUID := uuid
		if labeledUUID := dv.Labels[planbase.LabelVMUUID]; labeledUUID != "" {
			importUUID = labeledUUID
		}

		transfer := api.DiskTransfer{
			Type: api.TransferVDDK,
			VDDK: &api.VDDKTransfer{
				URL:          url,
				UUID:         importUUID,
				BackingFile:  baseVolume(backingFile, true),
				Thumbprint:   thumbprint,
				SecretRef:    secret.Name,
				InitImageURL: vddkImage,
			},
		}
		if vddkConfigMap != nil {
			transfer.VDDK.ExtraArgsConfigMap = &core.ObjectReference{
				Name:      vddkConfigMap.Name,
				Namespace: vddkConfigMap.Namespace,
			}
		}

		mdi.Spec = api.MigrationDiskImportSpec{
			Target: core.ObjectReference{
				Name:      claimName,
				Namespace: r.Plan.Spec.TargetNamespace,
			},
			Transfer: transfer,
		}
		if tn := r.Plan.Spec.TransferNetwork; tn != nil {
			ref := path.Join(tn.Namespace, tn.Name)
			mdi.Spec.TransferNetwork = &ref
		}
		imports = append(imports, *mdi)
	}
	return imports, nil
}

func (r *Builder) ResolveMigrationDiskImportIdentifier(mdi *api.MigrationDiskImport) string {
	if src, ok := mdi.Annotations[planbase.AnnDiskSource]; ok {
		return baseVolume(src, r.Plan.IsWarm())
	}
	return mdi.Name
}

// SetMigrationDiskImportCheckpoints updates checkpoint state on MigrationDiskImport CRs.
func (r *Client) SetMigrationDiskImportCheckpoints(vmRef ref.Ref, precopies []planapi.Precopy, imports []api.MigrationDiskImport, final bool, hosts util.HostsFunc) (err error) {
	n := len(precopies)
	if n == 0 {
		return liberr.New("no precopies available for checkpoint update")
	}
	var previous planapi.Precopy
	current := precopies[n-1]
	if n >= 2 {
		previous = precopies[n-2]
	}
	changeIds := previous.DeltaMap()

	for i := range imports {
		mdi := &imports[i]
		backing := mdi.Annotations[planbase.AnnDiskSource]
		if backing == "" && mdi.Spec.Transfer.VDDK != nil {
			backing = mdi.Spec.Transfer.VDDK.BackingFile
		}
		alreadyExists := false
		for _, checkpoint := range mdi.Spec.Checkpoints {
			if checkpoint.Current == current.Snapshot {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			mdi.Spec.Checkpoints = append(mdi.Spec.Checkpoints, api.DiskCheckpoint{
				Current:  current.Snapshot,
				Previous: changeIds[backing],
			})
		}
		mdi.Spec.FinalCheckpoint = final
	}
	return nil
}

func sanitizeName(name string) string {
	out := make([]rune, 0, len(name))
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			out = append(out, c)
		} else if c >= 'A' && c <= 'Z' {
			out = append(out, c+('a'-'A'))
		}
	}
	if len(out) == 0 {
		return "disk"
	}
	return string(out)
}
