package copyappliance

import (
	"testing"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

// reportGuestNICs replaces what the simulated VM's guest has told vCenter. The
// simulator never populates an address of its own, so a test that needs one
// reaches past the API to the object the API reads.
func reportGuestNICs(t *testing.T, model *simulator.Model, vm *object.VirtualMachine, nics ...types.GuestNicInfo) {
	t.Helper()
	simulated, ok := model.Map().Get(vm.Reference()).(*simulator.VirtualMachine)
	if !ok {
		t.Fatalf("the simulator does not know VM %q", vm.Reference().Value)
	}
	simulated.Guest.Net = nics
}

// WaitForExports is written against what vCenter reports for a guest that is
// still coming up, which is an adapter with no address on it rather than no
// adapter at all. A hand-rolled fake would assert that assumption back at us.
func TestWaitForExportsAgainstSimulatedVCenter(t *testing.T) {
	t.Run("an appliance whose guest has not reported an address is not done", func(t *testing.T) {
		ctx, _, client := simulatedVCenter(t)
		applianceContext, _ := simulatedAppliance(t, ctx, client)
		runner := DeployRunner{context: applianceContext}

		// The simulated VM is left exactly as vcsim builds it: an adapter with
		// a MAC, no IP configuration, and VMware Tools not running.
		done, err := runner.WaitForExports(ctx)

		if err != nil {
			t.Fatalf("WaitForExports: %v", err)
		}
		if done {
			t.Error("deploy completed before the guest reported an address")
		}
		if len(applianceContext.Appliance.Status.Addresses) != 0 {
			t.Errorf("recorded %+v, want no addresses", applianceContext.Appliance.Status.Addresses)
		}
	})

	t.Run("an appliance is done once every configured network has an address", func(t *testing.T) {
		ctx, model, client := simulatedVCenter(t)
		applianceContext, vm := simulatedAppliance(t, ctx, client)
		runner := DeployRunner{context: applianceContext}
		spec := applianceContext.Appliance.Spec
		reportGuestNICs(t, model, vm,
			guestNIC(spec.ManagementNetwork, "00:50:56:01:02:03", "192.0.2.10"),
			guestNIC(spec.TransferNetwork, "00:50:56:04:05:06", "198.51.100.10"))

		done, err := runner.WaitForExports(ctx)

		if err != nil {
			t.Fatalf("WaitForExports: %v", err)
		}
		if !done {
			t.Error("deploy is still waiting though every network has an address")
		}
		addresses := applianceContext.Appliance.Status.Addresses
		if len(addresses) != 2 {
			t.Fatalf("recorded %+v, want an address per network", addresses)
		}
		management := addresses[0]
		if management.Network != spec.ManagementNetwork ||
			management.MAC != "00:50:56:01:02:03" ||
			management.IP != "192.0.2.10" {
			t.Errorf("management address = %+v", management)
		}
	})

	// An appliance built with a transfer network moves disk data over it. One
	// that only came up on its management network is not ready to be used.
	t.Run("an appliance with a transfer network waits for an address on it too", func(t *testing.T) {
		ctx, model, client := simulatedVCenter(t)
		applianceContext, vm := simulatedAppliance(t, ctx, client)
		runner := DeployRunner{context: applianceContext}
		spec := applianceContext.Appliance.Spec
		reportGuestNICs(t, model, vm,
			guestNIC(spec.ManagementNetwork, "00:50:56:01:02:03", "192.0.2.10"),
			guestNIC(spec.TransferNetwork, "00:50:56:04:05:06"))

		done, err := runner.WaitForExports(ctx)

		if err != nil {
			t.Fatalf("WaitForExports: %v", err)
		}
		if done {
			t.Error("deploy completed with no address on the transfer network")
		}
		if len(applianceContext.Appliance.Status.Addresses) != 1 {
			t.Errorf("recorded %+v, want only the management address",
				applianceContext.Appliance.Status.Addresses)
		}
	})

	// An appliance with no transfer network is given only a management NIC, so
	// waiting for a second address would park it forever.
	t.Run("an appliance with no transfer network is done on its management address", func(t *testing.T) {
		ctx, model, client := simulatedVCenter(t)
		applianceContext, vm := simulatedAppliance(t, ctx, client)
		applianceContext.Appliance.Spec.TransferNetwork = ""
		runner := DeployRunner{context: applianceContext}
		reportGuestNICs(t, model, vm,
			guestNIC(applianceContext.Appliance.Spec.ManagementNetwork,
				"00:50:56:01:02:03", "192.0.2.10"))

		done, err := runner.WaitForExports(ctx)

		if err != nil {
			t.Fatalf("WaitForExports: %v", err)
		}
		if !done {
			t.Error("deploy is still waiting though the management network has an address")
		}
	})

	// The VM can be destroyed out from under a deploy. That is a failure to
	// report, not a wait to sit in.
	t.Run("an appliance VM that is gone fails the wait", func(t *testing.T) {
		ctx, _, client := simulatedVCenter(t)
		applianceContext, _ := simulatedAppliance(t, ctx, client)
		applianceContext.Appliance.Status.MoRef = "vm-does-not-exist"
		runner := DeployRunner{context: applianceContext}

		_, err := runner.WaitForExports(ctx)

		if err == nil {
			t.Error("the wait accepted a VM that is not in the inventory")
		}
	})
}
