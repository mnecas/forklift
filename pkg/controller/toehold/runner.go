package toehold

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libvsphere "github.com/kubev2v/forklift/pkg/lib/vsphere"
	"github.com/kubev2v/forklift/pkg/toehold/version"
	toeholdvsphere "github.com/kubev2v/forklift/pkg/toehold/vsphere"
	vimtypes "github.com/vmware/govmomi/vim25/types"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Runner drives a ToeholdTemplate to a ready vCenter template. It holds no
// state of its own: every pass reads where it got to from the status and
// leaves the next stage behind — same shape as the copy-appliance runners.
type Runner struct {
	ctx     context.Context
	r       *Reconciler
	toehold *api.ToeholdTemplate
}

// Run runs the current stage once. A finished stage sets Status.Stage to its
// successor; the next reconcile picks it up. done is true only when the
// Finished stage has been reached.
func (run *Runner) Run() (done bool, err error) {
	sshSecretName, sshPublicKey, sshProviderNS, err := run.r.loadToeholdSSH(run.ctx, run.toehold)
	if err != nil {
		return false, err
	}
	run.toehold.Status.Template.DiskHash = version.DiskHash(run.toehold.Spec, sshPublicKey)
	run.toehold.Status.Template.ConfigHash = version.ConfigHash(run.toehold.Spec)
	run.toehold.Status.Template.BaseContainerImage = run.toehold.Spec.BaseDisk.ContainerImage
	if run.toehold.Status.Stage == "" || run.toehold.Status.Stage == api.StageEnsurePrerequisites {
		run.toehold.Status.Stage = api.StageEnsureTemplate
	}
	run.toehold.Status.Phase = api.ToeholdTemplatePhaseRunning

	log.Info("toehold template stage",
		"toeholdTemplate", run.toehold.Name,
		"namespace", run.toehold.Namespace,
		"stage", run.toehold.Status.Stage,
		"phase", run.toehold.Status.Phase,
	)

	switch run.toehold.Status.Stage {
	case api.StageToeholdFinished:
		run.toehold.Status.Phase = api.ToeholdTemplatePhaseSucceeded
		now := meta.Now()
		run.toehold.Status.CompletionTime = &now
		return true, nil
	case api.StageEnsureTemplate:
		next, ensureErr := run.ensureTemplate(sshSecretName, sshPublicKey, sshProviderNS)
		if ensureErr != nil {
			return false, ensureErr
		}
		run.toehold.Status.Stage = next
	case api.StageBuildAndUpload:
		built, buildErr := run.buildAndUpload(sshSecretName, sshPublicKey, sshProviderNS)
		if buildErr != nil {
			return false, buildErr
		}
		if built {
			run.toehold.Status.Stage = api.StageToeholdFinished
		}
	default:
		return false, liberr.New(fmt.Sprintf("unknown toehold stage %q", run.toehold.Status.Stage))
	}
	return false, nil
}

