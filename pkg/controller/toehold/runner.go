package toehold

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/conversion"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
	"github.com/kubev2v/forklift/pkg/toehold/version"
	toeholdvsphere "github.com/kubev2v/forklift/pkg/toehold/vsphere"
	vimtypes "github.com/vmware/govmomi/vim25/types"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var flagNeedsBuild libitr.Flag = 0x01

// Runner executes the toehold template itinerary.
type Runner struct {
	ctx          context.Context
	r            *Reconciler
	toehold      *api.ToeholdTemplate
	pctx         *providerContext
	diskHash     string
	configHash   string
	sshPublicKey string
	skipBuild    bool
}

type runnerPredicate struct {
	run *Runner
}

func (p *runnerPredicate) Count() int { return 1 }

func (p *runnerPredicate) Evaluate(flag libitr.Flag) (bool, error) {
	if flag == flagNeedsBuild {
		return !p.run.skipBuild, nil
	}
	return false, nil
}

func (run *Runner) itinerary() *libitr.Itinerary {
	return &libitr.Itinerary{
		Name:      "toehold-template",
		Predicate: &runnerPredicate{run},
		Pipeline: libitr.Pipeline{
			{Name: string(api.StageEnsurePrerequisites)},
			{Name: string(api.StageEnsureTemplate)},
			{Name: string(api.StageBuildAndUpload), All: flagNeedsBuild},
			{Name: string(api.StageToeholdFinished)},
		},
	}
}

func (run *Runner) Run() (done bool, err error) {
	run.sshPublicKey, err = run.r.loadToeholdSSHPublicKey(run.ctx, run.toehold)
	if err != nil {
		return false, err
	}
	run.diskHash = version.DiskHash(run.toehold.Spec, run.sshPublicKey)
	run.configHash = version.ConfigHash(run.toehold.Spec)
	run.toehold.Status.Template.DiskHash = run.diskHash
	run.toehold.Status.Template.ConfigHash = run.configHash
	run.toehold.Status.Template.BaseContainerImage = run.toehold.Spec.BaseDisk.ContainerImage
	if run.toehold.Status.Stage == "" {
		step, err := run.itinerary().First()
		if err != nil {
			return false, err
		}
		run.toehold.Status.Stage = api.ToeholdTemplateStage(step.Name)
	}
	if run.toehold.Status.Phase == "" {
		run.toehold.Status.Phase = api.ToeholdTemplatePhasePending
	}
	run.toehold.Status.Phase = api.ToeholdTemplatePhaseRunning

	for {
		stage := run.toehold.Status.Stage
		log.Info("toehold template itinerary stage",
			"toeholdTemplate", run.toehold.Name,
			"namespace", run.toehold.Namespace,
			"stage", stage,
			"phase", run.toehold.Status.Phase,
		)
		if stage == api.StageToeholdFinished {
			run.toehold.Status.Phase = api.ToeholdTemplatePhaseSucceeded
			now := meta.Now()
			run.toehold.Status.CompletionTime = &now
			return true, nil
		}
		if err = run.executeStage(stage); err != nil {
			return false, err
		}
		next, itineraryDone, err := run.itinerary().Next(string(stage))
		if err != nil {
			return false, err
		}
		if itineraryDone {
			run.toehold.Status.Stage = api.StageToeholdFinished
			continue
		}
		run.toehold.Status.Stage = api.ToeholdTemplateStage(next.Name)
	}
}

func (run *Runner) executeStage(stage api.ToeholdTemplateStage) error {
	switch stage {
	case api.StageEnsurePrerequisites:
		return run.stagePrerequisites()
	case api.StageEnsureTemplate:
		return run.stageEnsureTemplate()
	case api.StageBuildAndUpload:
		return run.stageBuildAndUpload()
	default:
		return liberr.New(fmt.Sprintf("unknown toehold stage %q", stage))
	}
}

func (run *Runner) stagePrerequisites() error {
	if err := run.r.ensureServiceAccount(run.ctx, run.toehold); err != nil {
		return err
	}
	pctx, err := run.r.providerContext(run.ctx, run.toehold)
	if err != nil {
		return err
	}
	if _, err = run.r.ensureSSHPublicSecret(run.ctx, run.toehold); err != nil {
		pctx.Client.Close(run.ctx)
		return err
	}
	if err = run.r.ensureCredsSecret(run.ctx, run.toehold, pctx, run.sshPublicKey); err != nil {
		pctx.Client.Close(run.ctx)
		return err
	}
	if err = pctx.Client.ValidateInventory(run.ctx, toeholdvsphere.InventoryPreflight{
		Folder:    run.toehold.Spec.Folder,
		Datastore: run.toehold.Spec.Datastore,
		Network:   run.toehold.Spec.Network,
	}); err != nil {
		pctx.Client.Close(run.ctx)
		return err
	}
	run.pctx = pctx
	return nil
}

