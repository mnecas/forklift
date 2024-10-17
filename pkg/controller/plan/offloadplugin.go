package plan

import (
	"context"
	planapi "github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1/plan"
	plancontext "github.com/konveyor/forklift-controller/pkg/controller/plan/context"
	libcnd "github.com/konveyor/forklift-controller/pkg/lib/condition"
	liberr "github.com/konveyor/forklift-controller/pkg/lib/error"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"path"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"strings"
)

const retry int = 5

// OffloadPluginRunner
type OffloadPluginRunner struct {
	*plancontext.Context
	// VM.
	vm       *planapi.VMStatus
	kubevirt KubeVirt
	step     *planapi.Step
}

// Run.
func (r *OffloadPluginRunner) Run(vm *planapi.VMStatus) (err error) {
	r.vm = vm
	r.step.MarkStarted()
	job, err := r.ensureJob()
	if err != nil {
		return
	}
	conditions := libcnd.Conditions{}
	for _, cnd := range job.Status.Conditions {
		conditions.SetCondition(libcnd.Condition{
			Type:    string(cnd.Type),
			Status:  string(cnd.Status),
			Reason:  cnd.Reason,
			Message: cnd.Message,
		})
	}
	// TODO: Do not finish all tasks but only specific of the disks which are migrated by the offload plugin
	if conditions.HasCondition("Failed") {
		r.step.AddError(conditions.FindCondition("Failed").Message)
		r.step.MarkCompleted()
	} else if int(job.Status.Failed) > retry {
		r.step.AddError("Retry limit exceeded.")
		r.step.MarkCompleted()
	} else if job.Status.Succeeded > 0 {
		r.step.MarkCompleted()
	}

	return
}

// Ensure the job.
func (r *OffloadPluginRunner) ensureJob() (job *batch.Job, err error) {
	list := batch.JobList{}
	err = r.Destination.Client.List(
		context.TODO(),
		&list,
		&client.ListOptions{
			LabelSelector: labels.SelectorFromSet(r.labels()),
			Namespace:     r.Plan.Namespace,
		})
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	if len(list.Items) == 0 {
		job, err = r.job()
		if err != nil {
			return
		}
		err = r.Destination.Client.Create(context.TODO(), job)
		if err != nil {
			err = liberr.Wrap(err)
			return
		}
		r.Log.Info(
			"Created (offload plugin) job.",
			"job",
			path.Join(
				job.Namespace,
				job.Name))
	} else {
		job = &list.Items[0]
		r.Log.V(1).Info(
			"Found (offload plugin) job.",
			"job",
			path.Join(
				job.Namespace,
				job.Name))
	}

	return
}

// Build the Job.
func (r *OffloadPluginRunner) job() (job *batch.Job, err error) {
	//secret, err := r.Kubevirt.ensureSecret(r.vm.Ref, r.Kubevirt.secretDataSetterForCDI(r.vm.Ref), r.labels())
	template := r.template()
	backOff := int32(1)
	job = &batch.Job{
		Spec: batch.JobSpec{
			Template:     *template,
			BackoffLimit: &backOff,
		},
		ObjectMeta: meta.ObjectMeta{
			Namespace: r.Plan.Namespace,
			GenerateName: strings.ToLower(
				strings.Join([]string{
					r.vm.ID,
					"offloadplugin"},
					"-") + "-"),
			Labels: r.labels(),
		},
	}
	err = k8sutil.SetOwnerReference(r.Plan, job, scheme.Scheme)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}

	return
}

// FIXME: This is just a tmp before we settle on the design in the end we could have multiple maps with multiple images
// we might even have multiple jobs with multiple offload plugins... depends on the mapping and design
func (r *OffloadPluginRunner) getOffloadImage() string {
	for _, storageMap := range r.Context.Plan.Map.Storage.Spec.Map {
		if storageMap.Destination.OffloadPlugin != "" {
			return storageMap.Destination.OffloadPlugin
		}
	}
	return ""
}

// Build pod template.
func (r *OffloadPluginRunner) template() (template *core.PodTemplateSpec) {
	template = &core.PodTemplateSpec{
		Spec: core.PodSpec{
			RestartPolicy: core.RestartPolicyNever,
			Containers: []core.Container{
				{
					Name:  "offloadplugin",
					Image: r.getOffloadImage(),
					Env:   r.getEnvironments(),
				},
			},
			//Volumes: r.getVolumes(),
		},
	}

	return
}

// Labels for created resources.
func (r *OffloadPluginRunner) labels() map[string]string {
	return map[string]string{
		kPlan:      string(r.Plan.UID),
		kMigration: string(r.Migration.UID),
		kVM:        r.vm.ID,
		kStep:      r.vm.Phase,
	}
}

func (r *OffloadPluginRunner) getEnvironments() (environments []core.EnvVar) {
	environments = append(environments,
		core.EnvVar{
			Name:  "HOST",
			Value: r.Context.Source.Provider.Spec.URL,
		},
		core.EnvVar{
			Name:  "PLAN_NAME",
			Value: r.Context.Plan.Name,
		},
		core.EnvVar{
			Name:  "NAMESPACE",
			Value: r.Context.Plan.Namespace,
		},
	)
	return environments
}

func (r *OffloadPluginRunner) getVolumes(secret *core.Secret) (volumes []core.Volume) {
	volumes = append(volumes,
		core.Volume{
			Name: "secret-volume",
			VolumeSource: core.VolumeSource{
				Secret: &core.SecretVolumeSource{
					SecretName: secret.Name,
				},
			},
		},
	)
	return volumes
}
