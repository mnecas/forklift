package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kubev2v/forklift/pkg/diskimporter"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := diskimporter.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "disk-importer: %v\n", err)
		os.Exit(1)
	}
}
