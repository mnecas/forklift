package copyappliance

import (
	"context"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

// simulatedVCenter starts a vcsim instance and returns a client connected to
// it. The server is closed when the test ends.
func simulatedVCenter(t *testing.T) (ctx context.Context, client *govmomi.Client) {
	t.Helper()
	ctx = context.Background()
	model := simulator.VPX()
	err := model.Create()
	if err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)
	client, err = govmomi.NewClient(ctx, server.URL, true)
	if err != nil {
		t.Fatalf("connect to simulator: %v", err)
	}
	return
}

// simulatedAppliance adopts one of the simulator's VMs as the appliance VM.
func simulatedAppliance(t *testing.T, ctx context.Context, client *govmomi.Client) (*ApplianceContext, *object.VirtualMachine) {
	t.Helper()
	vm, err := find.NewFinder(client.Client, false).VirtualMachine(ctx, "/DC0/vm/DC0_H0_VM0")
	if err != nil {
		t.Fatalf("find simulated VM: %v", err)
	}
	appliance := testAppliance()
	appliance.Status.MoRef = vm.Reference().Value
	appliance.Status.VCenterInstanceUUID = client.ServiceContent.About.InstanceUuid
	return &ApplianceContext{Appliance: appliance, VCenter: client, Log: testLog()}, vm
}

// runTeardown drives the runner the way the reconciler does, one pass at a
// time, until it settles. The bound is what makes a runner that parks on a
// phase forever a failure instead of a hang.
func runTeardown(t *testing.T, ctx context.Context, runner TeardownRunner) (phases []string) {
	t.Helper()
	status := &runner.context.Appliance.Status
	for pass := 0; pass < 10; pass++ {
		err := runner.Run(ctx)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		phases = append(phases, status.Phase)
		if status.Phase == PhaseTeardownCompleted || status.Phase == PhaseTeardownFailed {
			return
		}
	}
	t.Fatalf("teardown did not settle; phases: %v", phases)
	return
}

// The teardown steps are written against what vSphere actually does with a
// power off, a reconfigure that removes disks, and a destroy. A hand-rolled
// fake would assert those assumptions back at us.
func TestTeardownAgainstSimulatedVCenter(t *testing.T) {
	t.Run("teardown runs to completion and leaves no VM behind", func(t *testing.T) {
		ctx, client := simulatedVCenter(t)
		applianceContext, vm := simulatedAppliance(t, ctx, client)
		runner := TeardownRunner{context: applianceContext}

		runner.Begin()
		phases := runTeardown(t, ctx, runner)

		status := applianceContext.Appliance.Status
		if status.Phase != PhaseTeardownCompleted {
			t.Fatalf("phase = %q after %v, want %q", status.Phase, phases, PhaseTeardownCompleted)
		}
		if status.MoRef != "" || status.TaskRef != "" {
			t.Errorf("status still names a VM or a task: %+v", status)
		}
		_, err := vm.PowerState(ctx)
		if err == nil {
			t.Error("the appliance VM is still in the inventory")
		}
	})

	t.Run("an appliance with no recorded VM is torn down in a single pass", func(t *testing.T) {
		ctx, client := simulatedVCenter(t)
		appliance := testAppliance()
		runner := TeardownRunner{
			context: &ApplianceContext{Appliance: appliance, VCenter: client, Log: testLog()},
		}

		runner.Begin()
		phases := runTeardown(t, ctx, runner)

		if len(phases) != 1 {
			t.Errorf("took %d passes (%v), want 1", len(phases), phases)
		}
		if appliance.Status.Phase != PhaseTeardownCompleted {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, PhaseTeardownCompleted)
		}
	})

	// A step with nothing to do records no task, and its wait step reads that
	// as done. If any of these started a task anyway, teardown would wait on a
	// power off that never happens.
	t.Run("a step with nothing to do starts no task", func(t *testing.T) {
		ctx, client := simulatedVCenter(t)
		applianceContext, vm := simulatedAppliance(t, ctx, client)
		runner := TeardownRunner{context: applianceContext}
		status := &applianceContext.Appliance.Status

		powerOff, err := vm.PowerOff(ctx)
		if err != nil {
			t.Fatalf("power off the simulated VM: %v", err)
		}
		err = powerOff.Wait(ctx)
		if err != nil {
			t.Fatalf("power off the simulated VM: %v", err)
		}

		err = runner.PowerOff(ctx)
		if err != nil {
			t.Fatalf("PowerOff: %v", err)
		}
		if status.TaskRef != "" {
			t.Errorf("a VM that is already off started task %q", status.TaskRef)
		}

		detach, err := applianceContext.DetachDisks(ctx, vm)
		if err != nil {
			t.Fatalf("DetachDisks: %v", err)
		}
		err = detach.Wait(ctx)
		if err != nil {
			t.Fatalf("detach the disks: %v", err)
		}
		err = runner.DetachDisks(ctx)
		if err != nil {
			t.Fatalf("DetachDisks: %v", err)
		}
		if status.TaskRef != "" {
			t.Errorf("a VM with no disks started task %q", status.TaskRef)
		}
	})

	// The VM can be destroyed out from under us between one reconcile and the
	// next. That is the outcome teardown wants, not a failure to report.
	t.Run("a VM that is already gone completes teardown", func(t *testing.T) {
		ctx, client := simulatedVCenter(t)
		applianceContext, vm := simulatedAppliance(t, ctx, client)
		runner := TeardownRunner{context: applianceContext}

		// vSphere refuses to destroy a running VM, so the simulator does too.
		powerOff, err := vm.PowerOff(ctx)
		if err != nil {
			t.Fatalf("power off the simulated VM: %v", err)
		}
		err = powerOff.Wait(ctx)
		if err != nil {
			t.Fatalf("power off the simulated VM: %v", err)
		}
		destroy, err := vm.Destroy(ctx)
		if err != nil {
			t.Fatalf("destroy the simulated VM: %v", err)
		}
		err = destroy.Wait(ctx)
		if err != nil {
			t.Fatalf("destroy the simulated VM: %v", err)
		}

		runner.Begin()
		phases := runTeardown(t, ctx, runner)

		if applianceContext.Appliance.Status.Phase != PhaseTeardownCompleted {
			t.Errorf("phase = %q after %v, want %q",
				applianceContext.Appliance.Status.Phase, phases, PhaseTeardownCompleted)
		}
	})

	// A moRef recorded against another vCenter names some unrelated VM here.
	// Teardown must refuse rather than destroy it.
	t.Run("a VM recorded against another vCenter is not destroyed", func(t *testing.T) {
		ctx, client := simulatedVCenter(t)
		applianceContext, vm := simulatedAppliance(t, ctx, client)
		applianceContext.Appliance.Status.VCenterInstanceUUID = "some-other-vcenter"
		runner := TeardownRunner{context: applianceContext}

		runner.Begin()
		err := runner.Run(ctx)

		if err == nil {
			t.Fatal("teardown accepted a moRef from another vCenter")
		}
		state, err := vm.PowerState(ctx)
		if err != nil {
			t.Fatalf("the VM was destroyed: %v", err)
		}
		if state != types.VirtualMachinePowerStatePoweredOn {
			t.Errorf("power state = %v, want the VM left alone", state)
		}
	})
}