func (run *Runner) stageEnsureTemplate() error {
	if run.pctx == nil || run.pctx.Client == nil {
		pctx, err := run.r.providerContext(run.ctx, run.toehold)
		if err != nil {
			return err
		}
		run.pctx = pctx
	}
	defer run.pctx.Client.Close(run.ctx)
	if run.toehold.RebuildRequested() {
		_ = run.pctx.Client.DestroyVMIfExists(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
		run.toehold.Status.RebuildRequestedAt = run.toehold.Annotations[api.AnnRebuildRequestedAt]
		return run.requireBuild()
	}
	ref, err := run.pctx.Client.FindTemplate(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
	if err != nil {
		// Template may still exist under a previous folder; wipe by name so
		// the rebuild can land in Spec.Folder.
		_ = run.pctx.Client.DestroyVMIfExists(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
		return run.requireBuild()
	}
	storedDisk, storedConfig, err := run.pctx.Client.TemplateHashesFromVM(run.ctx, ref.VM)
	if err != nil {
		return err
	}
	if storedDisk == "" || storedDisk != run.diskHash {
		_ = run.pctx.Client.DestroyVMIfExists(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
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
		if err = stampTemplate(run.ctx, run.pctx.Client, ref, run.toehold, run.diskHash, run.configHash); err != nil {
			return err
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
	pod, err := run.r.ensureBuildPod(run.ctx, run.toehold)
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
	ref, err := pctx.Client.FindTemplate(run.ctx, run.toehold.Spec.Folder, run.toehold.Spec.TemplateName)
	if err != nil {
		return fmt.Errorf("find template %q after build: %w", run.toehold.Spec.TemplateName, err)
	}
	now := meta.Now()
	run.toehold.Status.Template.ImportedAt = &now
	run.toehold.Status.Template.Moref = ref.Moref
	run.toehold.Status.Template.Reused = false
	return verifyOrStampTemplate(run.ctx, pctx.Client, ref, run.toehold, run.diskHash, run.configHash)
}

var errRequeue = fmt.Errorf("requeue")

func isRequeue(err error) bool {
	return err != nil && err.Error() == errRequeue.Error()
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
	conn := conversion.VsphereConnectionSecret(provider.Spec.URL, secret, provider.Status.Fingerprint)
	gc, err := conversion.GovmomiClientFromSecret(ctx, conn)
	if err != nil {
		return nil, err
	}
	client, err := toeholdvsphere.NewClient(gc)
	if err != nil {
		return nil, err
	}
	return &providerContext{Provider: provider, Secret: secret, Client: client}, nil
}

func verifyOrStampTemplate(ctx context.Context, client *toeholdvsphere.Client, ref *toeholdvsphere.VMRef, toehold *api.ToeholdTemplate, diskHash, configHash string) error {
	storedDisk, storedConfig, err := client.TemplateHashesFromVM(ctx, ref.VM)
	if err != nil {
		return fmt.Errorf("read template hashes for %q moref=%s: %w", ref.Name, ref.Moref, err)
	}
	if storedDisk == diskHash && storedConfig == configHash {
		return nil
	}
	if storedDisk != "" && storedDisk != diskHash {
		return fmt.Errorf("template %q moref=%s has disk hash %q, expected %q", ref.Name, ref.Moref, storedDisk, diskHash)
	}
	return stampTemplate(ctx, client, ref, toehold, diskHash, configHash)
}

func stampTemplate(ctx context.Context, client *toeholdvsphere.Client, ref *toeholdvsphere.VMRef, toehold *api.ToeholdTemplate, diskHash, configHash string) error {
	err := client.SetAnnotationMap(ctx, ref.VM, map[string]string{
		toeholdvsphere.DiskHashAnnotation:           diskHash,
		toeholdvsphere.ConfigHashAnnotation:         configHash,
		toeholdvsphere.BaseContainerImageAnnotation: toehold.Spec.BaseDisk.ContainerImage,
		toeholdvsphere.ImportedAtAnnotation:         fmt.Sprint(toehold.CreationTimestamp.Time.UTC().Format("2006-01-02T15:04:05Z")),
	})
	if err != nil {
		return fmt.Errorf("stamp template %q moref=%s: %w", ref.Name, ref.Moref, err)
	}
	return nil
}
