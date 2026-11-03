package cmd

import (
	"strings"
	"testing"
)

func TestMap(t *testing.T) {
	s := loadFixtures(t, "map.yaml")

	tests := []struct {
		golden    string
		output    string
		namespace string
	}{
		{golden: "map-all", output: outputTable},
		{golden: "map-team-a-wide", output: outputWide, namespace: "team-a"},
		{golden: "map-team-b-json", output: outputJSON, namespace: "team-b"},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			o, out, errOut := testOptions(tt.output)
			if err := runMap(o, s, tt.namespace); err != nil {
				t.Fatal(err)
			}
			if errOut.Len() > 0 {
				t.Errorf("unexpected stderr: %s", errOut)
			}
			assertGolden(t, tt.golden, out.Bytes())
		})
	}
}

func TestMapEmptyNamespace(t *testing.T) {
	s := loadFixtures(t, "map.yaml")
	o, out, errOut := testOptions(outputTable)
	if err := runMap(o, s, "empty"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "No resource claims found in empty namespace") {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}
}
