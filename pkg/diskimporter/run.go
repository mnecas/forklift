package diskimporter

import (
	"context"
	"fmt"
)

// Run executes a single checkpoint import using the configured transfer backend.
func Run(ctx context.Context) error {
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		return err
	}
	backend, err := NewBackend(cfg.TransferType)
	if err != nil {
		return err
	}
	if err := backend.Run(ctx, cfg); err != nil {
		return fmt.Errorf("%s import failed: %w", backend.Type(), err)
	}
	return nil
}
