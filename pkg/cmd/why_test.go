package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kphatak/kubectl-dra/pkg/analyze"
)

// TestWhyScenarios covers every scenario "why" must explain, using the
// verdict and summary from -o json.
func TestWhyScenarios(t *testing.T) {
	s := loadFixtures(t, "why.yaml")

	tests := []struct {
		target  string
		verdict analyze.Verdict
		summary string // substring
	}{
		{"unknown-class", analyze.VerdictNotAllocatable, `DeviceClass "missing" does not exist`},
		{"class-false", analyze.VerdictNotAllocatable, `request "gpu" needs 1 device: 8 published, 7 in usable pools, 0 match the class`},
		{"request-false", analyze.VerdictNotAllocatable, `7 match the class, 0 match the request selectors`},
		{"cel-error", analyze.VerdictError, "selector #0: CEL compile error"},
		{"all-busy", analyze.VerdictNotAllocatable, `5 match the request selectors, 0 not allocated to other claims`},
		{"match-conflict", analyze.VerdictNotAllocatable, "each request can be satisfied on its own, but the allocator rejected every candidate node"},
		{"first-available", analyze.VerdictAllocatable, "the allocator finds devices on 1 of 3 candidate nodes (node-b)"},
		{"all-l4", analyze.VerdictAllocatable, "the allocator finds devices on 1 of 3 candidate nodes (node-b), but fails with an error on 1 (node-c)"},
		{"too-many", analyze.VerdictNotAllocatable, `request "gpus" needs 3 devices on one node: 8 published, 7 in usable pools, 7 match the class, 2 match the request selectors, 2 not allocated to other claims, 2 taints tolerated, 2 reachable from candidate nodes, but the best node, node-b, reaches 2`},
		{"wrong-zone", analyze.VerdictNotAllocatable, "2 taints tolerated, 0 reachable from candidate nodes"},
		{"bound", analyze.VerdictAllocatedPodPending, "the claim's devices are usable only on node-a, and the pod cannot run there"},
		{"pod/templated", analyze.VerdictAllocatable, "the allocator finds devices on 1 of 2 candidate nodes (node-b)"},
		{"pod/not-yet", analyze.VerdictNotAllocatable, "not created yet from ResourceClaimTemplate l4"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			o, out, _ := testOptions(outputJSON)
			if err := runWhy(context.Background(), o, s, "why", tt.target); err != nil {
				t.Fatal(err)
			}
			var got whyList
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Explanations) != 1 {
				t.Fatalf("got %d explanations, want 1", len(got.Explanations))
			}
			e := got.Explanations[0]
			if e.Verdict != tt.verdict || !strings.Contains(e.Summary, tt.summary) {
				t.Errorf("got %s: %s\nwant %s: ...%s...", e.Verdict, e.Summary, tt.verdict, tt.summary)
			}
		})
	}
}

func TestWhyDetails(t *testing.T) {
	s := loadFixtures(t, "why.yaml")
	explain := func(target string) *analyze.Explanation {
		t.Helper()
		o, out, _ := testOptions(outputJSON)
		if err := runWhy(context.Background(), o, s, "why", target); err != nil {
			t.Fatal(err)
		}
		var got whyList
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Explanations[0]
	}

	t.Run("CEL false shows the attribute values", func(t *testing.T) {
		e := explain("request-false")
		stage := e.Requests[0].Alternatives[0].Stages[3]
		if stage.Name != analyze.StageSelectors || len(stage.Dropped) != 1 ||
			!strings.Contains(stage.Dropped[0].Examples[0], "gpu.example.com/memory=") {
			t.Errorf("selector stage: %+v", stage)
		}
	})

	t.Run("incomplete pool is warned about only when the class selects it", func(t *testing.T) {
		if w := explain("all-busy").PoolWarnings; len(w) != 1 || !strings.Contains(w[0], "gpu.example.com/node-c") {
			t.Errorf("all-busy pool warnings: %q", w)
		}
		// The L4 class selects all GPUs, so node-c's A100 counts for it too.
		if w := explain("request-false").PoolWarnings; len(w) != 1 {
			t.Errorf("request-false pool warnings: %q", w)
		}
		if w := explain("class-false").PoolWarnings; len(w) != 0 {
			t.Errorf("class-false selects nothing, so no pool warning: %q", w)
		}
	})

	t.Run("firstAvailable falls through to the second subrequest", func(t *testing.T) {
		alts := explain("first-available").Requests[0].Alternatives
		if len(alts) != 2 || alts[0].Satisfiable || !alts[1].Satisfiable || alts[1].Request != "gpu/small" {
			t.Errorf("alternatives: %+v", alts)
		}
	})

	t.Run("pod node constraints exclude nodes with reasons", func(t *testing.T) {
		// node-c fails the zone check before its taint is looked at.
		e := explain("wrong-zone")
		if len(e.CandidateNodes) != 1 || e.CandidateNodes[0] != "node-a" ||
			len(e.ExcludedNodes) != 1 || e.ExcludedNodes[0].Count != 2 {
			t.Errorf("candidates %v, excluded %+v", e.CandidateNodes, e.ExcludedNodes)
		}
	})

	t.Run("an error on one node does not hide success on another", func(t *testing.T) {
		// all-l4 asks for all matching devices. On node-c the pool is
		// incomplete, which is a fatal error for the allocator there.
		a := explain("all-l4").Allocator
		if len(a.Nodes) != 1 || a.Nodes[0] != "node-b" || len(a.AbortingNodes) != 1 || a.AbortingNodes[0] != "node-c" || a.Error != "" {
			t.Errorf("allocator: %+v", a)
		}
	})

	t.Run("pending pod shows the scheduler's message and events", func(t *testing.T) {
		e := explain("pod/pending-pod")
		if !strings.Contains(e.PodScheduled, "0/3 nodes are available") || len(e.Events) != 1 {
			t.Errorf("podScheduled %q, events %q", e.PodScheduled, e.Events)
		}
	})
}

func TestWhyText(t *testing.T) {
	s := loadFixtures(t, "why.yaml")
	for _, target := range []string{"request-false", "all-busy", "first-available", "pod/pending-pod", "pod/not-yet"} {
		t.Run(target, func(t *testing.T) {
			o, out, _ := testOptions(outputTable)
			if err := runWhy(context.Background(), o, s, "why", target); err != nil {
				t.Fatal(err)
			}
			assertGolden(t, "why-"+strings.ReplaceAll(target, "/", "-"), out.Bytes())
		})
	}
}

func TestWhyErrors(t *testing.T) {
	s := loadFixtures(t, "why.yaml")
	for target, want := range map[string]string{
		"nope":            `ResourceClaim "nope" not found in namespace "why"`,
		"pod/nope":        `pod "nope" not found in namespace "why"`,
		"deployment/x":    `cannot explain "deployment/x"`,
		"resourceclaim/x": `ResourceClaim "x" not found`,
	} {
		o, _, _ := testOptions(outputTable)
		if err := runWhy(context.Background(), o, s, "why", target); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", target, err, want)
		}
	}
}
