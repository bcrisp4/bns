package main

import (
	"github.com/bcrisp4/bns/internal/buildinfo"
	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "bns",
		Short:         "BNS — Ben's Name Server",
		Long:          "A caching DNS forwarder with ad-blocking.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Custom version output so the test (and humans) get a stable shape.
	cmd.Version = buildinfo.Version
	cmd.SetVersionTemplate("bns version {{.Version}}\n")

	cmd.AddCommand(newServeCmd())
	return cmd
}
