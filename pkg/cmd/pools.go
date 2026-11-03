package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"

	"github.com/kphatak/kubectl-dra/pkg/analyze"
	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

// Pool status values, from most to least healthy.
const (
	poolComplete   = "Complete"
	poolIncomplete = "Incomplete"
	poolInvalid    = "Invalid"
)

type poolsFlags struct {
	driver string
	node   string
}

// poolList is the -o json|yaml schema.
type poolList struct {
	Pools []poolRow `json:"pools"`
}

type poolRow struct {
	Driver             string `json:"driver"`
	Pool               string `json:"pool"`
	NodeScope          string `json:"nodeScope"`
	Generation         int64  `json:"generation"`
	Slices             int    `json:"slices"`
	ResourceSliceCount int64  `json:"resourceSliceCount"` // 0 when slices disagree

	OutdatedSlices int      `json:"outdatedSlices"`
	Devices        int      `json:"devices"`
	Allocated      int      `json:"allocated"`
	Status         string   `json:"status"`
	Problems       []string `json:"problems,omitempty"`
}

func newCmdPools(o *Options) *cobra.Command {
	var f poolsFlags
	cmd := &cobra.Command{
		Use:   "pools",
		Short: "List device pools and whether the scheduler can allocate from them",
		Long: `List every pool published by DRA drivers through ResourceSlices.

The scheduler only allocates from a pool once every ResourceSlice of the
pool's newest generation is published. STATUS shows:

  Complete     all slices are present; the pool is usable
  Incomplete   slices are missing, so no device in the pool can be allocated
  Invalid      the pool is unusable for another reason; see -o wide

SLICES is slices seen at the newest generation over resourceSliceCount.
ALLOCATED counts devices in use by at least one claim, not counting
admin access.`,
		Example: `  # List all pools
  kubectl dra pools

  # Pools that node gpu-node-1 can use, with problems and outdated slices
  kubectl dra pools --node gpu-node-1 -o wide

  # Incomplete or invalid pools, for scripts
  kubectl dra pools -o json | jq '.pools[] | select(.status != "Complete")'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := o.loadSnapshot(cmd.Context(), snapshot.LoadOptions{SkipWorkloads: true})
			if err != nil {
				return err
			}
			return runPools(o, s, f)
		},
	}
	cmd.Flags().StringVar(&f.driver, "driver", "", "Only show pools of this driver.")
	cmd.Flags().StringVar(&f.node, "node", "", "Only show pools with devices usable on this node.")
	return cmd
}

func runPools(o *Options, s *snapshot.Snapshot, f poolsFlags) error {
	var node *corev1.Node
	if f.node != "" {
		for _, n := range s.Nodes {
			if n.Name == f.node {
				node = n
				break
			}
		}
		if node == nil {
			return fmt.Errorf("node %q not found", f.node)
		}
	}

	// Whether the scheduler counts shared devices as shared or dedicated
	// does not change whether a device is in use, so the flag is moot here.
	allocations := analyze.BuildAllocations(s.ResourceClaims, true)

	list := poolList{Pools: []poolRow{}}
	for _, p := range analyze.Pools(s.ResourceSlices) {
		if f.driver != "" && p.ID.Driver != f.driver {
			continue
		}
		if node != nil {
			ok, err := p.ReachableFrom(node)
			if err != nil {
				return fmt.Errorf("pool %s: %w", p.ID, err)
			}
			if !ok {
				continue
			}
		}
		list.Pools = append(list.Pools, newPoolRow(p, allocations))
	}

	switch o.output {
	case outputJSON, outputYAML:
		return printStructured(o.Out, o.output, list)
	}
	if len(list.Pools) == 0 {
		fmt.Fprintln(o.ErrOut, "No pools found.")
		return nil
	}

	wide := o.output == outputWide
	tw := newTable(o.Out)
	w := tableOut(tw, s.Nodes, wide)
	fmt.Fprint(w, "DRIVER\tPOOL\tNODES\tGENERATION\tSLICES\tDEVICES\tALLOCATED\tSTATUS")
	if wide {
		fmt.Fprint(w, "\tOUTDATED SLICES\tPROBLEMS")
	}
	fmt.Fprintln(w)
	for _, r := range list.Pools {
		// Zero means the slices disagree on the count; the problem says how.
		expected := "?"
		if r.ResourceSliceCount > 0 {
			expected = fmt.Sprint(r.ResourceSliceCount)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d/%s\t%d\t%d\t%s",
			r.Driver, r.Pool, r.NodeScope, r.Generation, r.Slices, expected, r.Devices, r.Allocated, r.Status)
		if wide {
			problems := "<none>"
			if len(r.Problems) > 0 {
				problems = strings.Join(r.Problems, "; ")
			}
			fmt.Fprintf(w, "\t%d\t%s", r.OutdatedSlices, problems)
		}
		fmt.Fprintln(w)
	}
	return flushTable(o, tw, w)
}

func newPoolRow(p *analyze.Pool, allocations *analyze.Allocations) poolRow {
	devices := p.Devices()
	allocated := 0
	for _, d := range devices {
		if allocations.InUse(d.ID) {
			allocated++
		}
	}

	status := poolComplete
	switch {
	case len(p.Problems) > 0:
		status = poolInvalid
	case !p.Complete():
		status = poolIncomplete
	}

	return poolRow{
		Driver:             p.ID.Driver,
		Pool:               p.ID.Pool,
		NodeScope:          p.ScopeSummary(),
		Generation:         p.Generation,
		Slices:             len(p.Slices),
		ResourceSliceCount: p.ResourceSliceCount,
		OutdatedSlices:     p.OutdatedSlices,
		Devices:            len(devices),
		Allocated:          allocated,
		Status:             status,
		Problems:           p.Problems,
	}
}
