package snapshot

import (
	resourceapi "k8s.io/api/resource/v1"
)

// setClaimDefaults fills in the request fields the API server defaults on
// create: allocationMode ExactCount, count 1 for ExactCount, and toleration
// operator Equal. Objects read from a cluster already have them; objects
// from hand-written files may not, and the upstream allocator relies on
// them.
func setClaimDefaults(spec *resourceapi.ResourceClaimSpec) {
	for i := range spec.Devices.Requests {
		r := &spec.Devices.Requests[i]
		if r.Exactly != nil {
			defaultRequest(&r.Exactly.AllocationMode, &r.Exactly.Count, r.Exactly.Tolerations)
		}
		for j := range r.FirstAvailable {
			sub := &r.FirstAvailable[j]
			defaultRequest(&sub.AllocationMode, &sub.Count, sub.Tolerations)
		}
	}
}

func defaultRequest(mode *resourceapi.DeviceAllocationMode, count *int64, tolerations []resourceapi.DeviceToleration) {
	if *mode == "" {
		*mode = resourceapi.DeviceAllocationModeExactCount
	}
	if *mode == resourceapi.DeviceAllocationModeExactCount && *count == 0 {
		*count = 1
	}
	for i := range tolerations {
		if tolerations[i].Operator == "" {
			tolerations[i].Operator = resourceapi.DeviceTolerationOpEqual
		}
	}
}
