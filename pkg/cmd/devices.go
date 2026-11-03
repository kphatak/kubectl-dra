package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kphatak/kubectl-dra/pkg/analyze"
	"github.com/kphatak/kubectl-dra/pkg/celx"
	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

type devicesFlags struct {
	node   string
	driver string
	class  string
	free   bool
}

// deviceList is the -o json|yaml schema.
type deviceList struct {
	Devices []deviceRow `json:"devices"`
}

type deviceRow struct {
	Driver     string                                                    `json:"driver"`
	Pool       string                                                    `json:"pool"`
	Device     string                                                    `json:"device"`
	NodeScope  string                                                    `json:"nodeScope"`
	Status     analyze.DeviceStatus                                      `json:"status"`
	Reason     string                                                    `json:"reason,omitempty"`
	Claims     []deviceClaim                                             `json:"claims,omitempty"`
	Taints     []analyze.DeviceTaint                                     `json:"taints,omitempty"`
	Attributes map[resourceapi.QualifiedName]resourceapi.DeviceAttribute `json:"attributes,omitempty"`
	Capacity   map[resourceapi.QualifiedName]resourceapi.DeviceCapacity  `json:"capacity,omitempty"`
}

type deviceClaim struct {
	Namespace   string     `json:"namespace"`
	Name        string     `json:"name"`
	Request     string     `json:"request"`
	SubRequest  string     `json:"subRequest,omitempty"`
	AdminAccess bool       `json:"adminAccess,omitempty"`
	ShareID     *types.UID `json:"shareID,omitempty"`
}

