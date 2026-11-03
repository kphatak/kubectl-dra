package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const basicFixture = "../../test/fixtures/basic.yaml"

func TestLoadFiles(t *testing.T) {
	s, err := LoadFiles(basicFixture)
	if err != nil {
		t.Fatal(err)
	}
	assertBasicCounts(t, s)

	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "ConfigMap") {
		t.Errorf("want one warning about the ConfigMap, got %q", s.Warnings)
	}

	// Attribute and capacity unions decode silently to nil on a typo, so
	// check that the fixture really populated them.
	dev := s.ResourceSlices[0].Spec.Devices[1]
	if v := dev.Attributes["index"].IntValue; v == nil || *v != 1 {
		t.Errorf("gpu-1 index: got %v, want 1", v)
	}
	if v := dev.Attributes["driverVersion"].VersionValue; v == nil || *v != "1.0.0" {
		t.Errorf("gpu-1 driverVersion: got %v, want 1.0.0", v)
	}
	if q := dev.Capacity["memory"].Value; q.String() != "80Gi" {
		t.Errorf("gpu-1 memory: got %s, want 80Gi", q.String())
	}

	pod := s.Pods[0]
	if got := *pod.Status.ResourceClaimStatuses[0].ResourceClaimName; got != "pod0-gpu-x7k2p" {
		t.Errorf("generated claim name: got %q", got)
	}
}

func TestToListRoundTrip(t *testing.T) {
	s, err := LoadFiles(basicFixture)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ToList()
	if err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dump.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	back, err := LoadFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	assertBasicCounts(t, back)
	if len(back.Warnings) != 0 {
		t.Errorf("round trip produced warnings: %q", back.Warnings)
	}
	if got := back.ResourceClaims[0].Status.Allocation.Devices.Results[0].Device; got != "gpu-0" {
		t.Errorf("allocated device after round trip: got %q, want gpu-0", got)
	}
}

func assertBasicCounts(t *testing.T, s *Snapshot) {
	t.Helper()
	counts := map[string][2]int{
		"nodes":          {len(s.Nodes), 1},
		"pods":           {len(s.Pods), 1},
		"deviceclasses":  {len(s.DeviceClasses), 1},
		"resourceslices": {len(s.ResourceSlices), 1},
		"resourceclaims": {len(s.ResourceClaims), 1},
		"templates":      {len(s.ResourceClaimTemplates), 1},
	}
	for name, c := range counts {
		if c[0] != c[1] {
			t.Errorf("%s: got %d, want %d", name, c[0], c[1])
		}
	}
}
