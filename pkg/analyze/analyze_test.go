package analyze

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"

	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

func load(t *testing.T, name string) *snapshot.Snapshot {
	t.Helper()
	s, err := snapshot.LoadFiles("../../test/fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPools(t *testing.T) {
	pools := Pools(load(t, "pools.yaml").ResourceSlices)

	byID := make(map[string]*Pool)
	var order []string
	for _, p := range pools {
		byID[p.ID.String()] = p
		order = append(order, p.ID.String())
	}
	wantOrder := []string{
		"gpu.example.com/duplicate",
		"gpu.example.com/mismatch",
		"gpu.example.com/node-a",
		"gpu.example.com/node-b",
		"link.example.com/rack-r1",
		"net.example.com/fabric",
		"storage.example.com/zone-a",
	}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("pool order:\n got %v\nwant %v", order, wantOrder)
	}

	tests := []struct {
		pool       string
		generation int64
		slices     int
		outdated   int
		devices    int
		complete   bool
		usable     bool
		problem    string
	}{
		{pool: "gpu.example.com/node-a", generation: 2, slices: 2, outdated: 1, devices: 4, complete: true, usable: true},
		{pool: "gpu.example.com/node-b", generation: 1, slices: 1, devices: 1},
		{pool: "gpu.example.com/duplicate", generation: 1, slices: 2, devices: 2, complete: true, problem: `device name "gpu-0" is published by both duplicate-part1 and duplicate-part2`},
		{pool: "gpu.example.com/mismatch", generation: 1, slices: 2, devices: 2, problem: "disagree on resourceSliceCount (2, 3)"},
		{pool: "net.example.com/fabric", generation: 1, slices: 1, devices: 1, complete: true, usable: true},
	}
	for _, tt := range tests {
		t.Run(tt.pool, func(t *testing.T) {
			p := byID[tt.pool]
			if p.Generation != tt.generation {
				t.Errorf("generation: got %d, want %d", p.Generation, tt.generation)
			}
			if len(p.Slices) != tt.slices {
				t.Errorf("slices: got %d, want %d", len(p.Slices), tt.slices)
			}
			if p.OutdatedSlices != tt.outdated {
				t.Errorf("outdated slices: got %d, want %d", p.OutdatedSlices, tt.outdated)
			}
			if got := len(p.Devices()); got != tt.devices {
				t.Errorf("devices: got %d, want %d", got, tt.devices)
			}
			if p.Complete() != tt.complete {
				t.Errorf("complete: got %v, want %v", p.Complete(), tt.complete)
			}
			if p.Usable() != tt.usable {
				t.Errorf("usable: got %v, want %v", p.Usable(), tt.usable)
			}
			if tt.problem == "" && len(p.Problems) > 0 {
				t.Errorf("unexpected problems: %q", p.Problems)
			}
			if tt.problem != "" && (len(p.Problems) != 1 || !strings.Contains(p.Problems[0], tt.problem)) {
				t.Errorf("problems: got %q, want one containing %q", p.Problems, tt.problem)
			}
		})
	}
}

func gpu(name string) structured.DeviceID {
	return structured.MakeDeviceID("gpu.example.com", "node-a", name)
}

func TestBuildAllocations(t *testing.T) {
	claims := load(t, "allocations.yaml").ResourceClaims

	t.Run("consumable capacity on", func(t *testing.T) {
		a := BuildAllocations(claims, true)

		wantDedicated := []string{gpu("gpu-0").String(), gpu("gpu-2").String(), gpu("gpu-3").String()}
		var gotDedicated []string
		for id := range a.State.AllocatedDevices {
			gotDedicated = append(gotDedicated, id.String())
		}
		slices.Sort(gotDedicated)
		if !slices.Equal(gotDedicated, wantDedicated) {
			t.Errorf("dedicated devices:\n got %v\nwant %v", gotDedicated, wantDedicated)
		}
		if n := a.State.AllocatedSharedDeviceIDs.Len(); n != 2 {
			t.Errorf("shared device IDs: got %d, want 2", n)
		}
		if q := a.State.AggregatedCapacity[gpu("gpu-1")]["memory"]; q == nil || q.String() != "30Gi" {
			t.Errorf("aggregated memory on gpu-1: got %v, want 30Gi", q)
		}

		for dev, want := range map[string]bool{"gpu-0": true, "gpu-1": true, "gpu-2": true, "gpu-3": true, "gpu-4": false, "gpu-9": false} {
			if got := a.InUse(gpu(dev)); got != want {
				t.Errorf("InUse(%s): got %v, want %v", dev, got, want)
			}
		}
	})

	t.Run("consumable capacity off", func(t *testing.T) {
		a := BuildAllocations(claims, false)
		if !a.State.AllocatedDevices.Has(gpu("gpu-1")) {
			t.Error("gpu-1 should count as dedicated when consumable capacity is off")
		}
		if n := a.State.AllocatedSharedDeviceIDs.Len(); n != 0 {
			t.Errorf("shared device IDs: got %d, want 0", n)
		}
	})

	t.Run("holders", func(t *testing.T) {
		a := BuildAllocations(claims, true)

		h := a.Holders(gpu("gpu-0"))
		if len(h) != 2 {
			t.Fatalf("gpu-0 holders: got %d, want 2", len(h))
		}
		if h[0].Claim.Name != "dedicated" || h[0].AdminAccess {
			t.Errorf("gpu-0 first holder: got %s admin=%v, want dedicated admin=false", h[0].Claim.Name, h[0].AdminAccess)
		}
		if h[1].Claim.Name != "monitor" || !h[1].AdminAccess {
			t.Errorf("gpu-0 second holder: got %s admin=%v, want monitor admin=true", h[1].Claim.Name, h[1].AdminAccess)
		}

		p := a.Holders(gpu("gpu-2"))
		if len(p) != 1 || p[0].Request != "gpu" || p[0].SubRequest != "large" {
			t.Errorf("gpu-2 holder: got %+v, want request gpu, subrequest large", p)
		}

		o := a.Holders(gpu("gpu-3"))
		if len(o) != 1 || o[0].Claim.Namespace != "team-b" {
			t.Errorf("gpu-3 holder: got %+v, want the claim in team-b", o)
		}

		if s := a.Holders(gpu("gpu-1")); len(s) != 2 || s[0].ShareID == nil {
			t.Errorf("gpu-1 holders: got %+v, want two shared holders", s)
		}
	})
}

