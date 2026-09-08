package toehold

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const credsSecretSuffix = "-vcenter-creds"

func credsSecretName(toehold *api.Toehold) string {
	return toehold.Name + credsSecretSuffix
}

func (r Reconciler) setOwner(toehold *api.Toehold, obj meta.Object) error {
	if r.Scheme == nil {
		return liberr.New("controller scheme is not configured")
	}
	return controllerutil.SetControllerReference(toehold, obj, r.Scheme)
}

func (r Reconciler) ensureServiceAccount(ctx context.Context, toehold *api.Toehold) error {
	sa := &core.ServiceAccount{
		ObjectMeta: meta.ObjectMeta{
			Name:      saName,
			Namespace: toehold.TargetNS(),
		},
	}
	err := r.Create(ctx, sa)
	if err != nil && !k8serr.IsAlreadyExists(err) {
		return liberr.Wrap(err)
	}
	return nil
}

func (r Reconciler) ensureCredsSecret(ctx context.Context, toehold *api.Toehold, pctx *providerContext) error {
	name := credsSecretName(toehold)
	ns := toehold.TargetNS()
	data := map[string]string{
		"VCENTER_URL":        pctx.Provider.Spec.URL,
		"VCENTER_USER":       string(pctx.Secret.Data["user"]),
		"VCENTER_PASSWORD":   string(pctx.Secret.Data["password"]),
		"VCENTER_INSECURE":   fmt.Sprint(baseInsecure(pctx.Secret)),
		"VCENTER_THUMBPRINT": pctx.Provider.Status.Fingerprint,
	}
	secret := &core.Secret{}
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, secret)
	if k8serr.IsNotFound(err) {
		secret = &core.Secret{
			ObjectMeta: meta.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Type:       core.SecretTypeOpaque,
			StringData: data,
		}
		if err = r.setOwner(toehold, secret); err != nil {
			return liberr.Wrap(err)
		}
		return liberr.Wrap(r.Create(ctx, secret))
	}
	if err != nil {
		return liberr.Wrap(err)
	}
	secret.StringData = data
	if err = r.setOwner(toehold, secret); err != nil {
		return liberr.Wrap(err)
	}
	return liberr.Wrap(r.Update(ctx, secret))
}

func baseInsecure(secret *core.Secret) bool {
	if secret == nil {
		return false
	}
	v, ok := secret.Data["insecureSkipVerify"]
	if !ok {
		return false
	}
	return string(v) == "true" || string(v) == "1"
}

func (r Reconciler) ensureJob(ctx context.Context, toehold *api.Toehold) (*batch.Job, error) {
	list := &batch.JobList{}
	err := r.List(ctx, list, &client.ListOptions{
		Namespace:     toehold.TargetNS(),
		LabelSelector: labels.SelectorFromSet(jobLabels(toehold)),
	})
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	if len(list.Items) > 0 {
		return &list.Items[0], nil
	}
	job := r.buildJob(toehold, credsSecretName(toehold))
	if err = r.setOwner(toehold, job); err != nil {
		return nil, liberr.Wrap(err)
	}
	err = r.Create(ctx, job)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	return job, nil
}

func (r Reconciler) deleteJob(ctx context.Context, toehold *api.Toehold) error {
	list := &batch.JobList{}
	err := r.List(ctx, list, &client.ListOptions{
		Namespace:     toehold.TargetNS(),
		LabelSelector: labels.SelectorFromSet(jobLabels(toehold)),
	})
	if err != nil {
		return liberr.Wrap(err)
	}
	for i := range list.Items {
		if err = r.Delete(ctx, &list.Items[i]); err != nil && !k8serr.IsNotFound(err) {
			return liberr.Wrap(err)
		}
	}
	return nil
}

func jobSucceeded(job *batch.Job) bool {
	return job != nil && job.Status.Succeeded > 0
}

func jobFailed(job *batch.Job) bool {
	return job != nil && job.Status.Failed > 0
}
