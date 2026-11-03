package cmd

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nodesNamed(names ...string) []*corev1.Node {
	var nodes []*corev1.Node
	for _, n := range names {
		nodes = append(nodes, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n}})
	}
	return nodes
}

func TestCommonDomainSuffix(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{[]string{"node-1.zone-a.example.com", "node-22.zone-a.example.com"}, ".zone-a.example.com"},
		{[]string{"a.x.example.com", "b.y.example.com"}, ".example.com"},
		{[]string{"node-1.zone-a.example.com"}, ".zone-a.example.com"},
		{[]string{"node-a", "node-b"}, ""},                 // kind: nothing to drop
		{[]string{"node-a.example.com", "node-b"}, ""},     // mixed
		{[]string{"x.example.com", "example.com"}, ".com"}, // keep a label in front of every name
		{nil, ""},
	}
	for _, tt := range tests {
		if got := commonDomainSuffix(nodesNamed(tt.names...)); got != tt.want {
			t.Errorf("%v: got %q, want %q", tt.names, got, tt.want)
		}
	}
}

func TestShortenerDoesNotTouchOtherText(t *testing.T) {
	s := newNodeShortener(nodesNamed("node-1.zone.example.com", "node-10.zone.example.com"))
	in := "node-10.zone.example.com node-1.zone.example.com gpu.example.com/node-1.zone.example.com/gpu-0 zone.example.com node-100"
	want := "node-10 node-1 gpu.example.com/node-1/gpu-0 zone.example.com node-100"
	if got := s.apply(in); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestShortNodeNames(t *testing.T) {
	s := loadFixtures(t, "fqdn.yaml")
	const note = `Node names are shown without ".zone-a.example.com"; use -o wide for full names.`

	tests := []struct {
		golden string
		output string
		run    func(o *Options) error
	}{
		{golden: "fqdn-devices", output: outputTable, run: func(o *Options) error { return runDevices(context.Background(), o, s, devicesFlags{}) }},
		{golden: "fqdn-devices-wide", output: outputWide, run: func(o *Options) error { return runDevices(context.Background(), o, s, devicesFlags{}) }},
		{golden: "fqdn-pools", output: outputTable, run: func(o *Options) error { return runPools(o, s, poolsFlags{}) }},
		{golden: "fqdn-nodes", output: outputTable, run: func(o *Options) error { return runNodes(o, s, nodesFlags{}) }},
		{golden: "fqdn-map", output: outputTable, run: func(o *Options) error { return runMap(o, s, "") }},
		{golden: "fqdn-why", output: outputTable, run: func(o *Options) error {
			return runWhy(context.Background(), o, s, "ml", "pod/on-cpu-node")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			o, out, errOut := testOptions(tt.output)
			if err := tt.run(o); err != nil {
				t.Fatal(err)
			}
			assertGolden(t, tt.golden, out.Bytes())

			wantNote := tt.output == outputTable
			if got := strings.Contains(errOut.String(), note); got != wantNote {
				t.Errorf("stderr note present=%v, want %v; stderr: %q", got, wantNote, errOut)
			}
			hasFull := strings.Contains(out.String(), ".zone-a.example.com")
			if hasFull == wantNote {
				t.Errorf("full node names in output=%v for -o %s", hasFull, tt.output)
			}
		})
	}

	t.Run("json keeps full names", func(t *testing.T) {
		o, out, errOut := testOptions(outputJSON)
		if err := runDevices(context.Background(), o, s, devicesFlags{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "gpu-node-1.zone-a.example.com") || errOut.Len() > 0 {
			t.Errorf("json output shortened or noted: stderr %q", errOut)
		}
	})
}
