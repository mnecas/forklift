package vsphere

import (
	planbase "github.com/kubev2v/forklift/pkg/controller/plan/adapter/base"
	"github.com/kubev2v/forklift/pkg/controller/provider/model/vsphere"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/vsphere"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func shareablePVC(name, namespace, diskSource, planID string, terminating bool) *core.PersistentVolumeClaim {
	pvc := &core.PersistentVolumeClaim{
		ObjectMeta: meta.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				Shareable: "true",
			},
			Annotations: map[string]string{
				planbase.AnnDiskSource: diskSource,
			},
		},
	}
	if planID != "" {
		pvc.Labels["plan"] = planID
	}
	if terminating {
		now := meta.Now()
		pvc.DeletionTimestamp = &now
		pvc.Finalizers = []string{"test-finalizer"}
	}
	return pvc
}

func buildFakeClient(objs ...runtime.Object) *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	_ = core.AddToScheme(scheme)
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...)
}

var _ = Describe("listShareablePVCs", func() {
	const (
		ns    = "target-ns"
		plan1 = "plan-uid-1"
		disk1 = "[ds1] vm/disk1.vmdk"
		disk2 = "[ds1] vm/disk2.vmdk"
	)

	It("should return all non-terminating shareable PVCs", func() {
		c := buildFakeClient(
			shareablePVC("pvc-a", ns, disk1, plan1, false),
			shareablePVC("pvc-b", ns, disk2, "", false),
		).Build()

		pvcs, err := listShareablePVCs(c, ns)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcs).To(HaveLen(2))
	})

	It("should filter out terminating PVCs", func() {
		c := buildFakeClient(
			shareablePVC("pvc-active", ns, disk1, plan1, false),
			shareablePVC("pvc-terminating", ns, disk2, plan1, true),
		).Build()

		pvcs, err := listShareablePVCs(c, ns)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcs).To(HaveLen(1))
		Expect(pvcs[0].Name).To(Equal("pvc-active"))
	})
})

var _ = Describe("getDiskSharedPVC", func() {
	const (
		plan1 = "plan-uid-1"
		plan2 = "plan-uid-2"
		ns    = "target-ns"
		disk1 = "[ds1] vm/disk1.vmdk"
	)

	disk := vsphere.Disk{File: disk1, Shared: true}

	It("should prefer PVC with matching plan label", func() {
		pvcs := []*core.PersistentVolumeClaim{
			shareablePVC("pvc-other-plan", ns, disk1, plan2, false),
			shareablePVC("pvc-same-plan", ns, disk1, plan1, false),
		}

		pvc := getDiskSharedPVC(disk, pvcs, plan1)
		Expect(pvc).NotTo(BeNil())
		Expect(pvc.Name).To(Equal("pvc-same-plan"))
	})

	It("should fall back to PVC without plan label when plan doesn't match", func() {
		pvcs := []*core.PersistentVolumeClaim{
			shareablePVC("pvc-no-plan", ns, disk1, "", false),
		}

		pvc := getDiskSharedPVC(disk, pvcs, plan1)
		Expect(pvc).NotTo(BeNil())
		Expect(pvc.Name).To(Equal("pvc-no-plan"))
	})

	It("should fall back to PVC from different plan when no same-plan PVC exists", func() {
		pvcs := []*core.PersistentVolumeClaim{
			shareablePVC("pvc-other-plan", ns, disk1, plan2, false),
		}

		pvc := getDiskSharedPVC(disk, pvcs, plan1)
		Expect(pvc).NotTo(BeNil())
		Expect(pvc.Name).To(Equal("pvc-other-plan"))
	})

	It("should return nil when no PVC matches the disk source", func() {
		pvcs := []*core.PersistentVolumeClaim{
			shareablePVC("pvc-wrong-disk", ns, "[ds1] vm/other.vmdk", plan1, false),
		}

		pvc := getDiskSharedPVC(disk, pvcs, plan1)
		Expect(pvc).To(BeNil())
	})

	It("should work with empty planID", func() {
		pvcs := []*core.PersistentVolumeClaim{
			shareablePVC("pvc-a", ns, disk1, plan1, false),
		}

		pvc := getDiskSharedPVC(disk, pvcs, "")
		Expect(pvc).NotTo(BeNil())
		Expect(pvc.Name).To(Equal("pvc-a"))
	})
})

var _ = Describe("findSharedPVCs", func() {
	const (
		ns    = "target-ns"
		plan1 = "plan-uid-1"
		plan2 = "plan-uid-2"
		disk1 = "[ds1] vm/disk1.vmdk"
		disk2 = "[ds1] vm/disk2.vmdk"
		disk3 = "[ds1] vm/disk3.vmdk"
	)

	vm := &model.VM{}
	vm.Disks = []vsphere.Disk{
		{File: disk1, Shared: true},
		{File: disk2, Shared: true},
		{File: disk3, Shared: false},
	}

	It("should match shared disk PVCs and report missing ones", func() {
		c := buildFakeClient(
			shareablePVC("pvc-d1", ns, disk1, plan1, false),
		).Build()

		pvcs, missing, err := findSharedPVCs(c, vm, ns, plan1)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcs).To(HaveLen(1))
		Expect(pvcs[0].Name).To(Equal("pvc-d1"))
		Expect(missing).To(HaveLen(1))
		Expect(missing[0].File).To(Equal(disk2))
	})

	It("should not include terminating PVCs as matches", func() {
		c := buildFakeClient(
			shareablePVC("pvc-d1", ns, disk1, plan1, true),
			shareablePVC("pvc-d2", ns, disk2, plan1, false),
		).Build()

		pvcs, missing, err := findSharedPVCs(c, vm, ns, plan1)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcs).To(HaveLen(1))
		Expect(pvcs[0].Name).To(Equal("pvc-d2"))
		Expect(missing).To(HaveLen(1))
		Expect(missing[0].File).To(Equal(disk1))
	})

	It("should skip non-shared disks", func() {
		c := buildFakeClient(
			shareablePVC("pvc-d1", ns, disk1, plan1, false),
			shareablePVC("pvc-d2", ns, disk2, plan1, false),
			shareablePVC("pvc-d3", ns, disk3, plan1, false),
		).Build()

		pvcs, missing, err := findSharedPVCs(c, vm, ns, plan1)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcs).To(HaveLen(2))
		Expect(missing).To(BeEmpty())
	})

	It("should prefer same-plan PVCs over other-plan PVCs", func() {
		c := buildFakeClient(
			shareablePVC("pvc-other", ns, disk1, plan2, false),
			shareablePVC("pvc-same", ns, disk1, plan1, false),
			shareablePVC("pvc-d2", ns, disk2, plan1, false),
		).Build()

		pvcs, missing, err := findSharedPVCs(c, vm, ns, plan1)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcs).To(HaveLen(2))
		Expect(missing).To(BeEmpty())
		Expect(pvcs[0].Name).To(Equal("pvc-same"))
	})
})
