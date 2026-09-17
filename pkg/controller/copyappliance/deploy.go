package copyappliance

import (
	"context"
	"errors"
	stderr "errors"
	"syscall"

	"github.com/google/go-containerregistry/pkg/v1/remote"
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
	"github.com/kubev2v/forklift/pkg/settings"
	"github.com/vmware/govmomi/vim25/types"
	"golang.org/x/crypto/ssh"
)

// DeployRunner drives the appliance VM from nothing to running. It holds no
// state of its own: every pass reads where it got to from the appliance status
// and leaves the next phase behind.
type DeployRunner struct {
	context *ApplianceContext
}

// Begin seeds the deploy itinerary and records the vCenter the appliance VM
// will belong to.
func (r *DeployRunner) Begin() {
	r.context.Appliance.Status.TaskRef = ""
	r.context.Appliance.Status.Phase = PhaseCloneVM
	r.context.Appliance.Status.VCenterInstanceUUID = r.context.InstanceUUID()
}

// Run advances the deployment by one pass.
func (r *DeployRunner) Run(ctx context.Context) (err error) {
	err = r.context.CheckInstance()
	if err != nil {
		return
	}

	next, err := r.ExecutePhase(ctx)
	if err != nil {
		log := []interface{}{"phase", r.context.Appliance.Status.Phase}
		var detail *liberr.Error
		if stderr.As(err, &detail) && len(detail.Context()) > 0 {
			log = append(log, "details", detail.Context())
		}
		r.context.Log.Error(err, "Deploy phase failed.", log...)
	}
	r.context.Appliance.Status.Phase = next
	return
}

// ExecutePhase runs the current phase and returns the phase to record. Steps
// that need no wait fall through to the next in the same pass; a step that is
// still waiting returns its own phase and picks up again on the next reconcile.
//
// A waiting step records the phase of the case it is in and not the phase the
// pass started on. Those differ whenever a pass has fallen through, and the
// recorded phase is both what the next pass re-enters at and what an operator
// reads to see which step an appliance is sitting in.
func (r *DeployRunner) ExecutePhase(ctx context.Context) (next string, err error) {
	switch r.context.Appliance.Status.Phase {
	case PhaseCloneVM:
		err = r.CloneVM(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		next = PhaseWaitForClone
		fallthrough
	case PhaseWaitForClone:
		var done bool
		done, err = r.WaitForClone(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = PhaseWaitForClone
			return
		}
		next = PhaseWaitForNetwork
		fallthrough
	case PhaseWaitForNetwork:
		var done bool
		done, err = r.WaitForNetwork(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = PhaseWaitForNetwork
			return
		}
		next = PhaseLoadImage
		fallthrough
	case PhaseLoadImage:
		var done bool
		done, err = r.LoadImage(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = PhaseLoadImage
			return
		}
		next = PhaseConfigure
		fallthrough
	case PhaseConfigure:
		// LoadImage used to run after this step and now runs before it. An
		// appliance an older controller left sitting here has therefore not
		// loaded its image, and the fallthrough chain only ever moves forward,
		// so it cannot reach a case above this one. Send it back, rather than
		// install a supervisor with no image to run.
		if r.context.Appliance.Status.LoadedImage == "" {
			next = PhaseLoadImage
			return
		}
		var done bool
		done, err = r.Configure(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = PhaseConfigure
			return
		}
		next = PhaseWaitForExports
		fallthrough
	case PhaseWaitForExports:
		var done bool
		done, err = r.WaitForExports(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = PhaseWaitForExports
			return
		}
		next = PhaseDeployCompleted
		fallthrough
	case PhaseDeployCompleted:
		observeExportRequest(r.context.Appliance)
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Deploying the copy appliance has succeeded.",
		})
		next = PhaseDeployCompleted
	case PhaseDeployFailed:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.False,
			Category: libcnd.Critical,
			Message:  "Deploying the copy appliance has failed.",
		})
		next = PhaseDeployFailed
	default:
		err = liberr.New("unknown phase", "phase", r.context.Appliance.Status.Phase)
		next = PhaseDeployFailed
	}
	return
}

// CloneVM clones the template into the appliance VM.
func (r *DeployRunner) CloneVM(ctx context.Context) (err error) {
	task, err := r.context.CloneVM(ctx)
	if err != nil {
		return
	}
	r.context.SetTask(task)
	return
}