func newCmdDevices(o *Options) *cobra.Command {
	var f devicesFlags
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "List every device published by DRA drivers and whether it is free",
		Long: `List every device in the newest generation of every pool, with the
claims using it.

STATUS shows:

  Free         can be allocated to any request that selects it
  Allocated    dedicated to a claim
  Shared       used by claims sharing it through consumable capacity
  Tainted      unallocated, but a NoSchedule or NoExecute taint blocks
               requests without a matching toleration
  Unavailable  cannot be allocated, for example because its pool is
               incomplete; see -o wide

Claims with admin access are listed but do not make a device busy.
Taints include those added by DeviceTaintRules.`,
		Example: `  # All devices
  kubectl dra devices

  # Free devices on one node, with taints and attributes
  kubectl dra devices --node gpu-node-1 --free -o wide

  # Devices a DeviceClass selects, evaluated with the scheduler's CEL library
  kubectl dra devices --class gpu.nvidia.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := o.loadSnapshot(cmd.Context(), snapshot.LoadOptions{SkipWorkloads: true})
			if err != nil {
				return err
			}
			return runDevices(cmd.Context(), o, s, f)
		},
	}
	cmd.Flags().StringVar(&f.node, "node", "", "Only show devices usable on this node.")
	cmd.Flags().StringVar(&f.driver, "driver", "", "Only show devices of this driver.")
	cmd.Flags().StringVar(&f.class, "class", "", "Only show devices that this DeviceClass's selectors match.")
	cmd.Flags().BoolVar(&f.free, "free", false, "Only show devices with status Free.")
	return cmd
}

func runDevices(ctx context.Context, o *Options, s *snapshot.Snapshot, f devicesFlags) error {
	var node *corev1.Node
	if f.node != "" {
		i := slices.IndexFunc(s.Nodes, func(n *corev1.Node) bool { return n.Name == f.node })
		if i < 0 {
			return fmt.Errorf("node %q not found", f.node)
		}
		node = s.Nodes[i]
	}
	var class *resourceapi.DeviceClass
	if f.class != "" {
		i := slices.IndexFunc(s.DeviceClasses, func(c *resourceapi.DeviceClass) bool { return c.Name == f.class })
		if i < 0 {
			return fmt.Errorf("DeviceClass %q not found", f.class)
		}
		class = s.DeviceClasses[i]
	}

	allocations := analyze.BuildAllocations(s.ResourceClaims, true)
	infos := analyze.Devices(analyze.Pools(s.ResourceSlices), allocations, s.DeviceTaintRules)
	cel := celx.NewEvaluator()

	list := deviceList{Devices: []deviceRow{}}
	for _, d := range infos {
		if f.driver != "" && d.Slice.Spec.Driver != f.driver {
			continue
		}
		if f.free && d.Status != analyze.DeviceFree {
			continue
		}
		if node != nil {
			ok, err := d.Scope.Matches(node)
			if err != nil {
				return fmt.Errorf("device %s: %w", d.ID, err)
			}
			if !ok {
				continue
			}
		}
		if class != nil {
			mismatch, err := cel.Match(ctx, d.Slice.Spec.Driver, d.Device, class.Spec.Selectors)
			if err != nil {
				return fmt.Errorf("DeviceClass %s on device %s: %w; the scheduler fails every claim using this class the same way", class.Name, d.ID, err)
			}
			if mismatch != nil {
				continue
			}
		}
		list.Devices = append(list.Devices, newDeviceRow(d))
	}

	switch o.output {
	case outputJSON, outputYAML:
		return printStructured(o.Out, o.output, list)
	}
	if len(list.Devices) == 0 {
		fmt.Fprintln(o.ErrOut, "No devices found.")
		return nil
	}

	wide := o.output == outputWide
	w := newTable(o.Out)
	fmt.Fprint(w, "DRIVER\tPOOL\tDEVICE\tNODES\tSTATUS\tCLAIMS")
	if wide {
		fmt.Fprint(w, "\tTAINTS\tREASON\tATTRIBUTES")
	}
	fmt.Fprintln(w)
	for _, r := range list.Devices {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s", r.Driver, r.Pool, r.Device, r.NodeScope, r.Status, formatClaims(r.Claims))
		if wide {
			fmt.Fprintf(w, "\t%s\t%s\t%s", formatTaints(r.Taints), orNone(r.Reason), formatAttributes(r.Attributes))
		}
		fmt.Fprintln(w)
	}
	return w.Flush()
}

func newDeviceRow(d *analyze.DeviceInfo) deviceRow {
	row := deviceRow{
		Driver:     d.Slice.Spec.Driver,
		Pool:       d.Slice.Spec.Pool.Name,
		Device:     d.Device.Name,
		NodeScope:  d.Scope.String(),
		Status:     d.Status,
		Reason:     d.Reason,
		Taints:     d.Taints,
		Attributes: d.Device.Attributes,
		Capacity:   d.Device.Capacity,
	}
	for _, h := range d.Holders {
		row.Claims = append(row.Claims, deviceClaim{
			Namespace:   h.Claim.Namespace,
			Name:        h.Claim.Name,
			Request:     h.Request,
			SubRequest:  h.SubRequest,
			AdminAccess: h.AdminAccess,
			ShareID:     h.ShareID,
		})
	}
	return row
}

func formatClaims(claims []deviceClaim) string {
	parts := make([]string, 0, len(claims))
	for _, c := range claims {
		s := c.Namespace + "/" + c.Name
		if c.AdminAccess {
			s += " (admin)"
		}
		parts = append(parts, s)
	}
	return orNone(strings.Join(parts, ","))
}

func formatTaints(taints []analyze.DeviceTaint) string {
	parts := make([]string, 0, len(taints))
	for _, t := range taints {
		parts = append(parts, t.String())
	}
	return orNone(strings.Join(parts, ","))
}

// formatAttributes renders attributes as sorted name=value pairs. List
// values print as [a b].
func formatAttributes(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute) string {
	names := make([]string, 0, len(attrs))
	for n := range attrs {
		names = append(names, string(n))
	}
	slices.Sort(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+"="+formatAttribute(attrs[resourceapi.QualifiedName(n)]))
	}
	return orNone(strings.Join(parts, ","))
}

func formatAttribute(a resourceapi.DeviceAttribute) string {
	switch {
	case a.IntValue != nil:
		return fmt.Sprint(*a.IntValue)
	case a.BoolValue != nil:
		return fmt.Sprint(*a.BoolValue)
	case a.StringValue != nil:
		return *a.StringValue
	case a.VersionValue != nil:
		return *a.VersionValue
	case a.IntValues != nil:
		return fmt.Sprint(a.IntValues)
	case a.BoolValues != nil:
		return fmt.Sprint(a.BoolValues)
	case a.StringValues != nil:
		return fmt.Sprint(a.StringValues)
	case a.VersionValues != nil:
		return fmt.Sprint(a.VersionValues)
	default:
		return "?"
	}
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}
