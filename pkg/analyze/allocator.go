package analyze

import (
	"context"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/structured"
)

// InferFeatures returns the allocator features needed to understand the
// claims and slices: a feature is on when some object uses fields that only
// it handles. The scheduler's real settings come from feature gates we
// cannot read, so a cluster with a feature off may decide differently;
// callers report the features they used.
func InferFeatures(claims []*resourceapi.ResourceClaim, resourceSlices []*resourceapi.ResourceSlice, rules []*resourceapi.DeviceTaintRule) structured.Features {
	var f structured.Features
	f.DeviceTaints = len(rules) > 0

	for _, c := range claims {
		for _, r := range c.Spec.Devices.Requests {
			if r.Exactly != nil {
				f.AdminAccess = f.AdminAccess || r.Exactly.AdminAccess != nil
				f.ConsumableCapacity = f.ConsumableCapacity || r.Exactly.Capacity != nil
				f.DerivedAttributes = f.DerivedAttributes || len(r.Exactly.DerivedAttributes) > 0
			}
			if len(r.FirstAvailable) > 0 {
				f.PrioritizedList = true
			}
			for _, sub := range r.FirstAvailable {
				f.ConsumableCapacity = f.ConsumableCapacity || sub.Capacity != nil
				f.DerivedAttributes = f.DerivedAttributes || len(sub.DerivedAttributes) > 0
			}
		}
	}

	for _, s := range resourceSlices {
		if len(s.Spec.SharedCounters) > 0 || s.Spec.PerDeviceNodeSelection != nil {
			f.PartitionableDevices = true
		}
		f.OptionalNodeOperations = f.OptionalNodeOperations || len(s.Spec.SkipNodeOperations) > 0
		for _, d := range s.Spec.Devices {
			f.DeviceTaints = f.DeviceTaints || len(d.Taints) > 0
			f.DeviceBindingAndStatus = f.DeviceBindingAndStatus || len(d.BindingConditions) > 0 || d.BindsToNode != nil
			f.ConsumableCapacity = f.ConsumableCapacity || d.AllowMultipleAllocations != nil
			for _, c := range d.ConsumesCounters {
				f.PartitionableDevices = true
				f.CompatibilityGroups = f.CompatibilityGroups || len(c.CompatibilityGroups) > 0
			}
			for _, a := range d.Attributes {
				if a.IntValues != nil || a.BoolValues != nil || a.StringValues != nil || a.VersionValues != nil {
					f.ListTypeAttributes = true
				}
			}
		}
	}
	return f
}

// FeatureNames lists the enabled features by their feature gate names.
func FeatureNames(f structured.Features) []string {
	return sets.List(f.Set())
}

// AllocatorCheck is what the upstream allocator decides for a claim.
type AllocatorCheck struct {
	// Features are the allocator features used, by feature gate name.
	Features []string `json:"features"`
	// NodesTried counts the candidate nodes the allocator ran for.
	NodesTried int `json:"nodesTried"`
	// Nodes are where the allocator found an allocation.
	Nodes []string `json:"nodes"`
	// NodeErrors are failures limited to one node, such as invalid pools.
	NodeErrors []string `json:"nodeErrors,omitempty"`
	// AbortingNodes are nodes where the allocator returns a fatal error,
	// such as a request for all devices next to an incomplete pool. If the
	// scheduler evaluates such a node for the pod, it aborts the whole
	// scheduling attempt, even when other nodes would work.
	AbortingNodes []string `json:"abortingNodes,omitempty"`
	// Error is set when the allocator fails on every node it tried, or
	// cannot start, which is an input problem such as a missing class.
	Error string `json:"error,omitempty"`
}

// CheckWithAllocator runs structured.Allocator for the claim on every
// candidate node, with allocated state from all other claims, slices with
// DeviceTaintRules applied, and features inferred from the data.
func CheckWithAllocator(ctx context.Context, claim *resourceapi.ResourceClaim, allClaims []*resourceapi.ResourceClaim,
	classes []*resourceapi.DeviceClass, resourceSlices []*resourceapi.ResourceSlice, rules []*resourceapi.DeviceTaintRule, nodes []*corev1.Node,
) *AllocatorCheck {
	features := InferFeatures(append(slices.Clone(allClaims), claim), resourceSlices, rules)
	check := &AllocatorCheck{Features: FeatureNames(features), Nodes: []string{}}

	var others []*resourceapi.ResourceClaim
	for _, c := range allClaims {
		if c.UID != claim.UID || c.Namespace != claim.Namespace || c.Name != claim.Name {
			others = append(others, c)
		}
	}
	state := BuildAllocations(others, features.ConsumableCapacity).State
	celCache := cel.NewCache(maxCELCacheEntries, cel.Features{
		EnableConsumableCapacity: features.ConsumableCapacity,
		EnableListTypeAttributes: features.ListTypeAttributes,
	})
	allocator, err := structured.NewAllocator(ctx, features, state, classLister(classes), withTaintRules(resourceSlices, rules), celCache)
	if err != nil {
		check.Error = err.Error()
		return check
	}

	firstFatal := ""
	for _, node := range nodes {
		check.NodesTried++
		results, err := allocator.Allocate(ctx, node, []*resourceapi.ResourceClaim{claim.DeepCopy()})
		switch {
		case errors.Is(err, structured.ErrFailedAllocationOnNode):
			check.NodeErrors = append(check.NodeErrors, fmt.Sprintf("%s: %v", node.Name, err))
		case err != nil:
			check.AbortingNodes = append(check.AbortingNodes, node.Name)
			check.NodeErrors = append(check.NodeErrors, fmt.Sprintf("%s: %v", node.Name, err))
			if firstFatal == "" {
				firstFatal = err.Error()
			}
		case results != nil:
			check.Nodes = append(check.Nodes, node.Name)
		}
	}
	if len(check.Nodes) == 0 && len(check.AbortingNodes) > 0 && len(check.AbortingNodes) == check.NodesTried {
		check.Error = firstFatal
	}
	return check
}

const maxCELCacheEntries = 256

// classLister serves DeviceClasses from the snapshot.
type classLister []*resourceapi.DeviceClass

func (l classLister) List() ([]*resourceapi.DeviceClass, error) { return l, nil }

func (l classLister) Get(name string) (*resourceapi.DeviceClass, error) {
	for _, c := range l {
		if c.Name == name {
			return c, nil
		}
	}
	return nil, apierrors.NewNotFound(resourceapi.Resource("deviceclasses"), name)
}

// withTaintRules returns the slices with DeviceTaintRule taints added to
// matching devices, which is what the scheduler's slice tracker feeds the
// allocator. Slices without matching devices are returned as is.
func withTaintRules(resourceSlices []*resourceapi.ResourceSlice, rules []*resourceapi.DeviceTaintRule) []*resourceapi.ResourceSlice {
	if len(rules) == 0 {
		return resourceSlices
	}
	out := make([]*resourceapi.ResourceSlice, len(resourceSlices))
	for i, s := range resourceSlices {
		patched := s
		for d := range s.Spec.Devices {
			for _, r := range rules {
				if !ruleMatches(r, s, &s.Spec.Devices[d]) {
					continue
				}
				if patched == s {
					patched = s.DeepCopy()
				}
				patched.Spec.Devices[d].Taints = append(patched.Spec.Devices[d].Taints, r.Spec.Taint)
			}
		}
		out[i] = patched
	}
	return out
}
