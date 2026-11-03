package cmd

import (
	"strings"
	"testing"
)

func TestNodes(t *testing.T) {
	s := loadFixtures(t, "nodes.yaml")

	tests := []struct {
		golden string
		output string
		flags  nodesFlags
	}{
		{golden: "nodes", output: outputTable},
		{golden: "nodes-wide", output: outputWide},
		{golden: "nodes-issues-json", output: outputJSON, flags: nodesFlags{issues: true}},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			o, out, errOut := testOptions(tt.output)
			if err := runNodes(o, s, tt.flags); err != nil {
				t.Fatal(err)
			}
			if errOut.Len() > 0 {
				t.Errorf("unexpected stderr: %s", errOut)
			}
			assertGolden(t, tt.golden, out.Bytes())
		})
	}
}

func TestNodesNoIssues(t *testing.T) {
	s := loadFixtures(t, "devices.yaml")
	o, out, errOut := testOptions(outputTable)
	if err := runNodes(o, s, nodesFlags{issues: true}); err != nil {
		t.Fatal(err)
	}
	// devices.yaml has an incomplete pool on node-b, so that node is listed.
	if !strings.Contains(out.String(), "pool gpu.example.com/node-b is Incomplete") || errOut.Len() > 0 {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}

	s = loadFixtures(t, "basic.yaml")
	o, out, errOut = testOptions(outputTable)
	if err := runNodes(o, s, nodesFlags{issues: true}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "No nodes with issues") {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}
}
