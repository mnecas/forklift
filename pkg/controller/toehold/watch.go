package toehold

import (
	"context"

	batch "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func toeholdForJobMapper() handler.TypedEventHandler[*batch.Job, reconcile.Request] {
	return handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, job *batch.Job) []reconcile.Request {
		name := job.Labels[labelToehold]
		if name == "" {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: job.Namespace,
				Name:      name,
			},
		}}
	})
}

func jobOwnedByToehold(obj *batch.Job) bool {
	_, ok := obj.Labels[labelToehold]
	return ok
}