// WaitForClone reports whether the appliance VM has finished cloning, and
// records the moRef the clone task named it with.
func (r *DeployRunner) WaitForClone(ctx context.Context) (done bool, err error) {
	done, result, err := r.context.WaitForTask(ctx)
	if err != nil {
		return
	}
	if !done {
		return
	}
	moRef, ok := result.(types.ManagedObjectReference)
	if !ok {
		err = liberr.New("task result is not a ManagedObjectRef", "result", result)
		return
	}
	r.context.Appliance.Status.MoRef = moRef.Reference().Value
	return
}

// WaitForNetwork reports whether the appliance can be reached, and records the
// addresses the guest reports itself on.
//
// The template gives the appliance one network, already configured, so there is
// nothing to match up: the appliance is reachable once the guest has booted far
// enough for VMware Tools to report an address at all.
func (r *DeployRunner) WaitForNetwork(ctx context.Context) (done bool, err error) {
	vm := r.context.VM(r.context.Appliance.Status.MoRef)
	addresses, err := r.context.GuestAddresses(ctx, vm)
	if err != nil {
		return
	}
	r.context.Appliance.Status.Addresses = addresses

	_, done = applianceAddress(addresses)
	return
}

// Configure installs the NBD orchestrator on the appliance and makes sure it is
// running. It reports whether the appliance is configured.
//
// The orchestrator is what turns the loaded image into exports: it finds the
// attached disks, runs one container per disk, and announces the result. It is
// installed as a systemd unit, and enabled, so that the appliance comes back
// supervising its disks after a reboot rather than idle.
//
// An appliance that is not accepting connections yet is not a failure: the
// guest reports its address before sshd is answering on it. Neither is a
// supervisor that is not up yet, which is only observable one moment after
// being asked to start. A rejected key, or a command the appliance answers, is.
func (r *DeployRunner) Configure(ctx context.Context) (done bool, err error) {
	// Recorded by WaitForNetwork on an earlier pass, so this costs no vCenter
	// round trip.
	address, ok := applianceAddress(r.context.Appliance.Status.Addresses)
	if !ok {
		err = liberr.New(
			"the appliance reports no address to reach it on",
			"appliance", r.context.Appliance.Name)
		return
	}
	// Both are read before logging in: neither depends on the appliance, and a
	// misconfigured secret should be reported without holding a connection.
	unit, err := r.context.renderUnit()
	if err != nil {
		return
	}
	certs, err := r.context.ServerTLS()
	if err != nil {
		return
	}

	// The orchestrator binary goes over this login, so it is opened for as long
	// as a transfer takes rather than as long as a command takes.
	client, answered, err := r.context.SSHLoginFor(ctx, address, sshTransferTimeout)
	if err != nil {
		return
	}
	if !answered {
		r.context.Log.Info("The appliance is not answering on SSH yet.", "address", address)
		return
	}
	defer func() {
		_ = client.Close()
	}()

	if err = r.context.EnsurePodmanWrapper(client); err != nil {
		return
	}

	// Asking first is what keeps a supervisor that cannot start from costing a
	// whole reinstall every few seconds, and from having its exports torn down
	// by the restart at the end of every one of them.
	installed, err := r.context.OrchestratorInstalled(client, unit, certs)
	if err != nil {
		return
	}
	if !installed {
		err = r.context.InstallOrchestrator(client, unit, certs)
		if err != nil {
			if !r.context.Alive(client) {
				// The link went away part way through. Nothing is known to be
				// wrong with the appliance, and a deploy that fails here cannot
				// be restarted, so this is a wait rather than a failure.
				r.context.Log.Info(
					"Lost the connection to the appliance while installing the supervisor.",
					"address", address,
					"error", err.Error())
				err = nil
			}
			return
		}
		r.context.Log.Info("Installed the appliance supervisor.",
			"address", address, "image", r.context.Appliance.Status.LoadedImage)
		// It was just restarted. Whether it stays up is a question for the next
		// pass; asked now it would only catch a process that had not yet got
		// round to failing.
		return
	}

	active, err := r.context.OrchestratorActive(client)
	if err != nil {
		return
	}
	if !active {
		// Installed but down. Starting it is all that is left to try, and if
		// that does not take either, the journal is the only thing that will
		// say why.
		sErr := r.context.StartOrchestrator(client)
		if sErr != nil {
			r.context.Log.Error(sErr, "Could not start the appliance supervisor.",
				"address", address)
		}
		r.context.Log.Info("The appliance supervisor is not running.",
			"address", address,
			"journal", r.context.OrchestratorLog(client))
		return
	}

	r.context.Log.Info("Configured the appliance.", "address", address)
	done = true
	return
}

