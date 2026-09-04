package ovf

import (
	"encoding/binary"
	"fmt"
	"os"
)

// DiskCapacity returns the virtual size of a stream-optimized VMDK when available.
func DiskCapacity(path string) (int64, error) {
	size, err := fileSize(path)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return size, nil
	}
	defer f.Close()

	header := make([]byte, 512)
	if _, err := f.Read(header); err != nil {
		return size, nil
	}
	if string(header[0:4]) != "KDMV" {
		return size, nil
	}
	// Stream-optimized VMDK stores capacity in grain table region; use descriptor if present.
	capacity := int64(binary.LittleEndian.Uint64(header[0x30:0x38]))
	if capacity > 0 {
		return capacity, nil
	}
	return size, nil
}

// WriteDescriptorFile writes the OVF descriptor next to the VMDK.
func WriteDescriptorFile(opts DescriptorOptions, output string) error {
	content, err := Descriptor(opts)
	if err != nil {
		return err
	}
	if output == "" {
		output = opts.Name + ".ovf"
	}
	return os.WriteFile(output, []byte(content), 0o644)
}

// ValidateDescriptor ensures generated OVF contains expected markers.
func ValidateDescriptor(content, name string) error {
	if content == "" {
		return fmt.Errorf("empty ovf descriptor")
	}
	if name != "" && !contains(content, name) {
		return fmt.Errorf("ovf missing name %q", name)
	}
	return nil
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || len(s) > 0 && stringIndex(s, sub) >= 0))
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
