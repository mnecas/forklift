package copyappliance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
	"github.com/vmware/govmomi/vim25/types"
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

// begin seeds the deploy pipeline and records the vCenter the appliance VM
// will belong to.
func (r *DeployRunner) begin() (err error) {
	r.context.Appliance.Status.TaskRef = ""
	r.context.Appliance.Status.VCenterInstanceUUID = r.context.InstanceUUID()
	step, err := r.itinerary().First()
	if err != nil {
		return
	}
	r.context.Appliance.Status.Phase = step.Name
	return
}

// Run runs the current deploy phase once and reports how long to wait before
// the next pass. A finished step sets Status.Phase to its successor; the next
// reconcile picks it up.
func (r *DeployRunner) Run(ctx context.Context) (reQ time.Duration, err error) {
	// Ended() swallows the error and controller-runtime applies no backoff of
	// its own, so a failed pass would otherwise retry against vCenter forever
	// at the error cadence.
	defer func() {
		if err != nil {
			reQ = base.LongReQ
		}
	}()
	// Only an appliance that has not started yet is seeded. A phase outside
	// the pipeline is a failed deploy, which execute parks; seeding it would
	// restart the deploy on every pass.
	if r.context.Appliance.Status.Phase == "" {
		err = r.begin()
		if err != nil {
			return
		}
	}
	err = r.context.CheckInstance()
	if err != nil {
		return
	}
	reQ, err = r.execute(ctx)
	if err != nil {
		r.context.Appliance.Status.Phase = r.failedPhase()
		log := []interface{}{"phase", r.context.Appliance.Status.Phase}
		var detail *liberr.Error
		if errors.As(err, &detail) && len(detail.Context()) > 0 {
			log = append(log, "details", detail.Context())
		}
		r.context.Log.Error(err, "Deploy phase failed.", log...)
	}
	return
}

func (r *DeployRunner) itinerary() *libitr.Itinerary {
	return &libitr.Itinerary{
		Name: "Deploy",
		Pipeline: libitr.Pipeline{
			{Name: api.PhaseCloneVM},
			{Name: api.PhaseWaitForClone},
			{Name: api.PhaseWaitForNetwork},
			{Name: api.PhaseLoadImage},
			{Name: api.PhaseConfigure},
			{Name: api.PhaseWaitForExports},
			{Name: api.PhaseDeployCompleted},
		},
	}
}

func (r *DeployRunner) failedPhase() string {
	return api.PhaseDeployFailed
}

// NextPhase sets Status.Phase to itinerary.Next of the current phase.
func (r *DeployRunner) NextPhase() {
	nextPhase(r.context.Appliance, r.itinerary())
}

// nextPhase sets Status.Phase to itinerary.Next of the current phase.
func nextPhase(appliance *api.CopyAppliance, itinerary *libitr.Itinerary) {
	step, done, err := itinerary.Next(appliance.Status.Phase)
	if done || err != nil {
		return
	}
	appliance.Status.Phase = step.Name
}

