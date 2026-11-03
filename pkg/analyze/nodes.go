package analyze

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// pluginRegistryDir is the directory where kubelet plugins (DRA drivers, but
// also CSI node plugins) place their registration sockets. Only its base name
// is compared, so kubelets with a non-default root directory still match.
const pluginRegistryDir = "plugins_registry"

// NodeSummary is DRA state from the point of view of one node.
type NodeSummary struct {
	Name string

	// Node is nil when ResourceSlices name a node that does not exist.
	Node *corev1.Node

	// Drivers publish ResourceSlices for this node.
	Drivers []string

	// Device counts cover devices local to this node.
	Devices     int
	Allocated   int // Allocated or Shared
	Free        int
	Tainted     int
	Unavailable int

	// Reachable counts devices that are not local to any node, such as
	// network-attached ones, which this node can use.
	Reachable int

	// PluginPods are kubelet plugin pods (DRA or CSI) running on the node.
	PluginPods []*corev1.Pod

	Issues []string
}

// Nodes summarizes DRA state per node and flags likely problems:
//
//   - a DRA driver's kubelet plugin runs on the node but the driver
//     publishes no ResourceSlice for it (see driverPlugins for how plugins
//     are matched to drivers)
//   - a kubelet plugin pod on the node is not Ready
//   - the node is NotReady while it has ResourceSlices, which may be stale
//   - a pool local to the node is incomplete or invalid
//   - ResourceSlices name a node that does not exist
func Nodes(nodes []*corev1.Node, pods []*corev1.Pod, pools []*Pool, devices []*DeviceInfo) ([]*NodeSummary, error) {
	byName := make(map[string]*NodeSummary)
	var summaries []*NodeSummary
	summary := func(name string) *NodeSummary {
		if s, ok := byName[name]; ok {
			return s
		}
		s := &NodeSummary{Name: name}
		byName[name] = s
		summaries = append(summaries, s)
		return s
	}
	for _, n := range nodes {
		summary(n.Name).Node = n
	}

	// Which drivers publish for which node, from node-local slices and
	// devices with their own node name.
	publishes := make(map[string]sets.Set[string]) // driver -> node names
	addPublisher := func(driver, node string) {
		if publishes[driver] == nil {
			publishes[driver] = sets.New[string]()
		}
		publishes[driver].Insert(node)
	}
	for _, p := range pools {
		for _, s := range p.Slices {
			if s.Spec.NodeName != nil && *s.Spec.NodeName != "" {
				addPublisher(p.ID.Driver, *s.Spec.NodeName)
			}
		}
	}

	for _, d := range devices {
		if d.Scope.NodeName != "" {
			addPublisher(d.Slice.Spec.Driver, d.Scope.NodeName)
			s := summary(d.Scope.NodeName)
			s.Devices++
			switch d.Status {
			case DeviceAllocated, DeviceShared:
				s.Allocated++
			case DeviceFree:
				s.Free++
			case DeviceTainted:
				s.Tainted++
			case DeviceUnavailable:
				s.Unavailable++
			}
			continue
		}
		if !d.Scope.Known() {
			continue
		}
		for _, n := range nodes {
			ok, err := d.Scope.Matches(n)
			if err != nil {
				return nil, fmt.Errorf("device %s: %w", d.ID, err)
			}
			if ok {
				byName[n.Name].Reachable++
			}
		}
	}
	for driver, nodeNames := range publishes {
		for name := range nodeNames {
			s := summary(name)
			s.Drivers = append(s.Drivers, driver)
		}
	}

	draDrivers := sets.New[string]()
	for _, p := range pools {
		draDrivers.Insert(p.ID.Driver)
	}
	plugins := pluginGroups(pods)
	for _, g := range plugins {
		for _, p := range g.pods {
			s := summary(p.Spec.NodeName)
			s.PluginPods = append(s.PluginPods, p)
			if !podReady(p) {
				s.Issues = append(s.Issues, fmt.Sprintf("kubelet plugin pod %s/%s is not Ready", p.Namespace, p.Name))
			}
		}
	}
	for driver, nodeNames := range publishes {
		for _, issue := range driverPlugins(driver, nodeNames, draDrivers, plugins, byName) {
			s := byName[issue.node]
			s.Issues = append(s.Issues, issue.text)
		}
	}

	for _, p := range pools {
		node := poolNode(p)
		if node == "" || p.Usable() {
			continue
		}
		status := "Invalid"
		if len(p.Problems) == 0 {
			status = fmt.Sprintf("Incomplete (%d of %d slices)", len(p.Slices), p.ResourceSliceCount)
		}
		s := summary(node)
		s.Issues = append(s.Issues, fmt.Sprintf("pool %s is %s", p.ID, status))
	}

	for _, s := range summaries {
		slices.Sort(s.Drivers)
		switch {
		case s.Node == nil:
			s.Issues = append(s.Issues, "node does not exist; its ResourceSlices are left over")
		case len(s.Drivers) > 0 && nodeNotReady(s.Node):
			s.Issues = append(s.Issues, "node is NotReady; its ResourceSlices may be stale")
		}
		sort.SliceStable(s.PluginPods, func(i, j int) bool {
			return s.PluginPods[i].Namespace+"/"+s.PluginPods[i].Name < s.PluginPods[j].Namespace+"/"+s.PluginPods[j].Name
		})
	}

	// Existing nodes first, then names only known from slices.
	sort.Slice(summaries, func(i, j int) bool {
		a, b := summaries[i], summaries[j]
		if (a.Node == nil) != (b.Node == nil) {
			return a.Node != nil
		}
		return a.Name < b.Name
	})
	return summaries, nil
}

