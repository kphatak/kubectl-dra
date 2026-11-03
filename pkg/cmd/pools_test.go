package cmd

import (
	"strings"
	"testing"
)

func TestPools(t *testing.T) {
	s := loadFixtures(t, "pools.yaml", "allocations.yaml")

	tests := []struct {
		golden string
		output string
		flags  poolsFlags
	}{
		{golden: "pools", output: outputTable},
		{golden: "pools-wide", output: outputWide},
		{golden: "pools-json", output: outputJSON},
		{golden: "pools-driver-yaml", output: outputYAML, flags: poolsFlags{driver: "net.example.com"}},
		{golden: "pools-node-b", output: outputTable, flags: poolsFlags{node: "node-b"}},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			o, out, errOut := testOptions(tt.output)
			if err := runPools(o, s, tt.flags); err != nil {
				t.Fatal(err)
			}
			if errOut.Len() > 0 {
				t.Errorf("unexpected stderr: %s", errOut)
			}
			assertGolden(t, tt.golden, out.Bytes())
		})
	}
}

func TestPoolsNoMatch(t *testing.T) {
	s := loadFixtures(t, "pools.yaml")

	t.Run("table says so on stderr", func(t *testing.T) {
		o, out, errOut := testOptions(outputTable)
		if err := runPools(o, s, poolsFlags{driver: "missing.example.com"}); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), "No pools found") {
			t.Errorf("stdout %q, stderr %q", out, errOut)
		}
	})

	t.Run("json is an empty list, not null", func(t *testing.T) {
		o, out, _ := testOptions(outputJSON)
		if err := runPools(o, s, poolsFlags{driver: "missing.example.com"}); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(out.String()); got != "{\n  \"pools\": []\n}" {
			t.Errorf("got %s", got)
		}
	})

	t.Run("unknown node is an error", func(t *testing.T) {
		o, _, _ := testOptions(outputTable)
		err := runPools(o, s, poolsFlags{node: "nope"})
		if err == nil || !strings.Contains(err.Error(), `node "nope" not found`) {
			t.Errorf("got %v", err)
		}
	})
}
