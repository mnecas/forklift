package ocp

import (
	"context"
	"fmt"
	"strings"

	liberr "github.com/konveyor/forklift-controller/pkg/lib/error"
	libitr "github.com/konveyor/forklift-controller/pkg/lib/itinerary"
	export "kubevirt.io/api/export/v1alpha1"

	planapi "github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1/plan"
	"github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1/ref"
	"github.com/konveyor/forklift-controller/pkg/controller/plan/adapter/base"
	planbase "github.com/konveyor/forklift-controller/pkg/controller/plan/adapter/base"
	plancontext "github.com/konveyor/forklift-controller/pkg/controller/plan/context"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	cnv "kubevirt.io/api/core/v1"
	cdi "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Builder struct {
	*plancontext.Context
	macConflictsMap map[string]string
}

// ConfigMap implements base.Builder
func (r *Builder) ConfigMap(vmRef ref.Ref, secret *core.Secret, object *core.ConfigMap) error {
	vmExport := &export.VirtualMachineExport{}
	r.Log.Info("Fetching vmExport", "vmRef", vmRef)
	// TODO: check why the name includes the namespace too
	namespaceNameSplit := strings.Split(vmRef.Name, "/")
	vmName := namespaceNameSplit[1]

	key := client.ObjectKey{
		Namespace: vmRef.Namespace,
		Name:      vmName,
	}
	err := r.Client.Get(context.TODO(), key, vmExport)
	if err != nil {
		r.Log.Error(err, "Failed to get VM export ConfigMap")
		return liberr.Wrap(err)
	}

	object.Data = map[string]string{
		"ca.pem": vmExport.Status.Links.External.Cert,
	}

	return nil
}

// DataVolumes implements base.Builder
func (r *Builder) DataVolumes(vmRef ref.Ref, secret *core.Secret, configMap *core.ConfigMap, dvTemplate *cdi.DataVolume) (dvs []cdi.DataVolume, err error) {
	// Get VM export
	vmExport := &export.VirtualMachineExport{}

	// TODO: check why the name includes the namespace too
	namespaceNameSplit := strings.Split(vmRef.Name, "/")
	vmName := namespaceNameSplit[1]

	key := client.ObjectKey{
		Namespace: vmRef.Namespace,
		Name:      vmName,
	}

	err = r.Client.Get(context.TODO(), key, vmExport)
	if err != nil {
		r.Log.Error(err, "Failed to get VM export ConfigMap")
		return nil, liberr.Wrap(err)
	}

	// Create DataVolumes on the destination
	// TODO Get SC from map
	storageClass := "nfs-csi"

	dataVolumes := []cdi.DataVolume{}
	for _, volume := range vmExport.Status.Links.External.Volumes {
		// Get PVC
		pvc := &core.PersistentVolumeClaim{}
		err = r.Client.Get(context.TODO(), client.ObjectKey{Namespace: vmRef.Namespace, Name: volume.Name}, pvc)
		if err != nil {
			return nil, liberr.Wrap(err)
		}

		size := pvc.Spec.Resources.Requests["storage"]
		dataVolume := dvTemplate.DeepCopy()
		dataVolume.Annotations[planbase.AnnDiskSource] = fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name)

		url := getExportURL(volume.Formats)
		if url == "" {
			return nil, liberr.Wrap(fmt.Errorf("failed to get export URL, available formats: %v", volume.Formats))
		}

		dataVolume.Spec = *createDataVolumeSpec(size, storageClass, url, configMap.Name, secret.Name)

		err = r.Destination.Client.Create(context.TODO(), dataVolume, &client.CreateOptions{})
		if err != nil {
			if !k8serr.IsAlreadyExists(err) {
				r.Log.Error(err, "Failed to create DataVolume")
				return nil, liberr.Wrap(err)
			}
		}

		dv := &cdi.DataVolume{}
		err = r.Destination.Client.Get(context.TODO(), client.ObjectKey{Namespace: dataVolume.Namespace, Name: dataVolume.Name}, dv)
		if err != nil {
			return nil, liberr.Wrap(err)
		}
	}

	return dataVolumes, nil
}

func getExportURL(virtualMachineExportVolumeFormat []export.VirtualMachineExportVolumeFormat) (url string) {
	for _, format := range virtualMachineExportVolumeFormat {
		// TODO: revisit this, I'm not sure if it's correct
		if format.Format == export.KubeVirtGz || format.Format == export.ArchiveGz {
			return format.Url
		}
	}

	return ""
}

