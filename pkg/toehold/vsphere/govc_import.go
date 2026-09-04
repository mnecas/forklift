package vsphere

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	toeholdovf "github.com/kubev2v/forklift/pkg/toehold/ovf"
)

const govcBin = "/usr/local/bin/govc"

// ImportOVFViaGovc imports a VMDK using govc import.ovf (same flow as appliance PoC).
func (c *Client) ImportOVFViaGovc(ctx context.Context, opts ImportOptions, creds ConnectOptions) (*VMRef, error) {
	if opts.VMDKPath == "" {
		return nil, fmt.Errorf("vmdk path is required")
	}
	ovfDir := filepath.Join(filepath.Dir(opts.VMDKPath), "..", "ovf")
	ovfDir, err := filepath.Abs(ovfDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(ovfDir, 0o755); err != nil {
		return nil, err
	}

	vmdkName := filepath.Base(opts.VMDKPath)
	destVMDK := filepath.Join(ovfDir, vmdkName)
	if err = copyFile(opts.VMDKPath, destVMDK); err != nil {
		return nil, err
	}

	descriptor, err := toeholdovf.Descriptor(toeholdovf.DescriptorOptions{
		VMDKPath:  destVMDK,
		Name:      opts.Name,
		Network:   opts.Network,
		CPUs:      opts.CPUs,
		MemoryMiB: opts.MemoryMiB,
	})
	if err != nil {
		return nil, err
	}
	ovfPath := filepath.Join(ovfDir, opts.Name+".ovf")
	if err = os.WriteFile(ovfPath, []byte(descriptor), 0o644); err != nil {
		return nil, err
	}

	env := govcEnv(creds, opts)
	specPath := filepath.Join(ovfDir, "import.json")
	if err = runGovc(ctx, env, "import.spec", ovfPath); err != nil {
		return nil, fmt.Errorf("govc import.spec: %w", err)
	}
	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("read import spec: %w", err)
	}
	if err = patchImportSpec(specPath, opts.Name, opts.Network); err != nil {
		return nil, err
	}
	_ = specBytes

	folder := opts.FolderPath
	if folder == "" {
		folder = "/Datacenter/vm"
	}
	vmPath := strings.TrimSuffix(folder, "/") + "/" + opts.Name
	_ = runGovc(ctx, env, "vm.power", "-off", vmPath)
	_ = runGovc(ctx, env, "vm.destroy", vmPath)

	log.Printf("toehold-import: govc import.ovf name=%s ds=%s folder=%s", opts.Name, opts.Datastore, folder)
	if err = runGovc(ctx, env,
		"import.ovf",
		"-ds", opts.Datastore,
		"-folder", folder,
		"-name", opts.Name,
		"-options", specPath,
		ovfPath,
	); err != nil {
		return nil, fmt.Errorf("govc import.ovf: %w", err)
	}

	_ = runGovc(ctx, env, "device.boot", "-vm", vmPath, "-firmware", "efi")
	_ = runGovc(ctx, env, "vm.power", "-off", vmPath)
	if err = runGovc(ctx, env, "vm.markastemplate", vmPath); err != nil {
		return nil, fmt.Errorf("govc vm.markastemplate: %w", err)
	}
	return c.FindTemplate(ctx, opts.FolderPath, opts.Name)
}

func govcEnv(creds ConnectOptions, opts ImportOptions) []string {
	dc := "/Datacenter"
	if parts := strings.Split(strings.Trim(opts.FolderPath, "/"), "/"); len(parts) > 0 && parts[0] != "" {
		dc = "/" + parts[0]
	}
	env := []string{
		"GOVC_URL=" + creds.URL,
		"GOVC_USERNAME=" + creds.Username,
		"GOVC_PASSWORD=" + creds.Password,
		"GOVC_INSECURE=1",
		"GOVC_DATACENTER=" + dc,
	}
	if creds.Insecure {
		env = append(env, "GOVC_INSECURE=1")
	}
	return env
}

func runGovc(ctx context.Context, env []string, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("govc args required")
	}
	if args[0] == "import.spec" {
		// govc import.spec writes JSON to stdout; capture to file.
		ovfPath := args[1]
		specPath := filepath.Join(filepath.Dir(ovfPath), "import.json")
		cmd := exec.CommandContext(ctx, govcBin, "import.spec", ovfPath)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(ee.Stderr)))
			}
			return err
		}
		return os.WriteFile(specPath, out, 0o644)
	}

	cmd := exec.CommandContext(ctx, govcBin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func patchImportSpec(path, name, network string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var spec map[string]any
	if err = json.Unmarshal(data, &spec); err != nil {
		return err
	}
	spec["Name"] = name
	spec["DiskProvisioning"] = "thin"
	spec["MarkAsTemplate"] = false
	spec["PowerOn"] = false
	spec["InjectOvfEnv"] = false
	spec["WaitForIP"] = false
	if mappings, ok := spec["NetworkMapping"].([]any); ok {
		for _, m := range mappings {
			if mm, ok := m.(map[string]any); ok {
				// Name is the OVF network; Network is the vCenter portgroup.
				if mm["Name"] == nil || mm["Name"] == "" {
					mm["Name"] = network
				}
				mm["Network"] = network
			}
		}
	}
	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
