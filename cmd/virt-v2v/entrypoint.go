package main

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/kubev2v/forklift/pkg/virt-v2v/config"
	"github.com/kubev2v/forklift/pkg/virt-v2v/conversion"
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
		err = convert.RunRemoteV2vInspection()
		if err != nil {
			fmt.Println("Failed to execute virt-v2v-inspector command", err)
			os.Exit(1)
		}
	} else {
		// virt-v2v or virt-v2v-in-place
		if convert.IsInPlace {
			err = convert.RunVirtV2vInPlace()
		} else {
			err = convert.RunVirtV2v()
		}
		if err != nil {
			fmt.Println("Failed to execute virt-v2v command", err)
			os.Exit(1)
		}

		// virt-v2v-inspector
		err = convert.RunVirtV2VInspection()
		if err != nil {
			fmt.Println("Failed to inspect the disk", err)
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
