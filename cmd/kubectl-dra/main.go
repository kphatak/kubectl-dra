// Command kubectl-dra is a kubectl plugin for inspecting Dynamic Resource
// Allocation. Install it on PATH and run it as "kubectl dra".
package main

import (
	"context"
	"os"
	"os/signal"

	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/kphatak/kubectl-dra/pkg/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := cmd.NewCmdDRA(genericiooptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})
	if err := root.ExecuteContext(ctx); err != nil {
		stop()
		os.Exit(1)
	}
}
