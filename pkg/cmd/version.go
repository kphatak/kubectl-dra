package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is set at build time with
// -ldflags "-X github.com/kphatak/kubectl-dra/pkg/cmd.Version=v0.1.0".
var Version = "dev"

func newCmdVersion(o *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the kubectl-dra version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintln(o.Out, Version)
			return err
		},
	}
}