// execute runs the phase the appliance is on and reports how long to wait
// before running the next one. A step that advances the phase asks for no wait:
// writing the new phase to the status fires the watch, which brings the next
// pass back at once. A step still waiting writes nothing, so nothing wakes us,
// and it names the interval it wants to be polled at. Every wait here is on
// vSphere, so those intervals are deliberately slow: each reconcile opens and
// closes a vCenter session, and FastReQ would mean two logins per second per CR.
func (r *DeployRunner) execute(ctx context.Context) (reQ time.Duration, err error) {
	switch r.context.Appliance.Status.Phase {
	case api.PhaseCloneVM:
		task, cloneErr := r.context.CloneVM(ctx)
		if cloneErr != nil {
			err = cloneErr
			return
		}
		r.context.SetTask(task)
		r.NextPhase()
	case api.PhaseWaitForClone:
		done, waitErr := r.WaitForClone(ctx)
		if waitErr != nil {
			err = waitErr
			return
		}
		if done {
			r.NextPhase()
			return
		}
		// A vSphere task, which settles in seconds.
		reQ = base.SlowReQ
	case api.PhaseWaitForNetwork:
		done, waitErr := r.WaitForNetwork(ctx)
		if waitErr != nil {
			err = waitErr
			return
		}
		if done {
			r.NextPhase()
			return
		}
		// Slower than the task waits. Those are waiting on vSphere, which
		// settles in seconds; this one is waiting on a guest to boot and on
		// VMware Tools to start answering, which takes a minute or more.
		// Polling it at the task cadence buys nothing but vCenter logins.
		reQ = base.LongReQ
	case api.PhaseLoadImage:
		done, injectErr := r.InjectImage(ctx)
		if injectErr != nil {
			err = injectErr
			return
		}
		if done {
			r.NextPhase()
			return
		}
		// The first step to log in, so the only thing waited on here is sshd
		// answering on an address the guest has already reported. That gap is
		// seconds, so backing off to LongReQ would add half a minute to a
		// deploy that is nearly done. The load itself runs to completion
		// inside the pass and is never waited on.
		reQ = base.SlowReQ
	case api.PhaseConfigure:
		// LoadImage used to run after this step and now runs before it. An
		// appliance an older controller left sitting here has therefore not
		// loaded its image. Send it back, rather than install a supervisor with
		// no image to run.
		if r.context.Appliance.Status.ExporterImage == "" {
			r.context.Appliance.Status.Phase = api.PhaseLoadImage
			return
		}
		done, cfgErr := r.Configure(ctx)
		if cfgErr != nil {
			err = cfgErr
			return
		}
		if done {
			r.NextPhase()
			return
		}
		// Two things are waited on here: the appliance not answering yet, and a
		// supervisor that will not come up. The second one repeats forever,
		// which is only affordable because the step asks whether the install is
		// already in place before it does anything: a pass that finds it is two
		// short commands.
		reQ = base.SlowReQ
	case api.PhaseWaitForExports:
		done, waitErr := r.context.WaitForExports(ctx)
		if waitErr != nil {
			err = waitErr
			return
		}
		if done {
			r.NextPhase()
			// Ready must be set on this pass: the reconciler idles on
			// DeployCompleted, so the case below would never run.
			r.context.Appliance.Status.SetCondition(libcnd.Condition{
				Type:     libcnd.Ready,
				Status:   libcnd.True,
				Category: libcnd.Required,
				Message:  "Deploying the copy appliance has succeeded.",
			})
			return
		}
		// Waiting on the guest to enumerate its disks and bring up a container
		// for each, which is tens of seconds. Same reasoning as
		// api.PhaseWaitForNetwork.
		reQ = base.LongReQ
	case api.PhaseDeployCompleted:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Deploying the copy appliance has succeeded.",
		})
	case api.PhaseDeployFailed:
		// Keep the detailed fault from setFailed when present; only fall back
		// to a generic message if nothing recorded the root cause.
		msg := FailureReason(r.context.Appliance)
		if msg == "the appliance did not record why it failed" {
			msg = "Deploying the copy appliance has failed."
		}
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.False,
			Reason:   api.PhaseDeployFailed,
			Category: libcnd.Critical,
			Message:  msg,
			Durable:  true,
		})
		// Ended() swallows the error and controller-runtime applies no backoff
		// of its own, so a failed appliance would otherwise retry against
		// vCenter forever at the error cadence.
		reQ = base.LongReQ
	default:
		err = liberr.New("unknown phase", "phase", r.context.Appliance.Status.Phase)
	}
	return
}

