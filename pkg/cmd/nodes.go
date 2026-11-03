package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kphatak/kubectl-dra/pkg/analyze"
	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

type nodesFlags struct {
	issues bool
}

// nodeList is the -o json|yaml schema.
type nodeList struct {
	Nodes []nodeRow `json:"nodes"`
}

type nodeRow struct {
	Node        string   `json:"node"`
	Exists      bool     `json:"exists"`
	Drivers     []string `json:"drivers,omitempty"`
	Devices     int      `json:"devices"`
	Allocated   int      `json:"allocated"`
	Free        int      `json:"free"`
	Tainted     int      `json:"tainted"`
	Unavailable int      `json:"unavailable"`
	Reachable   int      `json:"reachable"`
	PluginPods  []string `json:"pluginPods,omitempty"`
	Issues      []string `json:"issues,omitempty"`
}

func newCmdNodes(o *Options) *cobra.Command {
	var f nodesFlags
	cmd := &cobra.Command{
		Use:   "nodes",
		Short: "Show DRA devices per node and nodes that look misconfigured",
		Long: `Show, for every node, which DRA drivers publish devices for it, how many
of those devices are allocated or free, and likely problems.

DEVICES counts devices local to the node. Network-attached devices that
the node can reach are counted in REACHABLE (-o wide).

ISSUES flags:

  - a driver's kubelet plugin runs on the node, but the driver publishes
    no ResourceSlice for it
  - a kubelet plugin pod on the node is not Ready
  - the node is NotReady, so its ResourceSlices may be stale
  - a pool local to the node is incomplete or invalid
  - ResourceSlices name a node that does not exist

Kubelet plugin pods are pods mounting the kubelet's plugins_registry
directory. A plugin is matched to its driver by a mounted
plugins/<driver> directory, or else inferred from the nodes it runs on;
issues say "seems to be" when inferred. Drivers that publish no slices
anywhere cannot be matched. This command lists pods in all namespaces.`,
		Example: `  # All nodes
  kubectl dra nodes

  # Only nodes with problems, with plugin pods and device counts
  kubectl dra nodes --issues -o wide`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := o.loadSnapshot(cmd.Context(), snapshot.LoadOptions{})
			if err != nil {
				return err
			}
			return runNodes(o, s, f)
		},
	}
	cmd.Flags().BoolVar(&f.issues, "issues", false, "Only show nodes with issues.")
	return cmd
}

func runNodes(o *Options, s *snapshot.Snapshot, f nodesFlags) error {
	pools := analyze.Pools(s.ResourceSlices)
	devices := analyze.Devices(pools, analyze.BuildAllocations(s.ResourceClaims, true), s.DeviceTaintRules)
	summaries, err := analyze.Nodes(s.Nodes, s.Pods, pools, devices)
	if err != nil {
		return err
	}

	list := nodeList{Nodes: []nodeRow{}}
	for _, n := range summaries {
		if f.issues && len(n.Issues) == 0 {
			continue
		}
		row := nodeRow{
			Node:        n.Name,
			Exists:      n.Node != nil,
			Drivers:     n.Drivers,
			Devices:     n.Devices,
			Allocated:   n.Allocated,
			Free:        n.Free,
			Tainted:     n.Tainted,
			Unavailable: n.Unavailable,
			Reachable:   n.Reachable,
			Issues:      n.Issues,
		}
		for _, p := range n.PluginPods {
			row.PluginPods = append(row.PluginPods, p.Namespace+"/"+p.Name)
		}
		list.Nodes = append(list.Nodes, row)
	}

	switch o.output {
	case outputJSON, outputYAML:
		return printStructured(o.Out, o.output, list)
	}
	if len(list.Nodes) == 0 {
		if f.issues {
			fmt.Fprintln(o.ErrOut, "No nodes with issues.")
		} else {
			fmt.Fprintln(o.ErrOut, "No nodes found.")
		}
		return nil
	}

	wide := o.output == outputWide
	tw := newTable(o.Out)
	w := tableOut(tw, s.Nodes, wide)
	fmt.Fprint(w, "NODE\tDRIVERS\tDEVICES\tALLOCATED\tFREE")
	if wide {
		fmt.Fprint(w, "\tTAINTED\tUNAVAILABLE\tREACHABLE\tPLUGIN PODS")
	}
	fmt.Fprintln(w, "\tISSUES")
	for _, r := range list.Nodes {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d", r.Node, orNone(strings.Join(r.Drivers, ",")), r.Devices, r.Allocated, r.Free)
		if wide {
			fmt.Fprintf(w, "\t%d\t%d\t%d\t%s", r.Tainted, r.Unavailable, r.Reachable, orNone(strings.Join(r.PluginPods, ",")))
		}
		fmt.Fprintf(w, "\t%s\n", orNone(strings.Join(r.Issues, "; ")))
	}
	return flushTable(o, tw, w)
}
