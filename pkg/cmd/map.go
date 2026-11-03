package cmd

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/resourceclaim"

	"github.com/kphatak/kubectl-dra/pkg/analyze"
	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

// Map row status values.
const (
	mapAllocated  = "Allocated"
	mapPending    = "Pending"
	mapMissing    = "Missing"
	mapNotCreated = "NotCreated"
)

// mapList is the -o json|yaml schema.
type mapList struct {
	Rows []mapRow `json:"rows"`
}

type mapRow struct {
	Namespace   string `json:"namespace"`
	Consumer    string `json:"consumer,omitempty"`
	Reserved    bool   `json:"reserved,omitempty"`
	Claim       string `json:"claim,omitempty"`
	Template    string `json:"template,omitempty"`
	Request     string `json:"request,omitempty"`
	SubRequest  string `json:"subRequest,omitempty"`
	Driver      string `json:"driver,omitempty"`
	Pool        string `json:"pool,omitempty"`
	Device      string `json:"device,omitempty"`
	AdminAccess bool   `json:"adminAccess,omitempty"`
	Node        string `json:"node,omitempty"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
}

func newCmdMap(o *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "map",
		Short: "Show which pod uses which claim and which device it got",
		Long: `Map consumers to claims to devices. Each row is one consumer of a claim
(usually a pod) and one device allocated to it, or one request still
waiting for a device.

STATUS shows:

  Allocated    the device is allocated to the claim
  Pending      the claim has no allocation yet; see "kubectl dra why"
  Missing      the pod references a ResourceClaim that does not exist
  NotCreated   the pod's claim template has not produced a claim yet

NODE is the pod's node, or for claims without a scheduled pod, where the
allocation is usable. Claims reserved for a PodGroup show it as consumer.`,
		Example: `  # Claims and devices in the current namespace
  kubectl dra map

  # Everything, with reservations and reasons
  kubectl dra map -A -o wide`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ns, err := o.namespace()
			if err != nil {
				return err
			}
			s, err := o.loadSnapshot(cmd.Context(), snapshot.LoadOptions{Namespace: ns})
			if err != nil {
				return err
			}
			return runMap(o, s, ns)
		},
	}
}

// runMap prints the map for namespace, or all namespaces when it is empty.
func runMap(o *Options, s *snapshot.Snapshot, namespace string) error {
	inScope := func(ns string) bool { return namespace == "" || ns == namespace }
	var pods []*corev1.Pod
	for _, p := range s.Pods {
		if inScope(p.Namespace) {
			pods = append(pods, p)
		}
	}
	var claims []*resourceapi.ResourceClaim
	for _, c := range s.ResourceClaims {
		if inScope(c.Namespace) {
			claims = append(claims, c)
		}
	}

	m := analyze.MapClaims(pods, claims)
	list := mapList{Rows: []mapRow{}}
	for _, use := range m.Claims {
		rows, err := claimRows(use, s.Nodes)
		if err != nil {
			return err
		}
		list.Rows = append(list.Rows, rows...)
	}
	for _, mc := range m.Missing {
		row := mapRow{
			Namespace: mc.Pod.Namespace,
			Consumer:  "pod/" + mc.Pod.Name,
			Claim:     mc.ClaimName,
			Template:  mc.Template,
			Node:      mc.Pod.Spec.NodeName,
			Status:    mapMissing,
			Reason:    mc.Reason,
		}
		if mc.ClaimName == "" {
			row.Status = mapNotCreated
		}
		list.Rows = append(list.Rows, row)
	}
	sortMapRows(list.Rows)

	switch o.output {
	case outputJSON, outputYAML:
		return printStructured(o.Out, o.output, list)
	}
	if len(list.Rows) == 0 {
		if namespace == "" {
			fmt.Fprintln(o.ErrOut, "No resource claims found.")
		} else {
			fmt.Fprintf(o.ErrOut, "No resource claims found in %s namespace.\n", namespace)
		}
		return nil
	}

	wide := o.output == outputWide
	w := newTable(o.Out)
	if namespace == "" {
		fmt.Fprint(w, "NAMESPACE\t")
	}
	fmt.Fprint(w, "CONSUMER\tCLAIM\tREQUEST\tDRIVER\tPOOL\tDEVICE\tNODE\tSTATUS")
	if wide {
		fmt.Fprint(w, "\tRESERVED\tREASON")
	}
	fmt.Fprintln(w)
	for _, r := range list.Rows {
		if namespace == "" {
			fmt.Fprintf(w, "%s\t", r.Namespace)
		}
		request := r.Request
		if r.SubRequest != "" {
			request = resourceclaim.CreateSubRequestRef(r.Request, r.SubRequest)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s",
			orNone(r.Consumer), orNone(r.Claim), orNone(request), orNone(r.Driver), orNone(r.Pool), orNone(r.Device), orNone(r.Node), r.Status)
		if wide {
			fmt.Fprintf(w, "\t%v\t%s", r.Reserved, orNone(r.Reason))
		}
		fmt.Fprintln(w)
	}
	return w.Flush()
}

// claimRows returns one row per consumer and allocated device, or per
// consumer and request while the claim is unallocated. A claim without
// consumers still gets rows, with an empty consumer.
func claimRows(use *analyze.ClaimUse, nodes []*corev1.Node) ([]mapRow, error) {
	claim := use.Claim
	consumers := use.Consumers
	if len(consumers) == 0 {
		consumers = []analyze.Consumer{{}}
	}

	var rows []mapRow
	for i := range consumers {
		c := &consumers[i]
		base := mapRow{Namespace: claim.Namespace, Claim: claim.Name, Reserved: c.Reserved}
		if c.Name != "" {
			base.Consumer = c.String()
		}

		alloc := claim.Status.Allocation
		if alloc == nil {
			for _, req := range claim.Spec.Devices.Requests {
				row := base
				row.Request = req.Name
				row.Status = mapPending
				if c.Pod != nil {
					row.Node = c.Pod.Spec.NodeName
				}
				rows = append(rows, row)
			}
			continue
		}

		node, err := allocationNode(c, alloc, nodes)
		if err != nil {
			return nil, fmt.Errorf("claim %s/%s: %w", claim.Namespace, claim.Name, err)
		}
		for _, res := range alloc.Devices.Results {
			row := base
			row.Request = resourceclaim.BaseRequestRef(res.Request)
			if resourceclaim.IsSubRequestRef(res.Request) {
				row.SubRequest = res.Request[len(row.Request)+1:]
			}
			row.Driver, row.Pool, row.Device = res.Driver, res.Pool, res.Device
			row.AdminAccess = res.AdminAccess != nil && *res.AdminAccess
			row.Node = node
			row.Status = mapAllocated
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// allocationNode is the consumer pod's node if it has one, otherwise where
// the allocation is usable: a single node name, "all nodes", or the
// allocation's node selector.
func allocationNode(c *analyze.Consumer, alloc *resourceapi.AllocationResult, nodes []*corev1.Node) (string, error) {
	if c.Pod != nil && c.Pod.Spec.NodeName != "" {
		return c.Pod.Spec.NodeName, nil
	}
	scope := analyze.NodeScope{NodeSelector: alloc.NodeSelector, AllNodes: alloc.NodeSelector == nil}
	names, err := scope.Nodes(nodes)
	if err != nil {
		return "", err
	}
	if len(names) == 1 && !scope.AllNodes {
		return names[0], nil
	}
	return scope.String(), nil
}

// sortMapRows groups rows by namespace and consumer, with claims nothing
// uses at the end of each namespace. Rows of one claim keep their order.
func sortMapRows(rows []mapRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if (a.Consumer == "") != (b.Consumer == "") {
			return a.Consumer != ""
		}
		if a.Consumer != b.Consumer {
			return a.Consumer < b.Consumer
		}
		return a.Claim < b.Claim
	})
}
