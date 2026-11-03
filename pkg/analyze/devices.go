package analyze

import (
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
)

// DeviceTaint is a taint on a device and where it came from.
type DeviceTaint struct {
	resourceapi.DeviceTaint `json:",inline"`

	// Rule names the DeviceTaintRule that added the taint. It is empty when
	// the driver published the taint in the ResourceSlice.
	Rule string `json:"rule,omitempty"`
}

// Blocks reports whether the taint keeps the allocator from picking the
// device for requests that do not tolerate it. Only NoSchedule and NoExecute
// do; None and effects unknown to this version are ignored, as upstream.
func (t DeviceTaint) Blocks() bool {
	return t.Effect == resourceapi.DeviceTaintEffectNoSchedule || t.Effect == resourceapi.DeviceTaintEffectNoExecute
}

func (t DeviceTaint) String() string {
	s := t.Key
	if t.Value != "" {
		s += "=" + t.Value
	}
	s += ":" + string(t.Effect)
	if t.Rule != "" {
		s += " (" + t.Rule + ")"
	}
	return s
}

// TaintsOf returns the device's own taints followed by those added by
// matching DeviceTaintRules, the way the scheduler's slice tracker applies
// them: a rule without a selector matches nothing, and each set selector
// field must equal the device's driver, pool or name.
func TaintsOf(ref DeviceRef, rules []*resourceapi.DeviceTaintRule) []DeviceTaint {
	var taints []DeviceTaint
	for _, t := range ref.Device.Taints {
		taints = append(taints, DeviceTaint{DeviceTaint: t})
	}
	for _, r := range rules {
		if ruleMatches(r, ref.Slice, ref.Device) {
			taints = append(taints, DeviceTaint{DeviceTaint: r.Spec.Taint, Rule: r.Name})
		}
	}
	return taints
}

func ruleMatches(r *resourceapi.DeviceTaintRule, slice *resourceapi.ResourceSlice, device *resourceapi.Device) bool {
	sel := r.Spec.DeviceSelector
	return sel != nil &&
		(sel.Driver == nil || *sel.Driver == slice.Spec.Driver) &&
		(sel.Pool == nil || *sel.Pool == slice.Spec.Pool.Name) &&
		(sel.Device == nil || *sel.Device == device.Name)
}

// DeviceStatus summarizes whether a device can be allocated now.
type DeviceStatus string

const (
	// DeviceFree can be allocated to any request that selects it.
	DeviceFree DeviceStatus = "Free"
	// DeviceAllocated is dedicated to at least one claim.
	DeviceAllocated DeviceStatus = "Allocated"
	// DeviceShared is used only by claims sharing it through consumable
	// capacity. It may have room for more.
	DeviceShared DeviceStatus = "Shared"
	// DeviceTainted is unallocated but has a taint that blocks requests
	// without a matching toleration.
	DeviceTainted DeviceStatus = "Tainted"
	// DeviceUnavailable cannot be allocated at all; Reason says why.
	DeviceUnavailable DeviceStatus = "Unavailable"
)

// DeviceInfo is everything known about one device of the current generation.
type DeviceInfo struct {
	DeviceRef
	Pool    *Pool
	Scope   NodeScope
	Holders []Holder
	Taints  []DeviceTaint
	Status  DeviceStatus
	// Reason explains DeviceUnavailable.
	Reason string
}

// Devices lists every device of every pool, in pool order.
//
// Status takes the first that applies: held by a claim (Allocated, or
// Shared when every non-admin holder has a shareID), in a pool the scheduler
// will not use (Unavailable), without node selection the scheduler
// understands (Unavailable), carrying a blocking taint (Tainted), else Free.
// Admin access holders are listed but do not change the status.
func Devices(pools []*Pool, allocations *Allocations, rules []*resourceapi.DeviceTaintRule) []*DeviceInfo {
	var infos []*DeviceInfo
	for _, p := range pools {
		for _, ref := range p.Devices() {
			info := &DeviceInfo{
				DeviceRef: ref,
				Pool:      p,
				Scope:     ScopeOf(ref.Slice, ref.Device),
				Holders:   allocations.Holders(ref.ID),
				Taints:    TaintsOf(ref, rules),
			}
			info.Status, info.Reason = deviceStatus(info)
			infos = append(infos, info)
		}
	}
	return infos
}

func deviceStatus(d *DeviceInfo) (DeviceStatus, string) {
	held, dedicated := false, false
	for _, h := range d.Holders {
		if h.AdminAccess {
			continue
		}
		held = true
		if h.ShareID == nil {
			dedicated = true
		}
	}
	switch {
	case dedicated:
		return DeviceAllocated, ""
	case held:
		return DeviceShared, ""
	case len(d.Pool.Problems) > 0:
		return DeviceUnavailable, "pool is invalid"
	case !d.Pool.Complete():
		return DeviceUnavailable, fmt.Sprintf("pool is incomplete (%d of %d slices)", len(d.Pool.Slices), d.Pool.ResourceSliceCount)
	case !d.Scope.Known():
		return DeviceUnavailable, "no node selection that this version understands"
	}
	for _, t := range d.Taints {
		if t.Blocks() {
			return DeviceTainted, ""
		}
	}
	return DeviceFree, ""
}
