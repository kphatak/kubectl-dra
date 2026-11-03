package celx

import (
	"context"
	"errors"
	"strings"
	"testing"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

func selectors(exprs ...string) []resourceapi.DeviceSelector {
	out := make([]resourceapi.DeviceSelector, len(exprs))
	for i, e := range exprs {
		out[i] = resourceapi.DeviceSelector{CEL: &resourceapi.CELDeviceSelector{Expression: e}}
	}
	return out
}

func TestValues(t *testing.T) {
	device := &resourceapi.Device{
		Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
			"model":                {StringValue: ptr.To("A100")},
			"example.com/firmware": {VersionValue: ptr.To("1.2.3")},
		},
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory": {Value: resource.MustParse("80Gi")},
		},
	}
	expr := `device.attributes["gpu.example.com"].model == "H100" &&
		device.capacity['gpu.example.com'].memory.compareTo(quantity("40Gi")) >= 0 &&
		device.attributes["example.com"]["firmware"].isGreaterThan(semver("2.0.0")) &&
		device.attributes["gpu.example.com"].model != "" &&
		device.attributes["gpu.example.com"].nvlink`
	want := `gpu.example.com/model="A100", gpu.example.com/memory=80Gi, example.com/firmware=version 1.2.3, gpu.example.com/nvlink=<missing>`
	if got := Values(expr, "gpu.example.com", device); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestMatch(t *testing.T) {
	device := &resourceapi.Device{
		Name: "gpu-0",
		Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
			"model":                     {StringValue: ptr.To("A100")},
			"index":                     {IntValue: ptr.To[int64](0)},
			"example.com/firmware":      {VersionValue: ptr.To("1.2.3")},
			"gpu.example.com/supported": {BoolValues: []bool{true, false}},
		},
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory": {Value: resource.MustParse("80Gi")},
		},
	}

	tests := []struct {
		name      string
		selectors []resourceapi.DeviceSelector
		mismatch  int // -1 for a match
		errPart   string
		compile   bool
	}{
		{name: "no selectors", mismatch: -1},
		{
			name: "unqualified names default to the driver domain",
			selectors: selectors(
				`device.driver == "gpu.example.com"`,
				`device.attributes["gpu.example.com"].model == "A100"`,
				`device.capacity["gpu.example.com"].memory.compareTo(quantity("40Gi")) >= 0`,
				`device.attributes["example.com"].firmware.isGreaterThan(semver("1.0.0"))`,
			),
			mismatch: -1,
		},
		{
			name:      "first false selector is reported",
			selectors: selectors(`device.driver == "gpu.example.com"`, `device.attributes["gpu.example.com"].index == 3`, `false`),
			mismatch:  1,
		},
		{name: "compile error", selectors: selectors(`device.driver ==`), errPart: "CEL compile error", compile: true},
		{name: "runtime error on a missing attribute", selectors: selectors(`device.attributes["gpu.example.com"].nosuch == 1`), errPart: "CEL runtime error"},
	}

	e := NewEvaluator()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := e.Match(context.Background(), "gpu.example.com", device, tt.selectors)
			if tt.errPart != "" {
				var se *SelectorError
				if !errors.As(err, &se) || !strings.Contains(err.Error(), tt.errPart) || se.Compile != tt.compile {
					t.Fatalf("got %v, want a SelectorError containing %q (compile=%v)", err, tt.errPart, tt.compile)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tt.mismatch < 0 && m != nil:
				t.Errorf("unexpected mismatch at selector #%d: %s", m.Index, m.Expression)
			case tt.mismatch >= 0 && (m == nil || m.Index != tt.mismatch):
				t.Errorf("got mismatch %+v, want selector #%d", m, tt.mismatch)
			}
		})
	}
}
