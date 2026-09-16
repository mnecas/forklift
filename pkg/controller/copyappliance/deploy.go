package copyappliance

import (
	"context"

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
		r.context.Log.Error(err, "Deploy phase failed.", "phase", r.context.Appliance.Status.Phase)
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
		next = PhaseConfigure
		fallthrough
	case PhaseConfigure:
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

// Configure logs in to the appliance and applies the configuration it needs
// before it can serve exports. It reports whether the appliance is configured.
//
// An appliance that is not accepting connections yet is not a failure: the
// guest reports its address before sshd is answering on it. A rejected key, or
// a command the appliance fails, is.
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
	client, answered, err := r.context.SSHLogin(ctx, address)
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

	err = r.context.RunCommands(client, applianceConfigCommands...)
	if err != nil {
		return
	}
	r.context.Log.Info("Configured the appliance.", "address", address)
	done = true
	return
}

// applianceConfigCommands are run in order over one login, and the first that
// fails fails the deploy.
//
// The appliance image needs no configuration yet; this is where it goes when it
// does. Until then the step runs one command that does nothing, so that it
// proves the account can execute and not merely authenticate: a key restricted
// with command= in authorized_keys logs in and then refuses everything.
var applianceConfigCommands = []string{"true"}

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
// the migration reads from.
//
// NO-OP: the appliance does not publish its exports yet.
func (r *DeployRunner) WaitForExports(ctx context.Context) (done bool, err error) {
	done = true
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
			{Name: PhaseConfigure},
			{Name: PhaseLoadImage},
			{Name: PhaseWaitForExports},
			{Name: PhaseDeployCompleted},
			{Name: PhaseDeployFailed},
		},
	}
}