func (run *Runner) ensureTemplate(sshSecretName, sshPublicKey, sshProviderNS string) (next api.ToeholdTemplateStage, err error) {
	if err = run.r.ensureServiceAccount(run.ctx, run.toehold); err != nil {
		return
	}
	pctx, err := run.r.providerContext(run.ctx, run.toehold)
	if err != nil {
		return
	}
	defer pctx.Client.Close(run.ctx)

	if _, err = run.r.ensureSSHPublicSecret(run.ctx, run.toehold, sshSecretName, sshPublicKey, sshProviderNS); err != nil {
		return
	}
	if err = run.r.ensureCredsSecret(run.ctx, run.toehold, pctx, sshPublicKey); err != nil {
		return
	}
	if err = pctx.Client.ValidateInventory(run.ctx, toeholdvsphere.InventoryPreflight{
		Folder:    run.toehold.Spec.Folder,
		Datastore: run.toehold.Spec.Datastore,
		Network:   run.toehold.Spec.Network,
	}); err != nil {
		return
	}

	diskHash := run.toehold.Status.Template.DiskHash
	configHash := run.toehold.Status.Template.ConfigHash

	if run.toehold.RebuildRequested() {
		_ = pctx.Client.DestroyIfExists(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
		run.toehold.Status.RebuildRequestedAt = run.toehold.Annotations[api.AnnRebuildRequestedAt]
		return run.requireBuild(pctx)
	}
	ref, err := pctx.Client.FindVM(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName, true)
	if err != nil {
		_ = pctx.Client.DestroyIfExists(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
		return run.requireBuild(pctx)
	}
	anns, err := pctx.Client.GetAnnotationMap(run.ctx, ref.VM)
	if err != nil {
		return
	}
	storedDisk, storedConfig := anns[toeholdvsphere.DiskHashAnnotation], anns[toeholdvsphere.ConfigHashAnnotation]
	if storedDisk == "" || storedDisk != diskHash {
		_ = pctx.Client.DestroyIfExists(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
		run.toehold.Status.SetCondition(libcnd.Condition{
			Type:     api.ToeholdTemplateRebuildRequired,
			Status:   libcnd.True,
			Category: libcnd.Advisory,
			Message:  "Template disk hash mismatch; rebuild required.",
		})
		return run.requireBuild(pctx)
	}

	run.toehold.Status.Template.Reused = true
	run.toehold.Status.Template.Moref = ref.Moref
	if storedConfig != configHash {
		spec := vimtypes.VirtualMachineConfigSpec{
			NumCPUs:  cpuCount(run.toehold),
			MemoryMB: int64(memoryMiB(run.toehold)),
		}
		task, reconfErr := ref.VM.Reconfigure(run.ctx, spec)
		if reconfErr != nil {
			return "", liberr.Wrap(reconfErr, "reconfigure template hardware")
		}
		if err = task.Wait(run.ctx); err != nil {
			return "", liberr.Wrap(err, "reconfigure template hardware task")
		}
		if err = pctx.Client.SetAnnotationMap(run.ctx, ref.VM, map[string]string{
			toeholdvsphere.DiskHashAnnotation:           diskHash,
			toeholdvsphere.ConfigHashAnnotation:         configHash,
			toeholdvsphere.BaseContainerImageAnnotation: run.toehold.Spec.BaseDisk.ContainerImage,
			toeholdvsphere.ImportedAtAnnotation:         run.toehold.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
		}); err != nil {
			return "", fmt.Errorf("stamp template %q moref=%s: %w", ref.Name, ref.Moref, err)
		}
	}
	run.toehold.Status.SetCondition(libcnd.Condition{
		Type:     api.ToeholdTemplateUpToDate,
		Status:   libcnd.True,
		Category: libcnd.Advisory,
		Message:  "Reusing existing vCenter template.",
	})
	return api.StageToeholdFinished, nil
}

func (run *Runner) requireBuild(pctx *providerContext) (api.ToeholdTemplateStage, error) {
	run.toehold.Status.Template.Reused = false
	if err := run.r.deleteBuildPod(run.ctx, run.toehold); err != nil {
		return "", err
	}
	if err := pctx.Client.ValidateInventory(run.ctx, toeholdvsphere.InventoryPreflight{
		Folder:       run.toehold.Spec.Folder,
		Datastore:    run.toehold.Spec.Datastore,
		Network:      run.toehold.Spec.Network,
		MinFreeBytes: toeholdvsphere.DefaultTemplateDatastoreFreeBytes,
	}); err != nil {
		return "", err
	}
	return api.StageBuildAndUpload, nil
}

// buildAndUpload returns true once the build pod has succeeded and the
// template is stamped; false means still waiting.
func (run *Runner) buildAndUpload(sshSecretName, sshPublicKey, sshProviderNS string) (done bool, err error) {
	pod, err := run.r.ensureBuildPod(run.ctx, run.toehold, sshSecretName, sshPublicKey, sshProviderNS)
	if err != nil {
		return false, err
	}
	run.toehold.Status.BuildPod = &core.ObjectReference{
		Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name,
	}
	if pod.Status.Phase == core.PodFailed {
		return false, liberr.New("toehold build pod failed")
	}
	if pod.Status.Phase != core.PodSucceeded {
		run.toehold.Status.Message = "Waiting for toehold build pod"
		return false, nil
	}
	pctx, err := run.r.providerContext(run.ctx, run.toehold)
	if err != nil {
		return false, err
	}
	defer pctx.Client.Close(run.ctx)
	ref, err := pctx.Client.FindVM(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName, true)
	if err != nil {
		return false, fmt.Errorf("find template %q after build: %w", run.toehold.Spec.TemplateName, err)
	}
	now := meta.Now()
	run.toehold.Status.Template.ImportedAt = &now
	run.toehold.Status.Template.Moref = ref.Moref
	run.toehold.Status.Template.Reused = false
	// ImportOVF already stamps disk/config hashes before mark-as-template.
	return true, nil
}

type providerContext struct {
	Provider *api.Provider
	Secret   *core.Secret
	Client   *toeholdvsphere.Client
}

func (r Reconciler) providerContext(ctx context.Context, toehold *api.ToeholdTemplate) (*providerContext, error) {
	provider := &api.Provider{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: toehold.Spec.Provider.Namespace,
		Name:      toehold.Spec.Provider.Name,
	}, provider)
	if err != nil {
		return nil, err
	}
	secret := &core.Secret{}
	err = r.Get(ctx, types.NamespacedName{
		Namespace: provider.Spec.Secret.Namespace,
		Name:      provider.Spec.Secret.Name,
	}, secret)
	if err != nil {
		return nil, err
	}
	gc, err := libvsphere.ConnectProvider(
		ctx,
		provider.Spec.URL,
		string(secret.Data["user"]),
		string(secret.Data["password"]),
		provider.Status.Fingerprint,
		secret,
	)
	if err != nil {
		return nil, err
	}
	client, err := toeholdvsphere.NewClient(gc)
	if err != nil {
		return nil, err
	}
	return &providerContext{Provider: provider, Secret: secret, Client: client}, nil
}