// PersistentVolumeClaimWithSourceRef implements base.Builder
func (*Builder) PersistentVolumeClaimWithSourceRef(da interface{}, storageName *string, populatorName string, accessModes []core.PersistentVolumeAccessMode, volumeMode *core.PersistentVolumeMode) *core.PersistentVolumeClaim {
	return nil
}

// PodEnvironment implements base.Builder
func (*Builder) PodEnvironment(vmRef ref.Ref, sourceSecret *core.Secret) (env []core.EnvVar, err error) {
	return nil, nil
}

// PreTransferActions implements base.Builder
func (r *Builder) PreTransferActions(c base.Client, vmRef ref.Ref) (ready bool, err error) {
	apiGroup := "kubevirt.io"

	// Check if VM export exists
	vmExport := &export.VirtualMachineExport{}
	err = r.Client.Get(context.Background(), client.ObjectKey{Namespace: vmRef.Namespace, Name: vmRef.Name}, vmExport)
	if err != nil {
		if !k8serr.IsNotFound(err) {
			r.Log.Error(err, "Failed to get VM export.", "vm", vmRef.Name)
			return true, liberr.Wrap(err)
		}
		// Create VM export
		vmExport = &export.VirtualMachineExport{
			TypeMeta: meta.TypeMeta{
				Kind:       "VirtualMachineExport",
				APIVersion: "kubevirt.io/v1alpha3",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      vmRef.Name,
				Namespace: vmRef.Namespace,
			},
			Spec: export.VirtualMachineExportSpec{
				Source: core.TypedLocalObjectReference{
					APIGroup: &apiGroup,
					Kind:     "VirtualMachine",
					Name:     vmRef.Name,
				},
			},
		}

		err = r.Client.Create(context.Background(), vmExport, &client.CreateOptions{})
		if err != nil {
			return true, liberr.Wrap(err)
		}
	}
	if vmExport.Status != nil && vmExport.Status.Phase == export.Ready {
		r.Log.Info("VM export is ready.", "vm", vmRef.Name)
		return true, nil
	}

	r.Log.Info("Waiting for VM export to be ready...", "vm", vmRef.Name)
	return false, nil
}

// ResolveDataVolumeIdentifier implements base.Builder
func (*Builder) ResolveDataVolumeIdentifier(dv *cdi.DataVolume) string {
	return dv.ObjectMeta.Annotations[planbase.AnnDiskSource]
}

// ResolvePersistentVolumeClaimIdentifier implements base.Builder
func (*Builder) ResolvePersistentVolumeClaimIdentifier(pvc *core.PersistentVolumeClaim) string {
	return ""
}

// Secret implements base.Builder
func (r *Builder) Secret(vmRef ref.Ref, in *core.Secret, object *core.Secret) error {
	vmExport := &export.VirtualMachineExport{}
	err := r.Client.Get(context.Background(), client.ObjectKey{Namespace: vmRef.Namespace, Name: vmRef.Name}, vmExport)
	if err != nil {
		r.Log.Error(err, "Failed to get VM export Secret")
		return liberr.Wrap(err)
	}

	// Export pod is ready
	// Create config maps with CA on the destination
	// Read secret token
	tokenSecret := &core.Secret{}
	err = r.Client.Get(context.Background(), client.ObjectKey{Namespace: vmRef.Namespace, Name: *vmExport.Status.TokenSecretRef}, tokenSecret)
	if err != nil {
		return liberr.Wrap(err)
	}

	// Create secret token header
	object.StringData = map[string]string{
		"token": fmt.Sprintf("x-kubevirt-export-token:%s", tokenSecret.Data["token"]),
	}

	return nil
}

// Tasks implements base.Builder
func (r *Builder) Tasks(vmRef ref.Ref) (list []*planapi.Task, err error) {
	vm := &cnv.VirtualMachine{}
	// TODO: check why the name includes the namespace too

	key := client.ObjectKey{
		Namespace: vmRef.Namespace,
		Name:      vmRef.Name,
	}

	r.Log.Info("Getting VM export", "key", key.Name, "namespace", key.Namespace)

	err = r.Client.Get(context.TODO(), key, vm)
	if err != nil {
		return nil, liberr.Wrap(err)
	}

	for _, vol := range vm.Spec.Template.Spec.Volumes {
		pvc := &core.PersistentVolumeClaim{}
		err = r.Client.Get(context.TODO(), client.ObjectKey{Namespace: vmRef.Namespace, Name: vol.PersistentVolumeClaim.ClaimName}, pvc)
		if err != nil {
			return nil, liberr.Wrap(err)
		}
		size := pvc.Spec.Resources.Requests["storage"]

		mB := size.Value() / 1024 / 1024
		list = append(
			list,
			&planapi.Task{
				Name: fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name),
				Progress: libitr.Progress{
					Total: mB,
				},
				Annotations: map[string]string{
					"unit": "MB",
				},
			})
	}

	return
}

