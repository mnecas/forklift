package ovf

import (
	"fmt"
	"strings"

	"github.com/vmware/govmomi/vmdk"
)

// DiskCapacity returns the virtual size of a stream-optimized VMDK.
func DiskCapacity(path string) (int64, error) {
	info, err := vmdk.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("vmdk stat %s: %w", path, err)
	}
	if info.Capacity <= 0 {
		return 0, fmt.Errorf("invalid virtual size for %s", path)
	}
	return int64(info.Capacity), nil
}

// ValidateDescriptor ensures generated OVF contains expected markers.
func ValidateDescriptor(content, name string) error {
	if content == "" {
		return fmt.Errorf("empty ovf descriptor")
	}
	if name != "" && !strings.Contains(content, name) {
		return fmt.Errorf("ovf missing name %q", name)
	}
	if !strings.Contains(content, `xmlns="http://schemas.dmtf.org/ovf/envelope/1"`) {
		return fmt.Errorf("ovf missing envelope namespace")
	}
	return nil
}