// pluginGroup is the set of kubelet plugin pods sharing a controller,
// usually one DaemonSet.
type pluginGroup struct {
	name  string // "DaemonSet ns/name", or "Pod ns/name" without controller
	pods  []*corev1.Pod
	nodes sets.Set[string]

	// serves holds the names X of every "<kubelet>/plugins/X" directory the
	// pods mount. A kubelet plugin keeps its socket in a directory named
	// after its driver, so these name the drivers the group serves. Pods
	// that mount the whole plugins directory (as dra-example-driver does)
	// leave it empty.
	serves sets.Set[string]
}

// pluginGroups finds running kubelet plugin pods: pods on a node with a
// hostPath volume for the kubelet's plugin registration directory. That
// includes CSI node plugins, which the caller tells apart through serves.
func pluginGroups(pods []*corev1.Pod) []*pluginGroup {
	byName := make(map[string]*pluginGroup)
	var groups []*pluginGroup
	for _, p := range pods {
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		registry, serves := false, sets.New[string]()
		for _, v := range p.Spec.Volumes {
			if v.HostPath == nil {
				continue
			}
			hostPath := path.Clean(v.HostPath.Path)
			switch {
			case path.Base(hostPath) == pluginRegistryDir:
				registry = true
			case path.Base(path.Dir(hostPath)) == "plugins":
				serves.Insert(path.Base(hostPath))
			}
		}
		if !registry {
			continue
		}

		name := "Pod " + p.Namespace + "/" + p.Name
		if ref := metav1.GetControllerOfNoCopy(p); ref != nil {
			name = ref.Kind + " " + p.Namespace + "/" + ref.Name
		}
		g, ok := byName[name]
		if !ok {
			g = &pluginGroup{name: name, nodes: sets.New[string](), serves: sets.New[string]()}
			byName[name] = g
			groups = append(groups, g)
		}
		g.pods = append(g.pods, p)
		g.nodes.Insert(p.Spec.NodeName)
		g.serves = g.serves.Union(serves)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].name < groups[j].name })
	return groups
}

type nodeIssue struct {
	node string
	text string
}

// driverPlugins reports nodes where driver's kubelet plugin runs but the
// driver publishes nothing.
//
// A plugin group that mounts "<kubelet>/plugins/<driver>" is that driver's
// plugin. Without one, the API does not say which plugin serves which
// driver, so this infers it. Candidates are groups that mount no directory
// of another driver (which rules out standard CSI node plugins) and run on
// every existing node the driver publishes for; the candidates on the fewest
// nodes win. A node is reported only if every winner runs there, so a tie
// never produces a false alarm. Drivers that publish nothing at all are not
// covered, since there is nothing to match against.
func driverPlugins(driver string, publishedFor, draDrivers sets.Set[string], groups []*pluginGroup, summaries map[string]*NodeSummary) []nodeIssue {
	existing := sets.New[string]()
	for name := range publishedFor {
		if s := summaries[name]; s != nil && s.Node != nil {
			existing.Insert(name)
		}
	}

	var best []*pluginGroup
	inferred := true
	for _, g := range groups {
		if g.serves.Has(driver) {
			best = append(best, g)
			inferred = false
		}
	}
	if inferred && existing.Len() > 0 {
		for _, g := range groups {
			if g.serves.Difference(draDrivers).Len() > 0 || !g.nodes.IsSuperset(existing) {
				continue
			}
			switch {
			case len(best) == 0 || g.nodes.Len() < best[0].nodes.Len():
				best = []*pluginGroup{g}
			case g.nodes.Len() == best[0].nodes.Len():
				best = append(best, g)
			}
		}
	}
	if len(best) == 0 {
		return nil
	}

	common := best[0].nodes.Clone()
	for _, g := range best[1:] {
		common = common.Intersection(g.nodes)
	}
	var issues []nodeIssue
	for _, node := range sets.List(common.Difference(publishedFor)) {
		text := fmt.Sprintf("kubelet plugins run here, but no %s ResourceSlice names this node", driver)
		if len(best) == 1 {
			names := make([]string, 0, len(best[0].pods))
			for _, p := range best[0].pods {
				if p.Spec.NodeName == node {
					names = append(names, p.Namespace+"/"+p.Name)
				}
			}
			verb := "is"
			if inferred {
				verb = "seems to be"
			}
			text = fmt.Sprintf("%s (%s) %s the kubelet plugin for %s, but no %s ResourceSlice names this node",
				best[0].name, strings.Join(names, ","), verb, driver, driver)
		}
		issues = append(issues, nodeIssue{node: node, text: text})
	}
	return issues
}

// poolNode returns the node all of a pool's slices are local to, or "".
func poolNode(p *Pool) string {
	node := ""
	for _, s := range p.Slices {
		if s.Spec.NodeName == nil || *s.Spec.NodeName == "" || (node != "" && *s.Spec.NodeName != node) {
			return ""
		}
		node = *s.Spec.NodeName
	}
	return node
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// nodeNotReady reports a Ready condition that is False or Unknown. A node
// without the condition is not reported, since nothing is known about it.
func nodeNotReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status != corev1.ConditionTrue
		}
	}
	return false
}
