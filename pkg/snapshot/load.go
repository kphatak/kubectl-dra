package snapshot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/pager"
)

// LoadOptions controls what Load reads from the cluster.
type LoadOptions struct {
	// Namespace scopes pods, events and claim templates. Empty means all
	// namespaces. ResourceClaims are always listed cluster-wide.
	Namespace string

	// Events also lists events in Namespace. Only commands that explain
	// scheduling failures need them, and they can be large.
	Events bool

	// SkipWorkloads leaves out pods and claim templates, for commands that
	// only look at devices. Pods are by far the largest list in most
	// clusters.
	SkipWorkloads bool
}

// Load lists everything a command needs in parallel, using only list calls.
//
// Required resources (nodes, pods, device classes, slices, claims) fail the
// load. Optional ones (events, claim templates, DeviceTaintRules) only add a
// warning when they are forbidden or not served.
func Load(ctx context.Context, cs kubernetes.Interface, opts LoadOptions) (*Snapshot, error) {
	served, err := servedResources(cs.Discovery())
	if err != nil {
		return nil, err
	}

	s := &Snapshot{Source: resourceapi.SchemeGroupVersion.String()}
	l := &loader{snap: s}
	g, ctx := errgroup.WithContext(ctx)

	core := cs.CoreV1()
	res := cs.ResourceV1()
	l.list(ctx, g, "nodes", true, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return core.Nodes().List(ctx, o)
	})
	if !opts.SkipWorkloads {
		l.list(ctx, g, "pods", true, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return core.Pods(opts.Namespace).List(ctx, o)
		})
		l.list(ctx, g, "resourceclaimtemplates", false, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return res.ResourceClaimTemplates(opts.Namespace).List(ctx, o)
		})
	}
	l.list(ctx, g, "deviceclasses", true, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return res.DeviceClasses().List(ctx, o)
	})
	l.list(ctx, g, "resourceslices", true, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return res.ResourceSlices().List(ctx, o)
	})
	l.list(ctx, g, "resourceclaims", true, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return res.ResourceClaims(metav1.NamespaceAll).List(ctx, o)
	})
	// DeviceTaintRules sit behind their own feature gate, so the API server
	// may serve resource.k8s.io/v1 without them. That is normal, not a warning.
	if served["devicetaintrules"] {
		l.list(ctx, g, "devicetaintrules", false, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return res.DeviceTaintRules().List(ctx, o)
		})
	}
	if opts.Events {
		l.list(ctx, g, "events", false, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return core.Events(opts.Namespace).List(ctx, o)
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	sort.Strings(s.Warnings)
	s.Sort()
	return s, nil
}

// servedResources checks that the API server serves resource.k8s.io/v1 and
// returns the set of resource names it serves in that version.
func servedResources(dc discovery.DiscoveryInterface) (map[string]bool, error) {
	gv := resourceapi.SchemeGroupVersion.String()
	list, err := dc.ServerResourcesForGroupVersion(gv)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("the cluster does not serve %s%s; kubectl-dra needs Kubernetes 1.34 or newer", gv, otherResourceVersions(dc))
	}
	if err != nil {
		return nil, fmt.Errorf("discovering %s: %w", gv, err)
	}
	served := make(map[string]bool, len(list.APIResources))
	for _, r := range list.APIResources {
		served[r.Name] = true
	}
	return served, nil
}

// otherResourceVersions names the resource.k8s.io versions the server does
// serve, to make the "not served" error actionable.
func otherResourceVersions(dc discovery.DiscoveryInterface) string {
	groups, err := dc.ServerGroups()
	if err != nil {
		return ""
	}
	for _, g := range groups.Groups {
		if g.Name != resourceapi.GroupName {
			continue
		}
		var versions []string
		for _, v := range g.Versions {
			versions = append(versions, v.Version)
		}
		return fmt.Sprintf(" (it serves %s)", strings.Join(versions, ", "))
	}
	return " (DRA is not enabled)"
}

type loader struct {
	mu   sync.Mutex
	snap *Snapshot
}

// list pages through one resource and adds the items to the snapshot.
func (l *loader) list(ctx context.Context, g *errgroup.Group, resource string, required bool, fn pager.ListPageFunc) {
	g.Go(func() error {
		var items []runtime.Object
		err := pager.New(fn).EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
			items = append(items, obj)
			return nil
		})

		l.mu.Lock()
		defer l.mu.Unlock()
		switch {
		case err == nil:
			for _, obj := range items {
				l.snap.Add(obj)
			}
			return nil
		case apierrors.IsForbidden(err) && required:
			return fmt.Errorf("listing %s is forbidden; see docs/rbac.md for the permissions kubectl-dra needs: %w", resource, err)
		case (apierrors.IsForbidden(err) || apierrors.IsNotFound(err)) && !required:
			l.snap.Warnings = append(l.snap.Warnings, fmt.Sprintf("skipped %s: %v", resource, err))
			return nil
		default:
			return fmt.Errorf("listing %s: %w", resource, err)
		}
	})
}
