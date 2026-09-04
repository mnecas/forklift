package toehold

import (
	"context"
	"fmt"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/toehold/annotations"
	toeholdguest "github.com/kubev2v/forklift/pkg/toehold/guest"
	toeholdvsphere "github.com/kubev2v/forklift/pkg/toehold/vsphere"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type pipeline struct {
	ctx      context.Context
	r        *Reconciler
	toehold  *api.Toehold
	pctx     *providerContext
	bootcID  string
	tplHash  string
	vmHash   string
	sshKey        string
	sshPrivateKey []byte
	skipBuild     bool
	skipClone bool
}

func (p *pipeline) run() (done bool, err error) {
	if p.toehold.Status.Stage == "" {
		p.toehold.Status.Stage = api.StageEnsurePrerequisites
	}
	if p.toehold.Status.Phase == "" {
		p.toehold.Status.Phase = api.ToeholdPhasePending
	}
	p.toehold.Status.Phase = api.ToeholdPhaseRunning

	for {
		switch p.toehold.Status.Stage {
		case api.StageEnsurePrerequisites:
			if err = p.stagePrerequisites(); err != nil {
				return false, err
			}
			p.advance(api.StageEnsureTemplate)
		case api.StageEnsureTemplate:
			if err = p.stageEnsureTemplate(); err != nil {
				return false, err
			}
			if p.skipBuild {
				p.advance(api.StageEnsureVM)
			} else {
				p.advance(api.StageBuildAndUpload)
			}
		case api.StageBuildAndUpload:
			if err = p.stageBuildAndUpload(); err != nil {
				return false, err
			}
			p.advance(api.StageEnsureVM)
		case api.StageEnsureVM:
			if err = p.stageEnsureVM(); err != nil {
				return false, err
			}
			if p.skipClone {
				p.advance(api.StageConfigureVM)
			} else {
				p.advance(api.StageCloneVM)
			}
		case api.StageCloneVM:
			if err = p.stageCloneVM(); err != nil {
				return false, err
			}
			p.advance(api.StageConfigureVM)
		case api.StageConfigureVM:
			if err = p.stageConfigureVM(); err != nil {
				return false, err
			}
			if p.toehold.Spec.ExportsDisks() {
				p.advance(api.StageAttachDisks)
			} else {
				p.advance(api.StageToeholdFinished)
			}
		case api.StageAttachDisks:
			if err = p.stageAttachDisks(); err != nil {
				return false, err
			}
			p.advance(api.StageStartNBD)
		case api.StageStartNBD:
			if err = p.stageStartNBD(); err != nil {
				return false, err
			}
			p.advance(api.StageVerifyNBD)
		case api.StageVerifyNBD:
			if err = p.stageVerifyNBD(); err != nil {
				if isRequeue(err) {
					return false, err
				}
				return false, err
			}
			p.advance(api.StageToeholdFinished)
		case api.StageToeholdFinished:
			p.toehold.Status.Phase = api.ToeholdPhaseSucceeded
			now := meta.Now()
			p.toehold.Status.CompletionTime = &now
			return true, nil
		default:
			p.toehold.Status.Stage = api.StageEnsurePrerequisites
		}
	}
}

func (p *pipeline) advance(stage api.ToeholdStage) {
	p.toehold.Status.Stage = stage
}

func (p *pipeline) stagePrerequisites() error {
	if err := p.r.ensureServiceAccount(p.ctx, p.toehold); err != nil {
		return err
	}
	var err error
	p.pctx, err = p.r.providerContext(p.ctx, p.toehold)
	if err != nil {
		return err
	}
	if err = p.r.ensureCredsSecret(p.ctx, p.toehold, p.pctx); err != nil {
		return err
	}
	p.bootcID = resolveBootcImageID(p.ctx, *p.r, p.toehold)
	p.tplHash = desiredTemplateHash(p.toehold.Spec, p.bootcID)
	p.toehold.Status.Template.ContentHash = p.tplHash
	p.toehold.Status.Template.BootcImage = p.toehold.Spec.BootcImage
	p.toehold.Status.Template.BootcImageID = p.bootcID
	p.sshKey, err = sshPublicKey(p.ctx, *p.r, p.pctx.Provider)
	if err != nil {
		return liberr.Wrap(err, "ssh public key")
	}
	p.vmHash = desiredVMHash(p.tplHash, p.toehold.Spec.VMName, p.sshKey)
	return nil
}

func (p *pipeline) stageEnsureTemplate() error {
	defer p.pctx.Client.Close(p.ctx)
	if p.toehold.Spec.ForceRebuild {
		_ = p.pctx.Client.DestroyVMIfExists(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.TemplateName)
		p.skipBuild = false
		p.toehold.Status.Template.Reused = false
		return nil
	}
	ref, err := p.pctx.Client.FindTemplate(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.TemplateName)
	if err != nil {
		p.skipBuild = false
		p.toehold.Status.Template.Reused = false
		return nil
	}
	stored, err := p.pctx.Client.TemplateContentHashFromVM(p.ctx, ref.VM)
	if err != nil {
		return err
	}
	if stored == "" || stored != p.tplHash {
		_ = p.pctx.Client.DestroyVMIfExists(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.TemplateName)
		p.skipBuild = false
		p.toehold.Status.Template.Reused = false
		p.toehold.Status.SetCondition(libcnd.Condition{
			Type:     api.ToeholdRebuildRequired,
			Status:   libcnd.True,
			Category: libcnd.Advisory,
			Message:  "Template content hash mismatch; rebuild required.",
		})
		return nil
	}
	p.skipBuild = true
	p.toehold.Status.Template.Reused = true
	p.toehold.Status.Template.Moref = ref.Moref
	p.toehold.Status.SetCondition(libcnd.Condition{
		Type:     api.ToeholdTemplateUpToDate,
		Status:   libcnd.True,
		Category: libcnd.Advisory,
		Message:  "Reusing existing vCenter template.",
	})
	return nil
}

func (p *pipeline) stageBuildAndUpload() error {
	job, err := p.r.ensureJob(p.ctx, p.toehold)
	if err != nil {
		return err
	}
	p.toehold.Status.Job = &core.ObjectReference{
		Kind: "Job", Namespace: job.Namespace, Name: job.Name,
	}
	if jobFailed(job) {
		return liberr.New("toehold build job failed")
	}
	if !jobSucceeded(job) {
		p.toehold.Status.Message = "Waiting for toehold build job"
		return errRequeue
	}
	pctx, err := p.r.providerContext(p.ctx, p.toehold)
	if err != nil {
		return err
	}
	defer pctx.Client.Close(p.ctx)
	ref, err := pctx.Client.FindTemplate(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.TemplateName)
	if err != nil {
		return err
	}
	now := meta.Now()
	p.toehold.Status.Template.ImportedAt = &now
	p.toehold.Status.Template.Moref = ref.Moref
	p.toehold.Status.Template.Reused = false
	return stampTemplate(p.ctx, pctx.Client, ref, p.toehold, p.bootcID, p.tplHash)
}

func (p *pipeline) stageEnsureVM() error {
	pctx, err := p.r.providerContext(p.ctx, p.toehold)
	if err != nil {
		return err
	}
	defer pctx.Client.Close(p.ctx)
	if p.toehold.Spec.ForceRebuild {
		_ = pctx.Client.DestroyVMIfExists(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.VMName)
		p.skipClone = false
		p.toehold.Status.VM.Reused = false
		return nil
	}
	ref, err := pctx.Client.FindVM(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.VMName)
	if err != nil {
		p.skipClone = false
		p.toehold.Status.VM.Reused = false
		return nil
	}
	ann, err := pctx.Client.GetAnnotationMap(p.ctx, ref.VM)
	if err != nil {
		return err
	}
	if ann[annotations.VMContentHash] == p.vmHash {
		p.skipClone = true
		p.toehold.Status.VM.Reused = true
		p.toehold.Status.VM.Moref = ref.Moref
		p.toehold.Status.VM.ContentHash = p.vmHash
		p.toehold.Status.SetCondition(libcnd.Condition{
			Type:     api.ToeholdVMUpToDate,
			Status:   libcnd.True,
			Category: libcnd.Advisory,
			Message:  "Reusing existing toehold VM.",
		})
		return nil
	}
	_ = pctx.Client.DestroyVMIfExists(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.VMName)
	p.skipClone = false
	p.toehold.Status.VM.Reused = false
	return nil
}

func (p *pipeline) stageCloneVM() error {
	pctx, err := p.r.providerContext(p.ctx, p.toehold)
	if err != nil {
		return err
	}
	defer pctx.Client.Close(p.ctx)
	ref, err := pctx.Client.Clone(p.ctx, toeholdvsphere.CloneOptions{
		FolderPath:    p.toehold.Spec.Folder,
		Datastore:     p.toehold.Spec.Datastore,
		TemplateName:  p.toehold.Spec.TemplateName,
		VMName:        p.toehold.Spec.VMName,
		GuestUserdata: GuestUserdata(p.sshKey),
	})
	if err != nil {
		return err
	}
	if err = stampVM(p.ctx, pctx.Client, ref, p.vmHash); err != nil {
		return err
	}
	p.toehold.Status.VM.Moref = ref.Moref
	p.toehold.Status.VM.ContentHash = p.vmHash
	p.toehold.Status.VM.Reused = false
	return nil
}

func (p *pipeline) stageConfigureVM() error {
	pctx, err := p.r.providerContext(p.ctx, p.toehold)
	if err != nil {
		return err
	}
	defer pctx.Client.Close(p.ctx)
	ref, err := pctx.Client.FindVM(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.VMName)
	if err != nil {
		return err
	}
	if err = pctx.Client.SetEFIBoot(p.ctx, ref.VM); err != nil {
		return err
	}
	if p.toehold.Spec.PowerOnEnabled() {
		if err = pctx.Client.PowerOn(p.ctx, ref.VM); err != nil {
			return err
		}
		waitCtx, cancel := context.WithTimeout(p.ctx, 5*time.Minute)
		defer cancel()
		ip, _ := pctx.Client.WaitForGuestIP(waitCtx, ref.VM)
		p.toehold.Status.VM.IP = ip
		if p.toehold.Spec.ExportsDisks() && ip != "" {
			if err = p.waitForGuestSSH(ip); err != nil {
				return err
			}
		}
	}
	if p.toehold.Spec.ExportsDisks() {
		p.toehold.Status.Message = "Toehold VM is ready; preparing disk exports"
	} else {
		p.toehold.Status.Message = "Toehold VM is ready"
	}
	return nil
}

func (p *pipeline) waitForGuestSSH(ip string) error {
	if err := p.ensureSSHKey(); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(p.ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		client, err := toeholdguest.Dial(waitCtx, ip, p.toehold.Spec.NBDSSHUser(), p.sshPrivateKey)
		if err == nil {
			_ = client.Close()
			return nil
		}
		select {
		case <-waitCtx.Done():
			return errRequeue
		case <-ticker.C:
		}
	}
}

var errRequeue = fmt.Errorf("requeue")

func isRequeue(err error) bool {
	return err != nil && err.Error() == errRequeue.Error()
}
