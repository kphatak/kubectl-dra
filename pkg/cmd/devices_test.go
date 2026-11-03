package cmd

import (
	"context"
	"strings"
	"testing"
)

func TestDevices(t *testing.T) {
	s := loadFixtures(t, "devices.yaml")

	tests := []struct {
		golden string
		output string
		flags  devicesFlags
	}{
		{golden: "devices", output: outputTable},
		{golden: "devices-wide", output: outputWide},
		{golden: "devices-json", output: outputJSON},
		{golden: "devices-class-big-gpu", output: outputTable, flags: devicesFlags{class: "big-gpu"}},
		{golden: "devices-free", output: outputTable, flags: devicesFlags{free: true}},
		{golden: "devices-node-b-yaml", output: outputYAML, flags: devicesFlags{node: "node-b"}},
		{golden: "devices-driver-net", output: outputTable, flags: devicesFlags{driver: "net.example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			o, out, errOut := testOptions(tt.output)
			if err := runDevices(context.Background(), o, s, tt.flags); err != nil {
				t.Fatal(err)
			}
			if errOut.Len() > 0 {
				t.Errorf("unexpected stderr: %s", errOut)
			}
			assertGolden(t, tt.golden, out.Bytes())
		})
	}
}

func TestDevicesErrors(t *testing.T) {
	s := loadFixtures(t, "devices.yaml")

	tests := []struct {
		name  string
		flags devicesFlags
		want  string
	}{
		{name: "unknown class", flags: devicesFlags{class: "nope"}, want: `DeviceClass "nope" not found`},
		{name: "unknown node", flags: devicesFlags{node: "nope"}, want: `node "nope" not found`},
		{name: "CEL runtime error", flags: devicesFlags{class: "broken"}, want: "selector #0: CEL runtime error"},
		{name: "CEL compile error", flags: devicesFlags{class: "bad-syntax"}, want: "selector #0: CEL compile error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, _, _ := testOptions(outputTable)
			err := runDevices(context.Background(), o, s, tt.flags)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}

	t.Run("no match says so on stderr", func(t *testing.T) {
		o, out, errOut := testOptions(outputTable)
		if err := runDevices(context.Background(), o, s, devicesFlags{driver: "missing.example.com"}); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), "No devices found") {
			t.Errorf("stdout %q, stderr %q", out, errOut)
		}
	})
}