// TemplateLabels implements base.Builder
func (*Builder) TemplateLabels(vmRef ref.Ref) (labels map[string]string, err error) {
	return nil, nil
}

// VirtualMachine implements base.Builder
func (r *Builder) VirtualMachine(vmRef ref.Ref, object *cnv.VirtualMachineSpec, persistentVolumeClaims []core.PersistentVolumeClaim) error {
	sourceVm := &cnv.VirtualMachine{}
	// TODO: figure out and remove
	namespaceNameSplit := strings.Split(vmRef.Name, "/")
	vmName := namespaceNameSplit[1]

	key := client.ObjectKey{
		Namespace: vmRef.Namespace,
		Name:      vmName,
	}

	err := r.Client.Get(context.TODO(), key, sourceVm)
	if err != nil {
		return liberr.Wrap(err)
	}

	targetVMspec := sourceVm.Spec

	// TODO: move logic to mapDisks
	pvcMap := make(map[string]*core.PersistentVolumeClaim)
	for i := range persistentVolumeClaims {
		pvc := &persistentVolumeClaims[i]
		if source, ok := pvc.Annotations[planbase.AnnDiskSource]; ok {
			pvcMap[source] = pvc
		}
	}

	diskMap := make(map[string]*cnv.Disk)
	for i := range sourceVm.Spec.Template.Spec.Domain.Devices.Disks {
		disk := &sourceVm.Spec.Template.Spec.Domain.Devices.Disks[i]
		// Find matching volume
		for _, vol := range sourceVm.Spec.Template.Spec.Volumes {
			if vol.Name == disk.Name {
				key := pvcSourceName(vmRef.Namespace, vol.PersistentVolumeClaim.ClaimName)
				if _, ok := pvcMap[key]; ok {
					diskMap[key] = disk
					continue
				}
			}
		}
	}

	// Clear original disks and volumes, will be required for other mapped devices later
	targetVMspec.Template.Spec.Domain.Devices.Disks = []cnv.Disk{}
	targetVMspec.Template.Spec.Volumes = []cnv.Volume{}

	for _, volume := range persistentVolumeClaims {
		if disk, ok := diskMap[volume.Annotations[planbase.AnnDiskSource]]; ok {
			targetVolume := cnv.Volume{
				Name: disk.Name,
				VolumeSource: cnv.VolumeSource{
					PersistentVolumeClaim: &cnv.PersistentVolumeClaimVolumeSource{
						PersistentVolumeClaimVolumeSource: core.PersistentVolumeClaimVolumeSource{
							ClaimName: volume.Name,
						},
					},
				},
			}
			targetVMspec.Template.Spec.Volumes = append(targetVMspec.Template.Spec.Volumes, targetVolume)

			targetDisk := cnv.Disk{
				Name: disk.Name,
				DiskDevice: cnv.DiskDevice{
					Disk: &cnv.DiskTarget{
						Bus: disk.Disk.Bus,
					},
				},
			}

			targetVMspec.Template.Spec.Domain.Devices.Disks = append(targetVMspec.Template.Spec.Domain.Devices.Disks, targetDisk)
		}
	}

	// Clear MAC address on target VM
	// TODO: temporary hack for internal migration, REMOVE this
	for i := range targetVMspec.Template.Spec.Domain.Devices.Interfaces {
		targetVMspec.Template.Spec.Domain.Devices.Interfaces[i].MacAddress = ""
	}

	object.Template.Spec = targetVMspec.Template.Spec

	return nil
}

func createDataVolumeSpec(size resource.Quantity, storageClassName, url, configMap, secret string) *cdi.DataVolumeSpec {
	return &cdi.DataVolumeSpec{
		Source: &cdi.DataVolumeSource{
			HTTP: &cdi.DataVolumeSourceHTTP{
				URL:                url,
				CertConfigMap:      configMap,
				SecretExtraHeaders: []string{secret},
			},
		},
		Storage: &cdi.StorageSpec{
			Resources: core.ResourceRequirements{
				Requests: core.ResourceList{
					core.ResourceStorage: size,
				},
			},
			StorageClassName: &storageClassName,
		},
	}
}

func pvcSourceName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}