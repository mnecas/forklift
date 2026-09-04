package vsphere

import (
	"context"
	"fmt"
	"strings"

	"github.com/kubev2v/forklift/pkg/toehold/annotations"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// VMRef holds a located virtual machine or template.
type VMRef struct {
	Name  string
	Moref string
	VM    *object.VirtualMachine
}

// FindTemplate locates a template by folder path and name.
func (c *Client) FindTemplate(ctx context.Context, folderPath, name string) (*VMRef, error) {
	return c.findVM(ctx, folderPath, name, true)
}

// FindVM locates a VM by folder path and name.
func (c *Client) FindVM(ctx context.Context, folderPath, name string) (*VMRef, error) {
	return c.findVM(ctx, folderPath, name, false)
}

func (c *Client) findVM(ctx context.Context, folderPath, name string, template bool) (*VMRef, error) {
	vm, err := c.Finder.VirtualMachine(ctx, name)
	if err != nil {
		path := strings.TrimPrefix(normalizeInventoryPath(folderPath), "/")
		if path != "" {
			vm, err = c.Finder.VirtualMachine(ctx, path+"/"+name)
		}
	}
	if err != nil {
		return nil, err
	}
	var o mo.VirtualMachine
	if err = vm.Properties(ctx, vm.Reference(), []string{"config.template", "name"}, &o); err != nil {
		return nil, err
	}
	if template && o.Config != nil && !o.Config.Template {
		return nil, fmt.Errorf("%q is not a template", name)
	}
	if !template && o.Config != nil && o.Config.Template {
		return nil, fmt.Errorf("%q is a template, expected VM", name)
	}
	return &VMRef{Name: o.Name, Moref: vm.Reference().Value, VM: vm}, nil
}

// DestroyVMIfExists removes a VM or template when present.
func (c *Client) DestroyVMIfExists(ctx context.Context, folderPath, name string) error {
	patterns := []string{name, "*/" + name}
	if path := normalizeInventoryPath(folderPath); path != "" {
		patterns = append(patterns, path+"/"+name)
	}
	seen := map[string]struct{}{}
	for _, pattern := range patterns {
		vms, err := c.Finder.VirtualMachineList(ctx, pattern)
		if err != nil {
			continue
		}
		for _, vm := range vms {
			id := vm.Reference().Value
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			if err := c.Destroy(ctx, vm); err != nil {
				return err
			}
		}
	}
	return nil
}

// GetAnnotationMap reads forklift toehold annotations from a VM/template.
func (c *Client) GetAnnotationMap(ctx context.Context, vm *object.VirtualMachine) (map[string]string, error) {
	var o mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"config"}, &o); err != nil {
		return nil, err
	}
	result := map[string]string{}
	if o.Config == nil {
		return result, nil
	}
	parseAnnotations(o.Config.Annotation, result)
	for _, extra := range o.Config.ExtraConfig {
		if val, ok := extra.(*types.OptionValue); ok {
			if strings.HasPrefix(val.Key, "forklift.konveyor.io/toehold-") {
				result[val.Key] = fmt.Sprint(val.Value)
			}
		}
	}
	return result, nil
}

// SetAnnotationMap writes forklift toehold annotations on a VM/template.
func (c *Client) SetAnnotationMap(ctx context.Context, vm *object.VirtualMachine, values map[string]string) error {
	existing, err := c.GetAnnotationMap(ctx, vm)
	if err != nil {
		return err
	}
	for k, v := range values {
		existing[k] = v
	}
	extra := []types.BaseOptionValue{}
	for _, key := range []string{
		annotations.ContentHash,
		annotations.BootcImage,
		annotations.BootcImageID,
		annotations.ImportedAt,
		annotations.VMContentHash,
	} {
		if v, ok := values[key]; ok {
			extra = append(extra, &types.OptionValue{Key: key, Value: v})
		}
	}
	spec := types.VirtualMachineConfigSpec{
		Annotation:  encodeAnnotations(existing),
		ExtraConfig: extra,
	}
	task, err := vm.Reconfigure(ctx, spec)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

// TemplateContentHashFromVM returns the stored template content hash.
func (c *Client) TemplateContentHashFromVM(ctx context.Context, vm *object.VirtualMachine) (string, error) {
	maps, err := c.GetAnnotationMap(ctx, vm)
	if err != nil {
		return "", err
	}
	return maps[annotations.ContentHash], nil
}

func parseAnnotations(annotation string, out map[string]string) {
	for _, line := range strings.Split(annotation, "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
}

func encodeAnnotations(values map[string]string) string {
	lines := []string{}
	for _, key := range []string{
		annotations.ContentHash,
		annotations.BootcImage,
		annotations.BootcImageID,
		annotations.ImportedAt,
		annotations.VMContentHash,
	} {
		if v, ok := values[key]; ok && v != "" {
			lines = append(lines, fmt.Sprintf("%s=%s", key, v))
		}
	}
	return strings.Join(lines, "\n")
}

// WaitForIP returns the guest IP when available.
func (c *Client) WaitForIP(ctx context.Context, vm *object.VirtualMachine) (string, error) {
	var o mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"guest.ipAddress"}, &o); err != nil {
		return "", err
	}
	if o.Guest != nil && o.Guest.IpAddress != "" {
		return o.Guest.IpAddress, nil
	}
	return "", nil
}

// PowerOn powers on a VM.
func (c *Client) PowerOn(ctx context.Context, vm *object.VirtualMachine) error {
	state, err := vm.PowerState(ctx)
	if err != nil {
		return err
	}
	if state == types.VirtualMachinePowerStatePoweredOn {
		return nil
	}
	task, err := vm.PowerOn(ctx)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

// SetEFIBoot configures UEFI firmware on a VM.
func (c *Client) SetEFIBoot(ctx context.Context, vm *object.VirtualMachine) error {
	spec := types.VirtualMachineConfigSpec{
		Firmware: string(types.GuestOsDescriptorFirmwareTypeEfi),
	}
	task, err := vm.Reconfigure(ctx, spec)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

// MarkAsTemplate marks a VM as a template.
func (c *Client) MarkAsTemplate(ctx context.Context, vm *object.VirtualMachine) error {
	return vm.MarkAsTemplate(ctx)
}

// Destroy removes a VM or template.
func (c *Client) Destroy(ctx context.Context, vm *object.VirtualMachine) error {
	state, err := vm.PowerState(ctx)
	if err == nil && state == types.VirtualMachinePowerStatePoweredOn {
		_, _ = vm.PowerOff(ctx)
	}
	_, err = vm.Destroy(ctx)
	return err
}

// WaitForGuestIP polls until a guest IP is reported or context ends.
func (c *Client) WaitForGuestIP(ctx context.Context, vm *object.VirtualMachine) (string, error) {
	pc := property.DefaultCollector(vm.Client())
	var ip string
	err := property.Wait(ctx, pc, vm.Reference(), []string{"guest.ipAddress"}, func(changes []types.PropertyChange) bool {
		for _, change := range changes {
			if change.Name == "guest.ipAddress" && change.Val != nil {
				if candidate, ok := change.Val.(string); ok && candidate != "" {
					ip = candidate
					return true
				}
			}
		}
		return false
	})
	if err != nil {
		return "", err
	}
	return ip, nil
}
