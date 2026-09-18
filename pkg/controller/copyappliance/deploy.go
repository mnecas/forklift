package copyappliance

import (
	"context"
	"errors"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
	"github.com/vmware/govmomi/vim25/types"
	"golang.org/x/crypto/ssh"
)

// DeployRunner drives the appliance VM from nothing to running. It holds no
// state of its own: every pass reads where it got to from the appliance status
// and leaves the next phase behind.
type DeployRunner struct {
	context *ApplianceContext
	// registry reads the appliance's container image out of the cluster's
	// internal registry. Nil means the real one, built on first use; a test
	// supplies its own.
	registry *ClusterRegistry
}

// clusterRegistry returns the registry the appliance image is read from,
// building the real one on first use.
func (r *DeployRunner) clusterRegistry() (registry *ClusterRegistry, err error) {
	if r.registry == nil {
		r.registry, err = NewClusterRegistry()
		if err != nil {
			return
		}
	}
	registry = r.registry
	return
}

// Begin seeds the deploy itinerary and records the vCenter the appliance VM
// will belong to.
func (r *DeployRunner) Begin() (err error) {
	r.context.Appliance.Status.TaskRef = ""
	r.context.Appliance.Status.VCenterInstanceUUID = r.context.InstanceUUID()
	step, err := r.Itinerary().First()
	if err != nil {
		return
	}
	r.context.Appliance.Status.Phase = step.Name
	return
}

// Run advances the deployment by one pass.
func (r *DeployRunner) Run(ctx context.Context) (err error) {
	err = r.context.CheckInstance()
	if err != nil {
		return
	}

	err = advance(
		ctx,
		r.context.Appliance,
		r.Itinerary(),
		r.execute,
		PhaseDeployCompleted,
		PhaseDeployFailed)
	if err != nil {
		log := []interface{}{"phase", r.context.Appliance.Status.Phase}
		var detail *liberr.Error
		if errors.As(err, &detail) && len(detail.Context()) > 0 {
			log = append(log, "details", detail.Context())
		}
		r.context.Log.Error(err, "Deploy phase failed.", log...)
	}
	return
}

// execute runs one deploy step and reports whether it finished. A step with
// nothing to wait for finishes, and the walk moves on to the next step within
// the same pass. The terminal phases report not finished, which parks the walk
// on them.
func (r *DeployRunner) execute(ctx context.Context, phase string) (done bool, err error) {
	switch phase {
	case PhaseCloneVM:
		err = r.CloneVM(ctx)
		if err != nil {
			return
		}
		done = true
	case PhaseWaitForClone:
		done, err = r.WaitForClone(ctx)
	case PhaseWaitForNetwork:
		done, err = r.WaitForNetwork(ctx)
	case PhaseLoadImage:
		done, err = r.InjectImage(ctx)
	case PhaseConfigure:
		// LoadImage used to run after this step and now runs before it. An
		// appliance an older controller left sitting here has therefore not
		// loaded its image. Send it back, rather than install a supervisor with
		// no image to run.
		if r.context.Appliance.Status.ExporterImage == "" {
			r.context.Appliance.Status.Phase = PhaseLoadImage
			return
		}
		done, err = r.Configure(ctx)
	case PhaseWaitForExports:
		done, err = r.WaitForExports(ctx)
	case PhaseDeployCompleted:
		r.context.observeExportRequest(r.context.Appliance)
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Deploying the copy appliance has succeeded.",
		})
	case PhaseDeployFailed:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.False,
			Category: libcnd.Critical,
			Message:  "Deploying the copy appliance has failed.",
		})
	default:
		err = liberr.New("unknown phase", "phase", phase)
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
// running.
func (r *DeployRunner) Configure(ctx context.Context) (done bool, err error) {
	address, ok := applianceAddress(r.context.Appliance.Status.Addresses)
	if !ok {
		err = liberr.New(
			"the appliance reports no address to reach it on",
			"appliance", r.context.Appliance.Name)
		return
	}

	unit, err := r.context.renderUnit()
	if err != nil {
		return
	}
	certs, err := r.context.ServerTLS()
	if err != nil {
		return
	}

	client, answered, err := r.context.SSHLoginFor(ctx, address, SSHFileTransferTimeout)
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
			"address", address, "image", r.context.Appliance.Status.ExporterImage)
		return
	}

	active, err := r.context.OrchestratorActive(client)
	if err != nil {
		return
	}
	if !active {
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

// InjectImage copies the appliance container image from the cluster registry
// into the appliance VM's container registry.
// TODO: Find a way to do the image upload that doesn't block the reconciler.
func (r *DeployRunner) InjectImage(ctx context.Context) (done bool, err error) {
	client, answered, err := r.context.SSHClient(ctx, SSHFileTransferTimeout)
	if err != nil {
		return
	}
	if !answered {
		r.context.Log.Info("The appliance is not answering on SSH yet.")
		return
	}
	defer func() {
		_ = client.Close()
	}()
	if err = r.context.EnsurePodmanWrapper(client); err != nil {
		return
	}
	// Check if the image is already present in the podman registry
	loaded := r.context.Appliance.Status.ExporterImage
	if loaded != "" {
		done, err = r.context.Probe(client, "podman image exists "+loaded)
		if err != nil || done {
			return
		}
	}
	registry, err := r.clusterRegistry()
	if err != nil {
		return
	}
	img, err := registry.Image(ctx, r.context.Appliance.Spec.ContainerImage)
	if err != nil {
		return
	}
	return r.injectImage(client, img)
}

func (r *DeployRunner) injectImage(client *ssh.Client, img v1.Image) (done bool, err error) {
	tag, err := makeTag(img)
	if err != nil {
		return
	}
	// Recorded before the transfer rather than after it. A load that is cut off
	// can still leave the image in the store, and the next pass has to know
	// what to ask about.
	r.context.Appliance.Status.ExporterImage = tag.Name()
	r.context.Log.Info("Starting to stream the exporter image.",
		"imageStreamTag", r.context.Appliance.Spec.ContainerImage,
		"as", tag.Name())

	err = r.context.streamImage(client, img, tag)
	if err != nil {
		return
	}
	r.context.Log.Info("Done streaming the exporter image.", "image", tag.Name())
	done = true
	return
}

// WaitForExports reports whether the appliance has published the disk exports
// the migration reads from, and records them.
func (r *DeployRunner) WaitForExports(ctx context.Context) (done bool, err error) {
	return r.context.WaitForExports(ctx)
}

// Itinerary is the ordered pipeline of deploy phases. PhaseDeployFailed is not
// in it: a failure is not a step the walk arrives at, it is where the walk ends
// when a step returns an error.
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
		},
	}
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
