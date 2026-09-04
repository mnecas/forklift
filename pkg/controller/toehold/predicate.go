package toehold

import (
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	libref "github.com/kubev2v/forklift/pkg/lib/ref"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type ToeholdPredicate struct {
	predicate.TypedFuncs[*api.Toehold]
}

func (r ToeholdPredicate) Create(e event.TypedCreateEvent[*api.Toehold]) bool {
	libref.Mapper.Create(event.CreateEvent{Object: e.Object})
	return true
}

func (r ToeholdPredicate) Update(e event.TypedUpdateEvent[*api.Toehold]) bool {
	object := e.ObjectNew
	changed := object.Status.ObservedGeneration < object.Generation
	if changed {
		libref.Mapper.Update(event.UpdateEvent{
			ObjectOld: e.ObjectOld,
			ObjectNew: e.ObjectNew,
		})
	}
	if object.Status.Phase == api.ToeholdPhaseSucceeded || object.Status.Phase == api.ToeholdPhaseFailed {
		return changed
	}
	return true
}

func (r ToeholdPredicate) Delete(e event.TypedDeleteEvent[*api.Toehold]) bool {
	libref.Mapper.Delete(event.DeleteEvent{Object: e.Object})
	return true
}
