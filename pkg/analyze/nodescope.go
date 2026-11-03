package analyze

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"
)

// NodeScope says which nodes can reach a device. At most one field is set.
type NodeScope struct {
	NodeName     string
	AllNodes     bool
	NodeSelector *corev1.NodeSelector
}

// ScopeOf returns where a device is usable. Normally that is set on the
// slice; with perDeviceNodeSelection each device carries its own.
func ScopeOf(slice *resourceapi.ResourceSlice, device *resourceapi.Device) NodeScope {
	if ptr.Deref(slice.Spec.PerDeviceNodeSelection, false) {
		return NodeScope{
			NodeName:     ptr.Deref(device.NodeName, ""),
			AllNodes:     ptr.Deref(device.AllNodes, false),
			NodeSelector: device.NodeSelector,
		}
	}
	return NodeScope{
		NodeName:     ptr.Deref(slice.Spec.NodeName, ""),
		AllNodes:     ptr.Deref(slice.Spec.AllNodes, false),
		NodeSelector: slice.Spec.NodeSelector,
	}
}

// Known reports whether any node selection is set. The scheduler skips
// devices without one, assuming a future API extension it does not
// understand.
func (s NodeScope) Known() bool {
	return s.NodeName != "" || s.AllNodes || s.NodeSelector != nil
}

// Matches reports whether node can reach the device, using the same
// matching code as the allocator.
func (s NodeScope) Matches(node *corev1.Node) (bool, error) {
	return structured.NodeMatches(structured.Features{}, node, s.NodeName, s.AllNodes, s.NodeSelector)
}

// Nodes returns the names of the given nodes that can reach the device.
func (s NodeScope) Nodes(nodes []*corev1.Node) ([]string, error) {
	var names []string
	for _, n := range nodes {
		ok, err := s.Matches(n)
		if err != nil {
			return nil, err
		}
		if ok {
			names = append(names, n.Name)
		}
	}
	return names, nil
}

// Describe is String, except that a node selector matching exactly one of
// the given nodes shows as that node's name.
func (s NodeScope) Describe(nodes []*corev1.Node) string {
	if s.NodeSelector != nil && !s.AllNodes && s.NodeName == "" {
		if names, err := s.Nodes(nodes); err == nil && len(names) == 1 {
			return names[0]
		}
	}
	return s.String()
}

// String renders the scope for tables: a node name, "all nodes", or the
// selector with terms joined by " || " and requirements by " && ".
func (s NodeScope) String() string {
	switch {
	case s.NodeName != "":
		return s.NodeName
	case s.AllNodes:
		return "all nodes"
	case s.NodeSelector != nil:
		terms := make([]string, 0, len(s.NodeSelector.NodeSelectorTerms))
		for _, t := range s.NodeSelector.NodeSelectorTerms {
			var reqs []string
			for _, r := range t.MatchExpressions {
				reqs = append(reqs, formatRequirement(r))
			}
			for _, r := range t.MatchFields {
				reqs = append(reqs, formatRequirement(r))
			}
			terms = append(terms, strings.Join(reqs, " && "))
		}
		return strings.Join(terms, " || ")
	default:
		return "unknown"
	}
}

func formatRequirement(r corev1.NodeSelectorRequirement) string {
	switch r.Operator {
	case corev1.NodeSelectorOpExists, corev1.NodeSelectorOpDoesNotExist:
		return fmt.Sprintf("%s %s", r.Key, r.Operator)
	case corev1.NodeSelectorOpIn, corev1.NodeSelectorOpNotIn:
		return fmt.Sprintf("%s %s (%s)", r.Key, r.Operator, strings.Join(r.Values, ","))
	default:
		return fmt.Sprintf("%s %s %s", r.Key, r.Operator, strings.Join(r.Values, ","))
	}
}
