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

// Runner executes the toehold template pipeline.
type Runner struct {
	ctx           context.Context
	r             *Reconciler
	toehold       *api.ToeholdTemplate
	pctx          *providerContext
	diskHash      string
	configHash    string
	sshSecretName string
	sshPublicKey  string
	sshProviderNS string
	skipBuild     bool
}

func (run *Runner) Run() (done bool, err error) {
	run.sshSecretName, run.sshPublicKey, run.sshProviderNS, err = run.r.loadToeholdSSH(run.ctx, run.toehold)
	if err != nil {
		return false, err
	}
	run.diskHash = version.DiskHash(run.toehold.Spec, run.sshPublicKey)
	run.configHash = version.ConfigHash(run.toehold.Spec)
	run.toehold.Status.Template.DiskHash = run.diskHash
	run.toehold.Status.Template.ConfigHash = run.configHash
	run.toehold.Status.Template.BaseContainerImage = run.toehold.Spec.BaseDisk.ContainerImage
	if run.toehold.Status.Stage == "" || run.toehold.Status.Stage == api.StageEnsurePrerequisites {
		run.toehold.Status.Stage = api.StageEnsureTemplate
	}
	run.toehold.Status.Phase = api.ToeholdTemplatePhaseRunning

	for {
		stage := run.toehold.Status.Stage
		log.Info("toehold template stage",
			"toeholdTemplate", run.toehold.Name,
			"namespace", run.toehold.Namespace,
			"stage", stage,
			"phase", run.toehold.Status.Phase,
		)
		switch stage {
		case api.StageToeholdFinished:
			run.toehold.Status.Phase = api.ToeholdTemplatePhaseSucceeded
			now := meta.Now()
			run.toehold.Status.CompletionTime = &now
			return true, nil
		case api.StageEnsureTemplate:
			if err = run.stageEnsure(); err != nil {
				return false, err
			}
			if run.skipBuild {
				run.toehold.Status.Stage = api.StageToeholdFinished
			} else {
				run.toehold.Status.Stage = api.StageBuildAndUpload
			}
		case api.StageBuildAndUpload:
			if err = run.stageBuildAndUpload(); err != nil {
				return false, err
			}
			run.toehold.Status.Stage = api.StageToeholdFinished
		default:
			return false, liberr.New(fmt.Sprintf("unknown toehold stage %q", stage))
		}
	}
}

