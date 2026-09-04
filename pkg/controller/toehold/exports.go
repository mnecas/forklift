package toehold

import (
	"context"
	"fmt"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/toehold/guest"
)

func (p *pipeline) stageAttachDisks() error {
	if !p.toehold.Spec.ExportsDisks() {
		return nil
	}
	if p.toehold.Status.VM.IP == "" {
		return errRequeue
	}
	if err := p.ensureSSHKey(); err != nil {
		return err
	}
	pctx, err := p.r.providerContext(p.ctx, p.toehold)
	if err != nil {
		return err
	}
	defer pctx.Client.Close(p.ctx)

	appliance, err := pctx.Client.FindVM(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.VMName)
	if err != nil {
		return err
	}
	guestClient, err := p.guestClient()
	if err != nil {
		return err
	}
	defer guestClient.Close()

	existing, err := guestClient.BlockDevices(p.ctx)
	if err != nil {
		return liberr.Wrap(err, "list guest block devices")
	}
	p.toehold.Status.NBD = make([]api.ToeholdNBDStatus, 0, len(p.toehold.Spec.Disks))

	for i, disk := range p.toehold.Spec.Disks {
		attached, attachErr := pctx.Client.AttachLinkedDisk(p.ctx, appliance.VM, p.toehold.Spec, disk)
		if attachErr != nil {
			return liberr.Wrap(attachErr, "attach disk", disk.Name)
		}
		if err = guestClient.Settle(p.ctx); err != nil {
			return liberr.Wrap(err, "udev settle", disk.Name)
		}
		device, devErr := guestClient.WaitForNewBlockDevice(p.ctx, existing, 2*time.Minute)
		if devErr != nil {
			return liberr.Wrap(devErr, "discover guest device", disk.Name)
		}
		existing[deviceName(device)] = true
		port := p.toehold.Spec.DiskPort(i, disk)
		p.toehold.Status.NBD = append(p.toehold.Status.NBD, api.ToeholdNBDStatus{
			Name:     disk.Name,
			SourceVM: disk.SourceVM,
			VMDKPath: attached.VMDKPath,
			Device:   device,
			Port:     port,
			Message:  fmt.Sprintf("attached at controller %d unit %d", attached.ControllerKey, attached.UnitNumber),
		})
	}
	p.toehold.Status.Message = "Migration disks attached"
	return nil
}

func (p *pipeline) stageStartNBD() error {
	if !p.toehold.Spec.ExportsDisks() {
		return nil
	}
	if err := p.ensureSSHKey(); err != nil {
		return err
	}
	guestClient, err := p.guestClient()
	if err != nil {
		return err
	}
	defer guestClient.Close()

	threads := p.toehold.Spec.NBDThreads()
	for i := range p.toehold.Status.NBD {
		entry := &p.toehold.Status.NBD[i]
		if entry.Device == "" {
			return liberr.New(fmt.Sprintf("disk %q has no guest device", entry.Name))
		}
		pidfile := guest.PIDFile(entry.Name)
		stopCmd := guest.StopCommand(pidfile)
		if _, err = guestClient.Run(p.ctx, stopCmd); err != nil {
			return liberr.Wrap(err, "stop existing nbdkit", entry.Name)
		}
		startCmd := guest.StartCommand(entry.Port, entry.Device, pidfile, threads)
		if _, err = guestClient.Run(p.ctx, startCmd); err != nil {
			return liberr.Wrap(err, "start nbdkit", entry.Name)
		}
		entry.Message = "nbdkit started"
	}
	p.toehold.Status.Message = "Starting NBD exports"
	return nil
}

func (p *pipeline) stageVerifyNBD() error {
	if !p.toehold.Spec.ExportsDisks() {
		return nil
	}
	if p.toehold.Status.VM.IP == "" {
		return errRequeue
	}
	if err := p.ensureSSHKey(); err != nil {
		return err
	}
	guestClient, err := p.guestClient()
	if err != nil {
		return err
	}
	defer guestClient.Close()

	ready := 0
	for i := range p.toehold.Status.NBD {
		entry := &p.toehold.Status.NBD[i]
		pidfile := guest.PIDFile(entry.Name)
		verifyCmd := guest.VerifyCommand(entry.Port, pidfile)
		if _, err = guestClient.Run(p.ctx, verifyCmd); err != nil {
			entry.Ready = false
			entry.URI = ""
			entry.Message = "nbdkit is not listening"
			continue
		}
		entry.Ready = true
		entry.URI = guest.ExportURI(p.toehold.Status.VM.IP, entry.Port)
		entry.Message = "export ready"
		ready++
	}
	if ready != len(p.toehold.Status.NBD) {
		p.toehold.Status.Message = fmt.Sprintf("Waiting for NBD exports (%d/%d ready)", ready, len(p.toehold.Status.NBD))
		return errRequeue
	}
	p.toehold.Status.Message = fmt.Sprintf("%d NBD export(s) ready", ready)
	return nil
}

func (p *pipeline) ensureSSHKey() error {
	if p.pctx == nil {
		var err error
		p.pctx, err = p.r.providerContext(p.ctx, p.toehold)
		if err != nil {
			return err
		}
	}
	if len(p.sshPrivateKey) > 0 {
		return nil
	}
	key, err := sshPrivateKey(p.ctx, *p.r, p.pctx.Provider)
	if err != nil {
		return liberr.Wrap(err, "ssh private key")
	}
	p.sshPrivateKey = key
	return nil
}

func (p *pipeline) guestClient() (*guest.Client, error) {
	if err := p.ensureSSHKey(); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()
	return guest.Dial(waitCtx, p.toehold.Status.VM.IP, p.toehold.Spec.NBDSSHUser(), p.sshPrivateKey)
}

func deviceName(devicePath string) string {
	if len(devicePath) > 5 && devicePath[:5] == "/dev/" {
		return devicePath[5:]
	}
	return devicePath
}

func (p *pipeline) stopExports() {
	if !p.toehold.Spec.ExportsDisks() || p.toehold.Status.VM.IP == "" {
		return
	}
	guestClient, err := p.guestClient()
	if err != nil {
		return
	}
	defer guestClient.Close()
	for _, entry := range p.toehold.Status.NBD {
		_, _ = guestClient.Run(p.ctx, guest.StopCommand(guest.PIDFile(entry.Name)))
	}
}

func (p *pipeline) detachDisks() error {
	if !p.toehold.Spec.ExportsDisks() {
		return nil
	}
	pctx, err := p.r.providerContext(p.ctx, p.toehold)
	if err != nil {
		return err
	}
	defer pctx.Client.Close(p.ctx)
	appliance, err := pctx.Client.FindVM(p.ctx, p.toehold.Spec.Folder, p.toehold.Spec.VMName)
	if err != nil {
		return nil
	}
	for _, disk := range p.toehold.Spec.Disks {
		_ = pctx.Client.DetachDiskByBacking(p.ctx, appliance.VM, disk.VMDKPath)
	}
	return nil
}