func TestDevices(t *testing.T) {
	s := load(t, "devices.yaml")
	infos := Devices(Pools(s.ResourceSlices), BuildAllocations(s.ResourceClaims, true), s.DeviceTaintRules)

	type want struct {
		status  DeviceStatus
		taints  []string
		holders int
		reason  string
	}
	wants := map[string]want{
		"gpu.example.com/node-a/gpu-0": {status: DeviceAllocated, holders: 1},
		"gpu.example.com/node-a/gpu-1": {status: DeviceTainted, taints: []string{"example.com/maintenance:NoSchedule"}},
		"gpu.example.com/node-a/gpu-2": {status: DeviceTainted, taints: []string{"example.com/drain:NoExecute (drain-gpu-2)"}},
		"gpu.example.com/node-a/gpu-3": {status: DeviceFree, taints: []string{"example.com/ecc-warning=1:None"}, holders: 1},
		"gpu.example.com/node-b/gpu-0": {status: DeviceUnavailable, reason: "pool is incomplete (1 of 2 slices)"},
		"net.example.com/fabric/nic-0": {status: DeviceShared, holders: 2},
	}
	if len(infos) != len(wants) {
		t.Fatalf("got %d devices, want %d", len(infos), len(wants))
	}
	for _, d := range infos {
		w, ok := wants[d.ID.String()]
		if !ok {
			t.Errorf("unexpected device %s", d.ID)
			continue
		}
		var taints []string
		for _, tn := range d.Taints {
			taints = append(taints, tn.String())
		}
		if d.Status != w.status || d.Reason != w.reason || len(d.Holders) != w.holders || !slices.Equal(taints, w.taints) {
			t.Errorf("%s: got status=%s reason=%q holders=%d taints=%v, want status=%s reason=%q holders=%d taints=%v",
				d.ID, d.Status, d.Reason, len(d.Holders), taints, w.status, w.reason, w.holders, w.taints)
		}
	}
}

func TestMapClaims(t *testing.T) {
	s := load(t, "map.yaml")
	m := MapClaims(s.Pods, s.ResourceClaims)

	consumers := make(map[string][]string)
	for _, use := range m.Claims {
		var cs []string
		for _, c := range use.Consumers {
			str := c.String()
			if c.Reserved {
				str += " reserved"
			}
			if c.Pod != nil {
				str += " +pod"
			}
			cs = append(cs, str)
		}
		consumers[use.Claim.Namespace+"/"+use.Claim.Name] = cs
	}
	want := map[string][]string{
		"team-a/big":          {"pod/big +pod"},
		"team-a/orphan":       nil,
		"team-a/prio":         {"pod/prio reserved +pod"},
		"team-a/shared-nic":   {"pod/web-1 reserved +pod", "pod/web-2 reserved +pod"},
		"team-a/train-gpu-x1": {"pod/train reserved +pod"},
		"team-a/workload":     {"podgroups/train-group reserved"},
		"team-b/other":        {"pod/other reserved +pod"},
	}
	if len(consumers) != len(want) {
		t.Errorf("claims: got %d, want %d", len(consumers), len(want))
	}
	for claim, w := range want {
		if got := consumers[claim]; !slices.Equal(got, w) {
			t.Errorf("%s consumers: got %v, want %v", claim, got, w)
		}
	}

	var missing []string
	for _, mc := range m.Missing {
		missing = append(missing, mc.Pod.Name+": "+mc.ClaimName+": "+mc.Reason)
	}
	wantMissing := []string{
		"not-yet: : not created yet from ResourceClaimTemplate one-gpu",
		"typo: does-not-exist: ResourceClaim not found",
	}
	if !slices.Equal(missing, wantMissing) {
		t.Errorf("missing:\n got %q\nwant %q", missing, wantMissing)
	}
}

