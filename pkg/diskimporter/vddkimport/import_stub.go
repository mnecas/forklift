//go:build !amd64

package vddkimport

import (
	"context"
	"fmt"
)

// Run performs a VDDK import using CDI's importer packages.
func Run(ctx context.Context, _ Params) error {
	return fmt.Errorf("vddk import is only supported on amd64")
}