func (run *Runner) stageEnsure() error {
	if err := run.r.ensureServiceAccount(run.ctx, run.toehold); err != nil {
		return err
	}
	pctx, err := run.r.providerContext(run.ctx, run.toehold)
	if err != nil {
		return err
	}
	run.pctx = pctx
	defer pctx.Client.Close(run.ctx)

	if _, err = run.r.ensureSSHPublicSecret(run.ctx, run.toehold, run.sshSecretName, run.sshPublicKey, run.sshProviderNS); err != nil {
		return err
	}
	if err = run.r.ensureCredsSecret(run.ctx, run.toehold, pctx, run.sshPublicKey); err != nil {
		return err
	}
	if err = pctx.Client.ValidateInventory(run.ctx, toeholdvsphere.InventoryPreflight{
		Folder:    run.toehold.Spec.Folder,
		Datastore: run.toehold.Spec.Datastore,
		Network:   run.toehold.Spec.Network,
	}); err != nil {
		return err
	}

	if run.toehold.RebuildRequested() {
		_ = pctx.Client.DestroyIfExists(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
		run.toehold.Status.RebuildRequestedAt = run.toehold.Annotations[api.AnnRebuildRequestedAt]
		return run.requireBuild()
	}
	ref, err := pctx.Client.FindVM(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName, true)
	if err != nil {
		_ = pctx.Client.DestroyIfExists(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
		return run.requireBuild()
	}
	anns, err := pctx.Client.GetAnnotationMap(run.ctx, ref.VM)
	if err != nil {
		return err
	}
	storedDisk, storedConfig := anns[toeholdvsphere.DiskHashAnnotation], anns[toeholdvsphere.ConfigHashAnnotation]
	if storedDisk == "" || storedDisk != run.diskHash {
		_ = pctx.Client.DestroyIfExists(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
		run.toehold.Status.SetCondition(libcnd.Condition{
			Type:     api.ToeholdTemplateRebuildRequired,
			Status:   libcnd.True,
			Category: libcnd.Advisory,
			Message:  "Template disk hash mismatch; rebuild required.",
		})
		return run.requireBuild()
	}
	run.skipBuild = true
	run.toehold.Status.Template.Reused = true
	run.toehold.Status.Template.Moref = ref.Moref
	if storedConfig != run.configHash {
		spec := vimtypes.VirtualMachineConfigSpec{
			NumCPUs:  cpuCount(run.toehold),
			MemoryMB: int64(memoryMiB(run.toehold)),
		}
		task, err := ref.VM.Reconfigure(run.ctx, spec)
		if err != nil {
			return liberr.Wrap(err, "reconfigure template hardware")
		}
		if err = task.Wait(run.ctx); err != nil {
			return liberr.Wrap(err, "reconfigure template hardware task")
		}
		if err = pctx.Client.SetAnnotationMap(run.ctx, ref.VM, map[string]string{
			toeholdvsphere.DiskHashAnnotation:           run.diskHash,
			toeholdvsphere.ConfigHashAnnotation:         run.configHash,
			toeholdvsphere.BaseContainerImageAnnotation: run.toehold.Spec.BaseDisk.ContainerImage,
			toeholdvsphere.ImportedAtAnnotation:         run.toehold.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
		}); err != nil {
			return fmt.Errorf("stamp template %q moref=%s: %w", ref.Name, ref.Moref, err)
		}
	}
	run.toehold.Status.SetCondition(libcnd.Condition{
		Type:     api.ToeholdTemplateUpToDate,
		Status:   libcnd.True,
		Category: libcnd.Advisory,
		Message:  "Reusing existing vCenter template.",
	})
	return nil
}

func (run *Runner) requireBuild() error {
	run.skipBuild = false
	run.toehold.Status.Template.Reused = false
	if err := run.r.deleteBuildPod(run.ctx, run.toehold); err != nil {
		return err
	}
	return run.pctx.Client.ValidateInventory(run.ctx, toeholdvsphere.InventoryPreflight{
		Folder:               run.toehold.Spec.Folder,
		Datastore:            run.toehold.Spec.Datastore,
		Network:              run.toehold.Spec.Network,
		RequireTemplateSpace: true,
	})
}

func (run *Runner) stageBuildAndUpload() error {
	pod, err := run.r.ensureBuildPod(run.ctx, run.toehold, run.sshSecretName, run.sshPublicKey, run.sshProviderNS)
	if err != nil {
		return err
	}
	run.toehold.Status.BuildPod = &core.ObjectReference{
		Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name,
	}
	if pod.Status.Phase == core.PodFailed {
		return liberr.New("toehold build pod failed")
	}
	if pod.Status.Phase != core.PodSucceeded {
		run.toehold.Status.Message = "Waiting for toehold build pod"
		return errRequeue
	}
	pctx, err := run.r.providerContext(run.ctx, run.toehold)
	if err != nil {
		return err
	}
	defer pctx.Client.Close(run.ctx)
	ref, err := pctx.Client.FindVM(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName, true)
	if err != nil {
		return fmt.Errorf("find template %q after build: %w", run.toehold.Spec.TemplateName, err)
	}
	now := meta.Now()
	run.toehold.Status.Template.ImportedAt = &now
	run.toehold.Status.Template.Moref = ref.Moref
	run.toehold.Status.Template.Reused = false
	// ImportOVF already stamps disk/config hashes before mark-as-template.
	return nil
}

var errRequeue = fmt.Errorf("requeue")

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
