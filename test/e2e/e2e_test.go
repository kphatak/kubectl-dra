//go:build e2e

// Package e2e runs the kubectl-dra binary against the kind cluster that
// run.sh creates. It only builds with -tags e2e because it needs that
// cluster; use "make e2e".
package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

// Cluster layout created by run.sh.
const (
	gpuDriver      = "gpu.example.com"
	gpusPerWorker  = 8 // dra-example-driver's default numDevices
	taintRuleName  = "e2e-taint-gpu-7"
	allocatedGPUs  = 3 // one-gpu holds 1, two-gpus holds 2
	e2eNamespace   = "dra-e2e"
	incompletePool = "fake.example.com/incomplete"
)

// kubectlDRA runs the binary under test and returns stdout.
func kubectlDRA(t *testing.T, args ...string) []byte {
	t.Helper()
	bin := os.Getenv("KUBECTL_DRA")
	if bin == "" {
		t.Fatal("KUBECTL_DRA is not set; run the e2e tests through test/e2e/run.sh")
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("kubectl dra %s: %v\nstderr:\n%s", strings.Join(args, " "), err, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Logf("kubectl dra %s stderr:\n%s", strings.Join(args, " "), stderr.String())
	}
	return stdout.Bytes()
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decoding %s: %v", data, err)
	}
	return v
}

// Subsets of the -o json schemas in pkg/cmd.
type pool struct {
	Driver             string `json:"driver"`
	Pool               string `json:"pool"`
	Slices             int    `json:"slices"`
	ResourceSliceCount int64  `json:"resourceSliceCount"`
	Devices            int    `json:"devices"`
	Allocated          int    `json:"allocated"`
	Status             string `json:"status"`
}

