package vsphere

import (
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	planapi "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/plan"
	plancontext "github.com/kubev2v/forklift/pkg/controller/plan/context"
	"github.com/kubev2v/forklift/pkg/lib/logging"
)

type ApplianceMigrator struct {
	*plancontext.Context
}

func (r *ApplianceMigrator) Logger() (logger logging.LevelLogger) { return r.Log }

func (r *ApplianceMigrator) Status(vm planapi.VM) (status *planapi.VMStatus) {
	if current, found := r.Context.Plan.Status.Migration.FindVM(vm.Ref); !found {
		status = &planapi.VMStatus{VM: vm}
	} else {
		status = current
	}
	return
}

func (r *ApplianceMigrator) Reset(vm *planapi.VMStatus, pipeline []*planapi.Step) {
	vm.DeleteCondition(api.ConditionCanceled, api.ConditionFailed)
	vm.MarkReset()
	itr := r.Itinerary(vm.VM)
	step, _ := itr.First()
	vm.Phase = step.Name
	vm.Pipeline = pipeline
	vm.Error = nil
	vm.Warm = nil
}

func (r *ApplianceMigrator) ExecutePhase(vm *planapi.VMStatus) (ok bool, err error) {
	switch vm.Phase {
	// 1. Snapshots of VM
	// 2. Create appliance VM that is attached to parents of snapshots
	// 3. Wait for appliance VM to be ready (something in the copyappliance CR status with its address and exports)
	// 4.
	}
	return
}
