package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// CustomizeDomainExec executes `virt-customize` to customize the image.
//
// Arguments:
//   - extraArgs (...string): The additional arguments which will be appended to the `virt-customize` arguments.
//
// Returns:
//   - error: An error if something goes wrong during the process, or nil if successful.
func CustomizeDomainExec(extraArgs ...string) error {
	args := []string{"--verbose", "--format", "raw"}
	args = append(args, extraArgs...)

	customizeCmd := exec.Command("virt-customize", args...)
	customizeCmd.Stdout = os.Stdout
	customizeCmd.Stderr = os.Stderr

	fmt.Println("exec:", customizeCmd)
	if err := customizeCmd.Run(); err != nil {
		return fmt.Errorf("error executing virt-customize command: %w", err)
	}
	return nil
}

// getScriptArgs generates a list of arguments.
//
// Arguments:
//   - argName (string): Argument name which should be used for all the values
//   - values (...string): The list of values which should be joined with argument names.
//
// Returns:
//   - []string: List of arguments
//
// Example:
//   - getScriptArgs("firstboot", boot1, boot2) => ["--firstboot", boot1, "--firstboot", boot2]
func getScriptArgs(argName string, values ...string) []string {
	var args []string
	for _, val := range values {
		args = append(args, fmt.Sprintf("--%s", argName), val)
	}
	return args
}

// getScriptsWithSuffix retrieves all scripts with suffix from the specified directory
func getScriptsWithSuffix(directory string, suffix string) ([]string, error) {
	files, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("failed to read scripts directory: %w", err)
	}

	var scripts []string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), suffix) {
			scriptPath := filepath.Join(directory, file.Name())
			scripts = append(scripts, scriptPath)
		}
	}

	return scripts, nil
}

// addDisksToCustomize appends disk arguments to extraArgs
func addDisksToCustomize(extraArgs *[]string, disks []string) {
	*extraArgs = append(*extraArgs, getScriptArgs("add", disks...)...)
}

// getScriptsWithRegex retrieves all scripts with suffix from the specified directory
func getScriptsWithRegex(directory string, regex string) ([]string, error) {
	files, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("failed to read scripts directory: %w", err)
	}

	r := regexp.MustCompile(regex)
	var scripts []string
	for _, file := range files {
		if !file.IsDir() && r.MatchString(file.Name()) {
			scriptPath := filepath.Join(directory, file.Name())
			scripts = append(scripts, scriptPath)
		}
	}
	return scripts, nil
}
