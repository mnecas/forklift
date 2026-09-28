package copyappliance

import (
	"context"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Ensurer finds and creates CopyAppliance CRs. An appliance is identified by
// its labels rather than by its name: appliances are created with GenerateName,
// so the name is not known until the API server assigns it.
type Ensurer struct {
	Labeler Labeler
	Log     logging.LevelLogger
	k8sclient.Client
}

// Appliance ensures there is a live appliance carrying the built appliance's
// labels, and returns the live one.
func (r *Ensurer) Appliance(ctx context.Context, appliance *api.CopyAppliance) (out *api.CopyAppliance, err error) {
	out, err = r.Find(ctx, appliance.Namespace, appliance.Labels, true)
	if err != nil || out != nil {
		return
	}
	err = r.Create(ctx, appliance)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	r.Log.Info("Created CopyAppliance.", "appliance", appliance.Name, "namespace", appliance.Namespace)
	out = appliance
	return
}

// Find returns the one appliance carrying labels, or nil when there is none.
// With liveOnly, an appliance being torn down is not a match: whatever needs
// one has to build a new one rather than wait on the old one releasing its
// disk locks. A caller waiting on a teardown passes false, since it has to see
// the appliance through it.
func (r *Ensurer) Find(ctx context.Context, namespace string, labels map[string]string, liveOnly bool) (out *api.CopyAppliance, err error) {
	list := &api.CopyApplianceList{}
	err = r.List(ctx, list, &k8sclient.ListOptions{
		LabelSelector: k8slabels.SelectorFromSet(labels),
		Namespace:     namespace,
	})
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	for i := range list.Items {
		item := &list.Items[i]
		if liveOnly && item.DeletionTimestamp != nil {
			continue
		}
		if out != nil {
			// An appliance holds read locks on the disks it serves. Taking the
			// first and ignoring the rest would leave those locks held by
			// something nothing is tracking.
			err = liberr.New(
				"found multiple copy appliances with the same labels",
				"labels", labels,
				"namespace", namespace)
			out = nil
			return
		}
		out = item
	}
	return
}
