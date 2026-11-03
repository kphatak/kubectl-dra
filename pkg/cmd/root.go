// Package cmd implements the kubectl-dra command line.
package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes"

	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

// Output formats accepted by -o.
const (
	outputTable = "table"
	outputWide  = "wide"
	outputJSON  = "json"
	outputYAML  = "yaml"
)

var outputFormats = []string{outputTable, outputWide, outputJSON, outputYAML}

// Options holds the flags shared by every subcommand.
type Options struct {
	configFlags   *genericclioptions.ConfigFlags
	allNamespaces bool
	output        string

	genericiooptions.IOStreams
}

// NewCmdDRA returns the root command.
func NewCmdDRA(streams genericiooptions.IOStreams) *cobra.Command {
	o := &Options{
		configFlags: genericclioptions.NewConfigFlags(true),
		IOStreams:   streams,
	}

	cmd := &cobra.Command{
		Use:   "kubectl-dra",
		Short: "Inspect and debug Kubernetes Dynamic Resource Allocation",
		Long: `kubectl dra inspects Dynamic Resource Allocation (DRA) state: device
classes, resource slices published by drivers, and resource claims.

It is read-only and runs entirely from your kubeconfig.`,
		Annotations: map[string]string{
			cobra.CommandDisplayNameAnnotation: "kubectl dra",
		},
		SilenceUsage: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return o.validate()
		},
	}
	cmd.SetIn(streams.In)
	cmd.SetOut(streams.Out)
	cmd.SetErr(streams.ErrOut)

	flags := cmd.PersistentFlags()
	o.configFlags.AddFlags(flags)
	flags.BoolVarP(&o.allNamespaces, "all-namespaces", "A", false, "Use all namespaces instead of the current one.")
	flags.StringVarP(&o.output, "output", "o", outputTable, "Output format: "+strings.Join(outputFormats, "|")+".")

	cmd.AddCommand(
		newCmdDevices(o),
		newCmdMap(o),
		newCmdPools(o),
		newCmdVersion(o),
		newCmdDump(o),
	)
	return cmd
}

func (o *Options) validate() error {
	if !slices.Contains(outputFormats, o.output) {
		return fmt.Errorf("unknown output format %q; use one of %s", o.output, strings.Join(outputFormats, ", "))
	}
	return nil
}

// namespace returns the namespace to scope pods to: empty for -A, otherwise
// -n or the kubeconfig context's namespace.
func (o *Options) namespace() (string, error) {
	if o.allNamespaces {
		return metav1.NamespaceAll, nil
	}
	ns, _, err := o.configFlags.ToRawKubeConfigLoader().Namespace()
	return ns, err
}

// loadSnapshot reads cluster state for a command and prints load warnings to
// stderr, so partial data is never silent.
func (o *Options) loadSnapshot(ctx context.Context, opts snapshot.LoadOptions) (*snapshot.Snapshot, error) {
	cfg, err := o.configFlags.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	s, err := snapshot.Load(ctx, cs, opts)
	if err != nil {
		return nil, err
	}
	for _, w := range s.Warnings {
		fmt.Fprintf(o.ErrOut, "Warning: %s\n", w)
	}
	return s, nil
}
