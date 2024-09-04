package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed scripts
var scriptFS embed.FS

// CustomizeWindows customizes a windows disk image by uploading scripts.
//
// The function writes two bash scripts to the specified local tmp directory,
// uploads them to the disk image using `virt-customize`.
//
// Arguments:
//   - disks ([]string): The list of disk paths which should be customized
//
// Returns:
//   - error: An error if something goes wrong during the process, or nil if successful.
func CustomizeWindows(execFunc DomainExecFunc, disks []string, dir string, t FileSystemTool) error {
	fmt.Printf("Customizing disks '%s'", disks)
	err := t.CreateFilesFromFS(dir)
	if err != nil {
		return fmt.Errorf("failed to create files from filesystem: %w", err)
	}

	var extraArgs []string

	if _, err = os.Stat(DYNAMIC_SCRIPTS_MOUNT_PATH); !os.IsNotExist(err) {
		err = addWinDynamicScripts(&extraArgs, DYNAMIC_SCRIPTS_MOUNT_PATH)
		if err != nil {
			return err
		}
	}

	addWinFirstbootScripts(&extraArgs, dir)

	addDisksToCustomize(&extraArgs, disks)

	fmt.Println("THIS IS OUTPUT FROM THE CustomizeWindows")
	fmt.Println(extraArgs)

	err = execFunc(extraArgs...)
	if err != nil {
		return err
	}
	return nil
}

// addRhelFirstbootScripts appends firstboot script arguments to extraArgs
func addWinFirstbootScripts(extraArgs *[]string, dir string) {
	windowsScriptsPath := filepath.Join(dir, "scripts", "windows")
	initPath := filepath.Join(windowsScriptsPath, "9999-run-mtv-ps-scripts.bat")
	restoreScriptPath := filepath.Join(windowsScriptsPath, "9999-restore_config.ps1")
	firstbootPath := filepath.Join(windowsScriptsPath, "firstboot.bat")

	// Upload scripts to the windows
	uploadScriptPath := formatUpload(restoreScriptPath, WIN_FIRSTBOOT_SCRIPTS_PATH)
	uploadInitPath := formatUpload(initPath, WIN_FIRSTBOOT_SCRIPTS_PATH)
	uploadFirstbootPath := formatUpload(firstbootPath, WIN_FIRSTBOOT_PATH)

	*extraArgs = append(*extraArgs,
		getScriptArgs("upload",
			uploadScriptPath,
			uploadInitPath,
			uploadFirstbootPath)...,
	)
}

func formatUpload(src string, dst string) string {
	return fmt.Sprintf("%s:%s", src, dst)
}

func addWinDynamicScripts(extraArgs *[]string, dir string) error {
	dynamicScripts, err := getScriptsWithRegex(dir, WINDOWS_DYNAMIC_REGEX)
	if err != nil {
		return err
	}
	for _, script := range dynamicScripts {
		*extraArgs = append(*extraArgs, getScriptArgs("upload", formatUpload(script, WIN_FIRSTBOOT_SCRIPTS_PATH))...)
	}
	return nil
}
