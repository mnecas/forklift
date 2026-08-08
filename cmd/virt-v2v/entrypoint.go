package main

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/kubev2v/forklift/pkg/virt-v2v/config"
	"github.com/kubev2v/forklift/pkg/virt-v2v/conversion"
	"github.com/kubev2v/forklift/pkg/virt-v2v/server"
)

func main() {
	env := &config.AppConfig{}
	err := env.Load()
	if err != nil {
		fmt.Println("Failed to load variables", err)
		os.Exit(1)
	}
	if err = linkCertificates(env); err != nil {
		fmt.Println("Failed to link the certificates", err)
		os.Exit(1)
	}
	if err = createV2vOutputDir(env); err != nil {
		fmt.Println("Failed to create v2v output dir", err)
		os.Exit(1)
	}
	convert, err := conversion.NewConversion(env)
	if err != nil {
		fmt.Println("Failed prepare conversion", err)
		os.Exit(1)
	}

	// Check if remote inspection of VMs should run
	if env.IsRemoteInspection {
		_, err = convert.InspectSource()
		if err != nil {
			fmt.Println("Failed to inspect the source VM", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	// Inspect the source guest before conversion so we can choose the
	// correct OS-specific customization arguments for virt-v2v.

	if convert.IsInPlace && convert.LibvirtUrl != "" {
		err = func() error {
			domainXML, err := convert.GetDomainXML()
			if err != nil {
				return fmt.Errorf("failed to get domain XML: %v", err)
			}
			if err := os.WriteFile(convert.LibvirtDomainFile, []byte(domainXML), 0644); err != nil {
				return fmt.Errorf("failed to write domain XML file: %v", err)
			}
			return nil
		}()
		if err != nil {
			fmt.Println("Failed to prepare libvirt domain XML", err)
			os.Exit(1)
		}
	}

	inspection, err := convert.InspectSource()
	if err != nil {
		fmt.Println("Failed to inspect the source VM", err)
		os.Exit(1)
	}

	// virt-v2v or virt-v2v-in-place
	if convert.IsInPlace {
		// Choose in-place conversion method based on available configuration:
		// - If LibvirtUrl is set: fetch domain XML from libvirt and use -i libvirtxml mode
		// - Otherwise: use -i disk mode directly on the mounted disks (e.g., EC2)
		if convert.LibvirtUrl != "" {
			if convert.OverlayEnabled {
				err = convert.RunInPlaceWithOverlay(func() error {
					return convert.RunVirtV2vInPlaceWithCustomization(inspection.OS)
				})
			} else {
				err = convert.RunVirtV2vInPlaceWithCustomization(inspection.OS)
			}
		} else {
			if convert.OverlayEnabled {
				err = convert.RunInPlaceWithOverlay(func() error {
					return convert.RunVirtV2vInPlaceDiskWithCustomization(inspection.OS)
				})
			} else {
				err = convert.RunVirtV2vInPlaceDiskWithCustomization(inspection.OS)
			}
		}
	} else {
		err = convert.RunVirtV2vWithCustomization(inspection.OS)
	}
	if err != nil {
		fmt.Println("Failed to execute virt-v2v command", err)
		os.Exit(1)
	}
	// In the remote migrations we can not connect to the conversion pod from the controller.
	// This connection is needed for to get the additional configuration which is gathered during inspection and
	// conversion. We expose those parameters via server in this pod and once the controller gets the config
	// the controller sends the request to terminate the pod.
	if convert.IsLocalMigration {
		s := server.Server{
			AppConfig: env,
		}
		err = s.Start()
		if err != nil {
			fmt.Println("failed to run the server", err)
			os.Exit(1)
		}
	}
}

// VirtV2VPrepEnvironment used in the cold migration.
// It creates a links between the downloaded guest image from virt-v2v and mounted PVC.
func linkCertificates(env *config.AppConfig) (err error) {
	if env.IsVsphereMigration() {
		if _, err := os.Stat("/etc/secret/cacert"); err == nil {
			// use the specified certificate
			err = os.Symlink("/etc/secret/cacert", "/opt/ca-bundle.crt")
			if err != nil {
				fmt.Println("Error creating ca cert link ", err)
				os.Exit(1)
			}
		} else {
			// otherwise, keep system pool certificates
			err := os.Symlink("/etc/pki/tls/certs/ca-bundle.crt.bak", "/opt/ca-bundle.crt")
			if err != nil {
				fmt.Println("Error creating ca cert link ", err)
				os.Exit(1)
			}
		}
	}
	return nil
}

func createV2vOutputDir(env *config.AppConfig) (err error) {
	if err = os.MkdirAll(env.Workdir, os.ModePerm); err != nil {
		return fmt.Errorf("error creating directory: %v", err)
	}
	return nil
}
