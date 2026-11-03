package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/printers"

	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

// newCmdDump is a hidden debugging aid: it loads the snapshot every command
// works from and either summarizes it or writes it out as a List that
// snapshot.LoadFiles can read back, for bug reports and test fixtures.
func newCmdDump(o *Options) *cobra.Command {
	var events bool
	cmd := &cobra.Command{
		Use:    "dump",
		Short:  "Summarize, or with -o yaml|json write out, the cluster state kubectl-dra reads",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ns, err := o.namespace()
			if err != nil {
				return err
			}
			s, err := o.loadSnapshot(cmd.Context(), snapshot.LoadOptions{Namespace: ns, Events: events})
			if err != nil {
				return err
			}

			switch o.output {
			case outputJSON, outputYAML:
				list, err := s.ToList()
				if err != nil {
					return err
				}
				var p printers.ResourcePrinter = &printers.YAMLPrinter{}
				if o.output == outputJSON {
					p = &printers.JSONPrinter{}
				}
				return p.PrintObj(list, o.Out)
			default:
				return printSummary(o, s)
			}
		},
	}
	cmd.Flags().BoolVar(&events, "events", false, "Also include events.")
	return cmd
}

func printSummary(o *Options, s *snapshot.Snapshot) error {
	w := newTable(o.Out)
	fmt.Fprintf(w, "SOURCE\t%s\n", s.Source)
	rows := []struct {
		kind  string
		count int
	}{
		{"Nodes", len(s.Nodes)},
		{"Pods", len(s.Pods)},
		{"Events", len(s.Events)},
		{"DeviceClasses", len(s.DeviceClasses)},
		{"ResourceSlices", len(s.ResourceSlices)},
		{"ResourceClaims", len(s.ResourceClaims)},
		{"ResourceClaimTemplates", len(s.ResourceClaimTemplates)},
		{"DeviceTaintRules", len(s.DeviceTaintRules)},
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%d\n", r.kind, r.count)
	}
	return w.Flush()
}