// WaitForClone reports whether the appliance VM has finished cloning, and
// records the moRef. Adopt (MoRef set, no task) counts as done.
func (r *DeployRunner) WaitForClone(ctx context.Context) (done bool, err error) {
	done, result, err := r.context.WaitForTask(ctx)
	if err != nil {
		return
	}
	if !done {
		return
	}
	if r.context.Appliance.Status.MoRef != "" && result == nil {
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
// addresses the guest reports itself on. CloneSpec.PowerOn is asynchronous to
// the clone task, so a powered-off VM may still be coming up; a failed power-on
// task is reported instead of waiting forever for an address.
func (r *DeployRunner) WaitForNetwork(ctx context.Context) (done bool, err error) {
	vm := r.context.VM(r.context.Appliance.Status.MoRef)

	state, err := vm.PowerState(ctx)
	if err != nil {
		err = liberr.Wrap(err, "vm", r.context.Appliance.Status.MoRef)
		return
	}
	if state == types.VirtualMachinePowerStatePoweredOff {
		if fault := r.context.recentTaskFault(ctx, vm); fault != "" {
			err = liberr.New("appliance VM failed to power on: " + fault)
		}
		return
	}

	addresses, err := r.context.GuestAddresses(ctx, vm)
	if err != nil {
		return
	}
	r.context.Appliance.Status.Addresses = addresses

	_, done = r.context.Appliance.Address()
	return
}

// Configure installs the NBD orchestrator on the appliance and makes sure it is
// running.
func (r *DeployRunner) Configure(ctx context.Context) (done bool, err error) {
	address, _ := r.context.Appliance.Address()

	orch, ready, err := NewOrchestrator(ctx, r.context, SSHFileTransferTimeout)
	if err != nil {
		return
	}
	if !ready {
		r.context.Log.Info("The appliance is not answering on SSH yet.",
			"address", address)
		return
	}
	defer func() {
		_ = orch.Close()
	}()

	installed, err := orch.Installed()
	if err != nil {
		return
	}
	if !installed {
		err = orch.Install()
		if err != nil {
			if !IsExitError(err) {
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

	active, err := orch.Active()
	if err != nil {
		return
	}
	if !active {
		sErr := orch.Start()
		if sErr != nil {
			r.context.Log.Error(sErr, "Could not start the appliance supervisor.",
				"address", address)
		}
		r.context.Log.Info("The appliance supervisor is not running.",
			"address", address,
			"journal", orch.Log())
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
	client, ready, err := r.context.SSHClient(ctx, SSHFileTransferTimeout)
	if err != nil {
		return
	}
	if !ready {
		r.context.Log.Info("The appliance is not answering on SSH yet.")
		return
	}
	defer func() {
		_ = client.Close()
	}()
	if r.registry == nil {
		r.registry, err = NewClusterRegistry()
		if err != nil {
			return
		}
	}
	img, err := r.registry.Image(ctx, r.context.Appliance.Spec.ContainerImage)
	if err != nil {
		return
	}
	return r.injectImage(client, img)
}

func (r *DeployRunner) injectImage(client *SSHClient, img v1.Image) (done bool, err error) {
	tag, err := makeTag(img)
	if err != nil {
		return
	}
	r.context.Appliance.Status.ExporterImage = tag.Name()
	r.context.Log.Info("Starting to stream the exporter image.",
		"image", r.context.Appliance.Spec.ContainerImage,
		"as", tag.Name())

	err = r.streamImage(client, img, tag)
	if err != nil {
		return
	}
	r.context.Log.Info("Done streaming the exporter image.", "image", tag.Name())
	done = true
	return
}

// streamImage writes the image into the appliance's podman store as a docker
// archive on the load command's standard input.
func (r *DeployRunner) streamImage(client *SSHClient, img v1.Image, ref name.Tag) (err error) {
	reader, writer := io.Pipe()
	go func() {
		// A failure part way through arrives at the load side as a read error,
		// rather than as a truncated archive that podman would reject for the
		// wrong reason.
		_ = writer.CloseWithError(tarball.Write(ref, img, writer))
	}()
	// Closing the read half is what unblocks the writer when the load command
	// gives up before the archive is finished.
	defer func() {
		_ = reader.Close()
	}()

	err = client.RunWithStdin(AppliancePodmanLoadCommand, reader)
	return
}

// makeTag makes a tag for the image from its digest.
func makeTag(img v1.Image) (tag name.Tag, err error) {
	digest, err := img.Digest()
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	tag, err = name.NewTag(fmt.Sprintf("%s:%s", ApplianceContainerImageName, digest.Hex), name.WithDefaultRegistry(""))
	if err != nil {
		err = liberr.Wrap(err, "digest", digest.String())
		return
	}
	return
}