func TestMapClaimsOwnerAndStaleReservation(t *testing.T) {
	s := load(t, "map.yaml")

	// A generated claim not owned by the pod is not the pod's claim.
	for _, c := range s.ResourceClaims {
		if c.Name == "train-gpu-x1" {
			c.OwnerReferences = nil
		}
	}
	// A reservation for a pod that has since been recreated (new UID) must
	// not attach the new pod.
	for _, p := range s.Pods {
		if p.Name == "prio" {
			p.UID = "00000000-0000-0000-0000-00000000ffff"
		}
	}
	m := MapClaims(s.Pods, s.ResourceClaims)

	var sawTrain bool
	for _, mc := range m.Missing {
		if mc.Pod.Name == "train" && strings.Contains(mc.Reason, "Pod is not owner") {
			sawTrain = true
		}
	}
	if !sawTrain {
		t.Errorf("want train's claim reported as not owned by the pod, got %+v", m.Missing)
	}
	for _, use := range m.Claims {
		if use.Claim.Name != "prio" {
			continue
		}
		// The new pod references the claim but is not reserved; the old
		// reservation stays a separate entry without a pod.
		var current, stale int
		for _, c := range use.Consumers {
			switch {
			case c.Pod != nil && !c.Reserved:
				current++
			case c.Pod == nil && c.Reserved:
				stale++
			}
		}
		if len(use.Consumers) != 2 || current != 1 || stale != 1 {
			t.Errorf("prio consumers: got %+v, want the new pod unreserved and the stale reservation apart", use.Consumers)
		}
	}
}

func TestNodes(t *testing.T) {
	s := load(t, "nodes.yaml")
	pools := Pools(s.ResourceSlices)
	devices := Devices(pools, BuildAllocations(s.ResourceClaims, true), s.DeviceTaintRules)
	summaries, err := Nodes(s.Nodes, s.Pods, pools, devices)
	if err != nil {
		t.Fatal(err)
	}

	type want struct {
		exists                             bool
		devices, allocated, free, tainted  int
		unavailable, reachable, pluginPods int
		issues                             []string
	}
	wants := []struct {
		name string
		want
	}{
		{"cpu-1", want{exists: true, reachable: 2, pluginPods: 1}},
		{"gpu-1", want{exists: true, devices: 4, allocated: 1, free: 2, tainted: 1, reachable: 2, pluginPods: 2}},
		{"gpu-2", want{exists: true, reachable: 2, pluginPods: 2, issues: []string{
			"kubelet plugin pod dra-driver/gpu-plugin-b is not Ready",
			"DaemonSet dra-driver/gpu-plugin (dra-driver/gpu-plugin-b) seems to be the kubelet plugin for gpu.example.com, but no gpu.example.com ResourceSlice names this node",
		}}},
		{"gpu-3", want{exists: true, devices: 2, free: 2, reachable: 2, pluginPods: 2, issues: []string{
			"node is NotReady; its ResourceSlices may be stale",
		}}},
		{"gpu-4", want{exists: true, devices: 1, unavailable: 1, reachable: 2, pluginPods: 2, issues: []string{
			"pool gpu.example.com/gpu-4 is Incomplete (1 of 2 slices)",
		}}},
		{"gone-node", want{devices: 1, free: 1, issues: []string{
			"node does not exist; its ResourceSlices are left over",
		}}},
	}
	if len(summaries) != len(wants) {
		var names []string
		for _, s := range summaries {
			names = append(names, s.Name)
		}
		t.Fatalf("got nodes %v, want %d", names, len(wants))
	}
	for i, w := range wants {
		got := summaries[i]
		if got.Name != w.name {
			t.Errorf("node %d: got %s, want %s", i, got.Name, w.name)
			continue
		}
		g := want{
			exists: got.Node != nil, devices: got.Devices, allocated: got.Allocated, free: got.Free, tainted: got.Tainted,
			unavailable: got.Unavailable, reachable: got.Reachable, pluginPods: len(got.PluginPods), issues: got.Issues,
		}
		if !reflect.DeepEqual(g, w.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", w.name, g, w.want)
		}
	}
}