// LoadImage puts the appliance's container image into its podman store, and
// reports whether the image is there.
//
// The transfer runs inside the pass and the pass waits for it, which for a few
// hundred megabytes means one of the controller's reconcile workers is held for
// minutes. What that buys is a step with no state to keep: it asks the
// appliance what it already has, so a controller that restarted part way
// through starts again rather than recovering anything.
func (r *DeployRunner) LoadImage(ctx context.Context) (done bool, err error) {
	address, ok := applianceAddress(r.context.Appliance.Status.Addresses)
	if !ok {
		err = liberr.New(
			"the appliance reports no address to reach it on",
			"appliance", r.context.Appliance.Name)
		return
	}
	// The transfer runs over this login, so it is opened for as long as a
	// transfer takes rather than as long as a command takes.
	client, answered, err := r.context.SSHLoginFor(ctx, address, sshTransferTimeout)
	if err != nil {
		return
	}
	if !answered {
		r.context.Log.Info("The appliance is not answering on SSH yet.", "address", address)
		return
	}
	defer func() {
		_ = client.Close()
	}()

	if err = r.context.EnsurePodmanWrapper(client); err != nil {
		return
	}

	// Asking the appliance is what makes the step idempotent. Without it a
	// re-entry sends the whole image again.
	loaded := r.context.Appliance.Status.LoadedImage
	if loaded != "" {
		done, err = r.context.Probe(client, "podman image exists "+loaded)
		if err != nil || done {
			return
		}
	}

	spec, err := resolveImage(ctx, r.context.Appliance.Spec.ContainerImage)
	if err != nil {
		return
	}
	registry, err := clusterRegistry(settings.ServiceCAFile, serviceAccountTokenFile)
	if err != nil {
		return
	}
	return r.loadImage(ctx, client, spec, registry...)
}

// loadImage is LoadImage from a resolved pull spec, which is everything about
// the step that does not need a cluster to run.
func (r *DeployRunner) loadImage(ctx context.Context, client *ssh.Client, spec string, registry ...remote.Option) (done bool, err error) {
	img, err := registryImage(ctx, spec, registry...)
	if err != nil {
		return
	}
	ref, err := loadedReference(img)
	if err != nil {
		return
	}
	// Recorded before the transfer rather than after it. A load that is cut off
	// can still leave the image in the store, and the next pass has to know
	// what to ask about.
	r.context.Appliance.Status.LoadedImage = ref.Name()
	r.context.Log.Info("Loading the appliance image.", "image", spec, "as", ref.Name())

	err = r.context.streamImage(client, img, ref)
	if err != nil {
		return
	}
	r.context.Log.Info("Loaded the appliance image.", "image", ref.Name())
	done = true
	return
}

// WaitForExports reports whether the appliance has published the disk exports
// the migration reads from, and records them.
func (r *DeployRunner) WaitForExports(ctx context.Context) (done bool, err error) {
	return r.context.WaitForExports(ctx)
}

var errExportsIncomplete = errors.New("not all attached disks are exported yet")

// exportsNotReady reports whether a failed query means the appliance is not
// serving yet rather than that something is wrong with what it serves. The
// announce endpoint comes up after sshd does, so for a while there is nothing
// listening; and like sshd it accepts a connection slightly before it can talk
// over it, which ends the handshake with no reply rather than with a refusal.
//
// A certificate that does not verify is deliberately not in here. That does not
// improve by waiting.
func exportsNotReady(err error) (notReady bool) {
	var timeout interface{ Timeout() bool }
	notReady = isStarting(err) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		(errors.As(err, &timeout) && timeout.Timeout())
	return
}

// applianceAddress returns the address to reach the appliance at. The appliance
// has one network, so the choice is only between the addresses the guest holds
// on it; the first is the one the guest listed first, and it answers on any of
// them.
func applianceAddress(addresses []api.ApplianceAddress) (address string, ok bool) {
	if len(addresses) == 0 {
		return
	}
	address = addresses[0].IP
	ok = true
	return
}

// Itinerary is the ordered pipeline of deploy phases.
func (r *DeployRunner) Itinerary() *libitr.Itinerary {
	return &libitr.Itinerary{
		Name: "Deploy",
		Pipeline: libitr.Pipeline{
			{Name: PhaseCloneVM},
			{Name: PhaseWaitForClone},
			{Name: PhaseWaitForNetwork},
			{Name: PhaseLoadImage},
			{Name: PhaseConfigure},
			{Name: PhaseWaitForExports},
			{Name: PhaseDeployCompleted},
			{Name: PhaseDeployFailed},
		},
	}
}
