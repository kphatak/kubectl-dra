// Package snapshot loads the cluster state that every kubectl-dra command
// analyzes. A Snapshot is read once per command, either from the API server or
// from YAML files, and is never mutated by analyzers.
package snapshot

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

// Snapshot is a point-in-time view of the objects relevant to DRA.
//
// ResourceClaims always covers every namespace, even when Pods is scoped to
// one, because allocated device state is cluster-wide: a device held by a
// claim in another namespace is still unavailable.
type Snapshot struct {
	Nodes                  []*corev1.Node
	Pods                   []*corev1.Pod
	Events                 []*corev1.Event
	DeviceClasses          []*resourceapi.DeviceClass
	ResourceSlices         []*resourceapi.ResourceSlice
	ResourceClaims         []*resourceapi.ResourceClaim
	ResourceClaimTemplates []*resourceapi.ResourceClaimTemplate
	DeviceTaintRules       []*resourceapi.DeviceTaintRule

	// Source describes where the data came from, for example
	// "resource.k8s.io/v1" or "file: dump.yaml".
	Source string

	// Warnings records non-fatal problems hit while loading, such as an
	// optional resource that the user is not allowed to list. Commands print
	// them so that partial data is never mistaken for complete data.
	Warnings []string
}

// Add appends a typed object to the matching slice. It returns false for
// kinds that kubectl-dra does not use. Claims and claim templates get the
// API server's defaults, so file-based objects behave like live ones.
func (s *Snapshot) Add(obj runtime.Object) bool {
	switch o := obj.(type) {
	case *corev1.Node:
		s.Nodes = append(s.Nodes, o)
	case *corev1.Pod:
		s.Pods = append(s.Pods, o)
	case *corev1.Event:
		s.Events = append(s.Events, o)
	case *resourceapi.DeviceClass:
		s.DeviceClasses = append(s.DeviceClasses, o)
	case *resourceapi.ResourceSlice:
		s.ResourceSlices = append(s.ResourceSlices, o)
	case *resourceapi.ResourceClaim:
		setClaimDefaults(&o.Spec)
		s.ResourceClaims = append(s.ResourceClaims, o)
	case *resourceapi.ResourceClaimTemplate:
		setClaimDefaults(&o.Spec.Spec)
		s.ResourceClaimTemplates = append(s.ResourceClaimTemplates, o)
	case *resourceapi.DeviceTaintRule:
		s.DeviceTaintRules = append(s.DeviceTaintRules, o)
	default:
		return false
	}
	return true
}

// Objects returns every object in the snapshot with its apiVersion and kind
// set, in a stable order, so the result can be serialized and read back by
// LoadFiles.
func (s *Snapshot) Objects() ([]runtime.Object, error) {
	var objs []runtime.Object
	add := func(o runtime.Object) { objs = append(objs, o) }
	for _, o := range s.Nodes {
		add(o)
	}
	for _, o := range s.Pods {
		add(o)
	}
	for _, o := range s.Events {
		add(o)
	}
	for _, o := range s.DeviceClasses {
		add(o)
	}
	for _, o := range s.ResourceSlices {
		add(o)
	}
	for _, o := range s.ResourceClaims {
		add(o)
	}
	for _, o := range s.ResourceClaimTemplates {
		add(o)
	}
	for _, o := range s.DeviceTaintRules {
		add(o)
	}
	for _, o := range objs {
		if err := setTypeMeta(o); err != nil {
			return nil, err
		}
	}
	return objs, nil
}

// setTypeMeta fills in apiVersion and kind, which typed clients leave empty.
func setTypeMeta(obj runtime.Object) error {
	gvks, _, err := scheme.Scheme.ObjectKinds(obj)
	if err != nil {
		return fmt.Errorf("looking up kind for %T: %w", obj, err)
	}
	obj.GetObjectKind().SetGroupVersionKind(gvks[0])
	return nil
}

// Sort orders every slice by namespace and name so that output and golden
// tests are deterministic regardless of API server or file order.
func (s *Snapshot) Sort() {
	sortByKey(s.Nodes, func(o *corev1.Node) string { return o.Name })
	sortByKey(s.Pods, func(o *corev1.Pod) string { return o.Namespace + "/" + o.Name })
	sortByKey(s.Events, func(o *corev1.Event) string { return o.Namespace + "/" + o.Name })
	sortByKey(s.DeviceClasses, func(o *resourceapi.DeviceClass) string { return o.Name })
	sortByKey(s.ResourceSlices, func(o *resourceapi.ResourceSlice) string { return o.Name })
	sortByKey(s.ResourceClaims, func(o *resourceapi.ResourceClaim) string { return o.Namespace + "/" + o.Name })
	sortByKey(s.ResourceClaimTemplates, func(o *resourceapi.ResourceClaimTemplate) string { return o.Namespace + "/" + o.Name })
	sortByKey(s.DeviceTaintRules, func(o *resourceapi.DeviceTaintRule) string { return o.Name })
}

func sortByKey[T any](items []T, key func(T) string) {
	sort.SliceStable(items, func(i, j int) bool { return key(items[i]) < key(items[j]) })
}