type device struct {
	Driver string `json:"driver"`
	Pool   string `json:"pool"`
	Device string `json:"device"`
	Status string `json:"status"`
	Claims []struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"claims"`
	Taints []struct {
		Rule string `json:"rule"`
	} `json:"taints"`
}

type mapRow struct {
	Consumer string `json:"consumer"`
	Reserved bool   `json:"reserved"`
	Claim    string `json:"claim"`
	Device   string `json:"device"`
	Node     string `json:"node"`
	Status   string `json:"status"`
}

// cluster reads the cluster state through "dump", which also exercises the
// loader and the List round trip.
func cluster(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dump.yaml")
	if err := os.WriteFile(path, kubectlDRA(t, "dump", "-A", "-o", "yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := snapshot.LoadFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func workers(s *snapshot.Snapshot) []string {
	var names []string
	for _, n := range s.Nodes {
		if _, cp := n.Labels["node-role.kubernetes.io/control-plane"]; !cp {
			names = append(names, n.Name)
		}
	}
	return names
}

func taintRuleServed(s *snapshot.Snapshot) bool {
	for _, r := range s.DeviceTaintRules {
		if r.Name == taintRuleName {
			return true
		}
	}
	return false
}

func TestDump(t *testing.T) {
	s := cluster(t)
	if len(s.Warnings) > 0 {
		t.Errorf("dump did not round-trip cleanly: %q", s.Warnings)
	}
	if got := len(workers(s)); got != 2 {
		t.Errorf("workers: got %d, want 2", got)
	}
	found := false
	for _, c := range s.DeviceClasses {
		found = found || c.Name == gpuDriver
	}
	if !found {
		t.Errorf("DeviceClass %s missing; is dra-example-driver installed?", gpuDriver)
	}
	var e2eClaims int
	for _, c := range s.ResourceClaims {
		if c.Namespace == e2eNamespace {
			e2eClaims++
		}
	}
	if e2eClaims != 5 {
		t.Errorf("claims in %s: got %d, want 5 (one generated from a template)", e2eNamespace, e2eClaims)
	}
}

func TestPools(t *testing.T) {
	s := cluster(t)
	pools := decode[struct{ Pools []pool }](t, kubectlDRA(t, "pools", "-o", "json")).Pools

	var gpuPools, allocated int
	var sawIncomplete bool
	for _, p := range pools {
		switch {
		case p.Driver == gpuDriver:
			gpuPools++
			allocated += p.Allocated
			if p.Status != "Complete" || p.Devices != gpusPerWorker {
				t.Errorf("pool %s/%s: got status %s with %d devices, want Complete with %d", p.Driver, p.Pool, p.Status, p.Devices, gpusPerWorker)
			}
		case p.Driver+"/"+p.Pool == incompletePool:
			sawIncomplete = true
			if p.Status != "Incomplete" || p.Slices != 1 || p.ResourceSliceCount != 2 {
				t.Errorf("pool %s: got %+v, want Incomplete with 1 of 2 slices", incompletePool, p)
			}
		}
	}
	if want := len(workers(s)); gpuPools != want {
		t.Errorf("%s pools: got %d, want one per worker (%d)", gpuDriver, gpuPools, want)
	}
	if allocated != allocatedGPUs {
		t.Errorf("allocated GPUs across pools: got %d, want %d", allocated, allocatedGPUs)
	}
	if !sawIncomplete {
		t.Errorf("pool %s not listed", incompletePool)
	}

	table := string(kubectlDRA(t, "pools"))
	if !strings.HasPrefix(table, "DRIVER") || !strings.Contains(table, "Incomplete") {
		t.Errorf("unexpected table output:\n%s", table)
	}
}

func TestNodes(t *testing.T) {
	s := cluster(t)
	type nodeRow struct {
		Node       string   `json:"node"`
		Drivers    []string `json:"drivers"`
		Devices    int      `json:"devices"`
		Allocated  int      `json:"allocated"`
		Reachable  int      `json:"reachable"`
		PluginPods []string `json:"pluginPods"`
		Issues     []string `json:"issues"`
	}
	rows := decode[struct{ Nodes []nodeRow }](t, kubectlDRA(t, "nodes", "-o", "json")).Nodes

	isWorker := make(map[string]bool)
	for _, w := range workers(s) {
		isWorker[w] = true
	}
	if len(rows) != len(s.Nodes) {
		t.Errorf("rows: got %d, want one per node (%d)", len(rows), len(s.Nodes))
	}
	allocated := 0
	for _, r := range rows {
		// A healthy cluster has no issues. The hand-made incomplete pool
		// is not local to any node, so it does not show here.
		if len(r.Issues) > 0 {
			t.Errorf("%s: unexpected issues %q", r.Node, r.Issues)
		}
		// The two fake.example.com devices are allNodes, so reachable
		// from every node.
		if r.Reachable != 2 {
			t.Errorf("%s: reachable devices: got %d, want 2", r.Node, r.Reachable)
		}
		if !isWorker[r.Node] {
			if len(r.Drivers) != 0 || r.Devices != 0 {
				t.Errorf("control plane %s: got drivers %v and %d devices, want none", r.Node, r.Drivers, r.Devices)
			}
			continue
		}
		allocated += r.Allocated
		if len(r.Drivers) != 1 || r.Drivers[0] != gpuDriver || r.Devices != gpusPerWorker {
			t.Errorf("worker %s: got drivers %v with %d devices, want [%s] with %d", r.Node, r.Drivers, r.Devices, gpuDriver, gpusPerWorker)
		}
		var driverPod bool
		for _, p := range r.PluginPods {
			driverPod = driverPod || strings.HasPrefix(p, "dra-example-driver/")
		}
		if !driverPod {
			t.Errorf("worker %s: plugin pods %v do not include the dra-example-driver kubelet plugin", r.Node, r.PluginPods)
		}
	}
	if allocated != allocatedGPUs {
		t.Errorf("allocated GPUs across workers: got %d, want %d", allocated, allocatedGPUs)
	}
}

func TestMap(t *testing.T) {
	s := cluster(t)
	podNode := make(map[string]string)
	for _, p := range s.Pods {
		if p.Namespace == e2eNamespace {
			podNode["pod/"+p.Name] = p.Spec.NodeName
		}
	}

	rows := decode[struct{ Rows []mapRow }](t, kubectlDRA(t, "map", "-n", e2eNamespace, "-o", "json")).Rows
	byConsumer := make(map[string][]mapRow)
	for _, r := range rows {
		byConsumer[r.Consumer] = append(byConsumer[r.Consumer], r)
	}

	want := map[string]struct {
		rows   int
		status string
	}{
		"pod/one-gpu":       {rows: 1, status: "Allocated"},
		"pod/two-gpus":      {rows: 2, status: "Allocated"},
		"pod/missing-class": {rows: 1, status: "Pending"},
		"pod/no-match":      {rows: 1, status: "Pending"},
		"":                  {rows: 1, status: "Pending"}, // claim unused
	}
	if len(rows) != 6 {
		t.Errorf("rows: got %d, want 6: %+v", len(rows), rows)
	}
	for consumer, w := range want {
		got := byConsumer[consumer]
		if len(got) != w.rows {
			t.Errorf("%s: got %d rows, want %d", consumer, len(got), w.rows)
			continue
		}
		for _, r := range got {
			if r.Status != w.status {
				t.Errorf("%s: got status %s, want %s", consumer, r.Status, w.status)
			}
			if w.status != "Allocated" {
				continue
			}
			if !r.Reserved || r.Device == "" || r.Node != podNode[consumer] {
				t.Errorf("%s: got %+v, want reserved, a device, and node %q", consumer, r, podNode[consumer])
			}
		}
	}
	if got := byConsumer["pod/one-gpu"]; len(got) == 1 && !strings.HasPrefix(got[0].Claim, "one-gpu-gpu-") {
		t.Errorf("pod/one-gpu: claim %q should be generated from its template", got[0].Claim)
	}
}

func TestWhy(t *testing.T) {
	type explanation struct {
		Claim     string `json:"claim"`
		Verdict   string `json:"verdict"`
		Summary   string `json:"summary"`
		Allocator *struct {
			NodesTried int      `json:"nodesTried"`
			Nodes      []string `json:"nodes"`
			Error      string   `json:"error"`
		} `json:"allocator"`
	}
	why := func(target string) explanation {
		t.Helper()
		got := decode[struct{ Explanations []explanation }](t, kubectlDRA(t, "why", target, "-n", e2eNamespace, "-o", "json")).Explanations
		if len(got) != 1 {
			t.Fatalf("why %s: got %d explanations, want 1", target, len(got))
		}
		return got[0]
	}

	tests := []struct {
		target, verdict, summary string
	}{
		{"pod/one-gpu", "ALLOCATED", "1 device(s) allocated"},
		{"two-gpus", "ALLOCATED", "2 device(s) allocated"},
		{"missing-class", "NOT ALLOCATABLE", `DeviceClass "does-not-exist" does not exist`},
		{"pod/no-match", "NOT ALLOCATABLE", "0 match the request selectors"},
		// The allocator runs against live cluster state here.
		{"unused", "ALLOCATABLE", "the allocator finds devices on"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			e := why(tt.target)
			if e.Verdict != tt.verdict || !strings.Contains(e.Summary, tt.summary) {
				t.Errorf("got %s: %s\nwant %s: ...%s...", e.Verdict, e.Summary, tt.verdict, tt.summary)
			}
		})
	}

	t.Run("allocator agrees for the pending pod", func(t *testing.T) {
		// The control plane's NoSchedule taint excludes it, so only the
		// workers are candidates, and none can allocate.
		e := why("pod/no-match")
		if e.Allocator == nil || e.Allocator.NodesTried != 2 || len(e.Allocator.Nodes) != 0 || e.Allocator.Error != "" {
			t.Errorf("allocator: %+v", e.Allocator)
		}
	})
}

func TestDevices(t *testing.T) {
	s := cluster(t)
	nodes := workers(s)
	tainted := 0
	if taintRuleServed(s) {
		tainted = len(nodes) // the rule taints gpu-7 in every pool
	} else {
		t.Log("DeviceTaintRules not served; skipping taint checks")
	}
	totalGPUs := gpusPerWorker * len(nodes)

	gpus := decode[struct{ Devices []device }](t, kubectlDRA(t, "devices", "--driver", gpuDriver, "-o", "json")).Devices
	if len(gpus) != totalGPUs {
		t.Fatalf("GPUs: got %d, want %d", len(gpus), totalGPUs)
	}

	var holders []string
	var sawTainted int
	for _, d := range gpus {
		switch d.Status {
		case "Allocated":
			for _, c := range d.Claims {
				if c.Namespace != e2eNamespace {
					t.Errorf("%s/%s held by unexpected claim %s/%s", d.Pool, d.Device, c.Namespace, c.Name)
				}
				holders = append(holders, c.Name)
			}
		case "Tainted":
			sawTainted++
			if d.Device != "gpu-7" || len(d.Taints) != 1 || d.Taints[0].Rule != taintRuleName {
				t.Errorf("%s/%s: unexpected taint state %+v", d.Pool, d.Device, d.Taints)
			}
		case "Free":
		default:
			t.Errorf("%s/%s: unexpected status %s", d.Pool, d.Device, d.Status)
		}
	}
	if len(holders) != allocatedGPUs {
		t.Errorf("allocated GPUs: got %d (%v), want %d", len(holders), holders, allocatedGPUs)
	}
	var twoGPUs, generated int
	for _, h := range holders {
		switch {
		case h == "two-gpus":
			twoGPUs++
		case strings.HasPrefix(h, "one-gpu-gpu-"):
			generated++
		}
	}
	if twoGPUs != 2 || generated != 1 {
		t.Errorf("holders: got %v, want two-gpus twice and one claim generated for pod one-gpu", holders)
	}
	if sawTainted != tainted {
		t.Errorf("tainted GPUs: got %d, want %d", sawTainted, tainted)
	}

	t.Run("free", func(t *testing.T) {
		free := decode[struct{ Devices []device }](t, kubectlDRA(t, "devices", "--driver", gpuDriver, "--free", "-o", "json")).Devices
		if want := totalGPUs - allocatedGPUs - tainted; len(free) != want {
			t.Errorf("free GPUs: got %d, want %d", len(free), want)
		}
	})

	t.Run("class", func(t *testing.T) {
		// The class selects by driver, so the fake.example.com devices
		// must drop out.
		matched := decode[struct{ Devices []device }](t, kubectlDRA(t, "devices", "--class", gpuDriver, "-o", "json")).Devices
		if len(matched) != totalGPUs {
			t.Errorf("devices matching class %s: got %d, want %d", gpuDriver, len(matched), totalGPUs)
		}
	})

	t.Run("node", func(t *testing.T) {
		// One worker's GPUs plus the two allNodes fake devices.
		onNode := decode[struct{ Devices []device }](t, kubectlDRA(t, "devices", "--node", nodes[0], "-o", "json")).Devices
		if want := gpusPerWorker + 2; len(onNode) != want {
			t.Errorf("devices usable on %s: got %d, want %d", nodes[0], len(onNode), want)
		}
	})
}