// nodeIssues runs Nodes and returns the issues per node name.
func nodeIssues(t *testing.T, s *snapshot.Snapshot) map[string][]string {
	t.Helper()
	pools := Pools(s.ResourceSlices)
	devices := Devices(pools, BuildAllocations(s.ResourceClaims, true), s.DeviceTaintRules)
	summaries, err := Nodes(s.Nodes, s.Pods, pools, devices)
	if err != nil {
		t.Fatal(err)
	}
	issues := make(map[string][]string)
	for _, sum := range summaries {
		issues[sum.Name] = sum.Issues
	}
	return issues
}

func TestNodesPluginMatching(t *testing.T) {
	t.Run("CSI plugin alone is never taken for a DRA driver", func(t *testing.T) {
		s := load(t, "nodes.yaml")
		s.Pods = slices.DeleteFunc(s.Pods, func(p *corev1.Pod) bool { return strings.HasPrefix(p.Name, "gpu-plugin-") })
		for node, issues := range nodeIssues(t, s) {
			for _, issue := range issues {
				if strings.Contains(issue, "kubelet plugin") {
					t.Errorf("%s: unexpected plugin issue %q", node, issue)
				}
			}
		}
	})

	t.Run("a mounted plugins/<driver> directory names the driver directly", func(t *testing.T) {
		s := load(t, "nodes.yaml")
		for _, p := range s.Pods {
			if strings.HasPrefix(p.Name, "gpu-plugin-") {
				p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{
					Name:         "data",
					VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kubelet/plugins/gpu.example.com"}},
				})
			}
		}
		want := "DaemonSet dra-driver/gpu-plugin (dra-driver/gpu-plugin-b) is the kubelet plugin for gpu.example.com, but no gpu.example.com ResourceSlice names this node"
		if got := nodeIssues(t, s)["gpu-2"]; !slices.Contains(got, want) {
			t.Errorf("gpu-2 issues: got %q, want one of them to be %q", got, want)
		}
	})

	t.Run("a tie between candidates only flags nodes both run on", func(t *testing.T) {
		s := load(t, "nodes.yaml")
		// A second plugin DaemonSet on gpu-1, gpu-3, gpu-4 and cpu-1 ties
		// with gpu-plugin (four nodes each). They share no node without
		// slices, so nothing may be flagged.
		for _, node := range []string{"gpu-1", "gpu-3", "gpu-4", "cpu-1"} {
			s.Pods = append(s.Pods, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "other-" + node, Namespace: "other",
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "other", UID: "o1", Controller: ptr.To(true)}},
				},
				Spec: corev1.PodSpec{NodeName: node, Volumes: []corev1.Volume{{
					Name:         "registry",
					VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/kubelet/plugins_registry"}},
				}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
			})
		}
		for node, issues := range nodeIssues(t, s) {
			for _, issue := range issues {
				if strings.Contains(issue, "ResourceSlice names this node") {
					t.Errorf("%s: unexpected issue %q", node, issue)
				}
			}
		}
	})
}

func TestNodeScope(t *testing.T) {
	s := load(t, "pools.yaml")

	tests := []struct {
		device string // driver/pool/device
		scope  string
		nodes  []string
	}{
		{device: "gpu.example.com/node-a/gpu-0", scope: "node-a", nodes: []string{"node-a"}},
		{device: "net.example.com/fabric/nic-0", scope: "all nodes", nodes: []string{"node-a", "node-b"}},
		{device: "storage.example.com/zone-a/vol-0", scope: "topology.kubernetes.io/zone In (a)", nodes: []string{"node-a"}},
		{device: "link.example.com/rack-r1/link-0", scope: "node-a", nodes: []string{"node-a"}},
		{device: "link.example.com/rack-r1/link-1", scope: "all nodes", nodes: []string{"node-a", "node-b"}},
		{device: "link.example.com/rack-r1/link-2", scope: "example.com/rack Exists", nodes: []string{"node-a"}},
	}

	refs := make(map[string]DeviceRef)
	for _, p := range Pools(s.ResourceSlices) {
		for _, d := range p.Devices() {
			refs[d.ID.String()] = d
		}
	}

	for _, tt := range tests {
		t.Run(tt.device, func(t *testing.T) {
			ref, ok := refs[tt.device]
			if !ok {
				t.Fatalf("device %s not found", tt.device)
			}
			scope := ScopeOf(ref.Slice, ref.Device)
			if !scope.Known() {
				t.Error("scope should be known")
			}
			if got := scope.String(); got != tt.scope {
				t.Errorf("String: got %q, want %q", got, tt.scope)
			}
			nodes, err := scope.Nodes(s.Nodes)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(nodes, tt.nodes) {
				t.Errorf("Nodes: got %v, want %v", nodes, tt.nodes)
			}
		})
	}

	if (NodeScope{}).Known() || (NodeScope{}).String() != "unknown" {
		t.Error("an empty scope should be unknown")
	}
}
