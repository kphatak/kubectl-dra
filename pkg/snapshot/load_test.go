package snapshot

import (
	"context"
	"strings"
	"testing"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeCluster returns a fake clientset holding the basic fixture and
// advertising the given resource.k8s.io/v1 resources through discovery.
func fakeCluster(t *testing.T, resources ...string) *fake.Clientset {
	t.Helper()
	s, err := LoadFiles(basicFixture)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := s.Objects()
	if err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset(objs...)
	if resources != nil {
		list := &metav1.APIResourceList{GroupVersion: resourceapi.SchemeGroupVersion.String()}
		for _, r := range resources {
			list.APIResources = append(list.APIResources, metav1.APIResource{Name: r})
		}
		cs.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{list}
	}
	return cs
}

var allResources = []string{"deviceclasses", "resourceslices", "resourceclaims", "resourceclaimtemplates", "devicetaintrules"}

func TestLoad(t *testing.T) {
	s, err := Load(context.Background(), fakeCluster(t, allResources...), LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertBasicCounts(t, s)
	if len(s.Warnings) != 0 {
		t.Errorf("unexpected warnings: %q", s.Warnings)
	}
}

func TestLoadNamespaceScopesPodsNotClaims(t *testing.T) {
	s, err := Load(context.Background(), fakeCluster(t, allResources...), LoadOptions{Namespace: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Pods) != 0 {
		t.Errorf("pods: got %d, want 0 in namespace other", len(s.Pods))
	}
	if len(s.ResourceClaims) != 1 {
		t.Errorf("claims: got %d, want 1 (claims are always cluster-wide)", len(s.ResourceClaims))
	}
}

func TestLoadSkipWorkloads(t *testing.T) {
	s, err := Load(context.Background(), fakeCluster(t, allResources...), LoadOptions{SkipWorkloads: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Pods) != 0 || len(s.ResourceClaimTemplates) != 0 {
		t.Errorf("got %d pods and %d templates, want none", len(s.Pods), len(s.ResourceClaimTemplates))
	}
	if len(s.ResourceSlices) != 1 || len(s.ResourceClaims) != 1 {
		t.Errorf("got %d slices and %d claims, want 1 each", len(s.ResourceSlices), len(s.ResourceClaims))
	}
}

func TestLoadV1NotServed(t *testing.T) {
	_, err := Load(context.Background(), fakeCluster(t), LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "does not serve resource.k8s.io/v1") {
		t.Fatalf("got %v, want a not-served error", err)
	}
}

func TestLoadForbidden(t *testing.T) {
	forbid := func(resource string) k8stesting.ReactionFunc {
		return func(k8stesting.Action) (bool, runtime.Object, error) {
			gr := resourceapi.Resource(resource)
			return true, nil, apierrors.NewForbidden(gr, "", nil)
		}
	}

	t.Run("optional resource becomes a warning", func(t *testing.T) {
		cs := fakeCluster(t, allResources...)
		cs.PrependReactor("list", "resourceclaimtemplates", forbid("resourceclaimtemplates"))
		s, err := Load(context.Background(), cs, LoadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "resourceclaimtemplates") {
			t.Errorf("want one warning about resourceclaimtemplates, got %q", s.Warnings)
		}
	})

	t.Run("required resource fails the load", func(t *testing.T) {
		cs := fakeCluster(t, allResources...)
		cs.PrependReactor("list", "resourceclaims", forbid("resourceclaims"))
		_, err := Load(context.Background(), cs, LoadOptions{})
		if err == nil || !strings.Contains(err.Error(), "docs/rbac.md") {
			t.Fatalf("got %v, want a forbidden error pointing at docs/rbac.md", err)
		}
	})
}

func TestLoadSkipsUnservedDeviceTaintRules(t *testing.T) {
	cs := fakeCluster(t, "deviceclasses", "resourceslices", "resourceclaims", "resourceclaimtemplates")
	var listedRules bool
	cs.PrependReactor("list", "devicetaintrules", func(k8stesting.Action) (bool, runtime.Object, error) {
		listedRules = true
		return false, nil, nil
	})
	if _, err := Load(context.Background(), cs, LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	if listedRules {
		t.Error("listed devicetaintrules although discovery does not serve them")
	}
}
