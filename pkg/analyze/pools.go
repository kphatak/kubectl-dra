// Package analyze holds pure functions over a snapshot. Nothing here calls
// the API server, so every analyzer can be tested from fixtures.
package analyze

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/structured"
)

// PoolID identifies a pool. Pool names are only unique per driver.
type PoolID struct {
	Driver string
	Pool   string
}

func (id PoolID) String() string { return id.Driver + "/" + id.Pool }

// Pool is the current state of one pool across the whole cluster.
//
// The scheduler gathers pools per node, but completeness is a property of the
// pool: it only allocates from a pool once every slice of the newest
// generation is visible, and treats the pool as unavailable otherwise.
type Pool struct {
	ID PoolID

	// Generation is the highest generation published for the pool. Slices
	// from older generations are ignored by the scheduler.
	Generation int64

	// Slices holds the slices at Generation, sorted by name.
	Slices []*resourceapi.ResourceSlice

	// OutdatedSlices counts slices from older generations. A non-zero
	// count is normal for a moment while a driver republishes the pool.
	OutdatedSlices int

	// ResourceSliceCount is the number of slices that the slices at
	// Generation say the pool has. It is zero when they disagree, which is
	// then listed in Problems.
	ResourceSliceCount int64

	// Problems explains, besides incompleteness, why the scheduler may
	// refuse to allocate from the pool.
	Problems []string
}

// Complete reports whether every slice of the current generation is present.
func (p *Pool) Complete() bool {
	return p.ResourceSliceCount > 0 && int64(len(p.Slices)) == p.ResourceSliceCount
}

// Usable reports whether the scheduler can allocate from the pool.
func (p *Pool) Usable() bool {
	return p.Complete() && len(p.Problems) == 0
}

// DeviceRef points at one device of a pool and the slice publishing it.
type DeviceRef struct {
	ID     structured.DeviceID
	Slice  *resourceapi.ResourceSlice
	Device *resourceapi.Device
}

// Devices returns the devices of the current generation, in slice order.
func (p *Pool) Devices() []DeviceRef {
	var refs []DeviceRef
	for _, s := range p.Slices {
		for i := range s.Spec.Devices {
			d := &s.Spec.Devices[i]
			refs = append(refs, DeviceRef{
				ID:     structured.MakeDeviceID(s.Spec.Driver, s.Spec.Pool.Name, d.Name),
				Slice:  s,
				Device: d,
			})
		}
	}
	// Drivers publish devices in any order; list them as people count.
	sort.SliceStable(refs, func(i, j int) bool { return NaturalLess(refs[i].Device.Name, refs[j].Device.Name) })
	return refs
}

// NaturalLess orders strings with embedded numbers by value, so gpu-2 comes
// before gpu-10. Runs of digits compare as numbers, everything else byte by
// byte.
func NaturalLess(a, b string) bool {
	for a != "" && b != "" {
		ca, cb := a[0], b[0]
		if isDigit(ca) && isDigit(cb) {
			na, nb := digitRun(a), digitRun(b)
			// Compare by value: strip leading zeros, then length, then
			// digits.
			va, vb := strings.TrimLeft(a[:na], "0"), strings.TrimLeft(b[:nb], "0")
			if len(va) != len(vb) {
				return len(va) < len(vb)
			}
			if va != vb {
				return va < vb
			}
			if na != nb {
				return na < nb
			}
			a, b = a[na:], b[nb:]
			continue
		}
		if ca != cb {
			return ca < cb
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digitRun(s string) int {
	n := 0
	for n < len(s) && isDigit(s[n]) {
		n++
	}
	return n
}

// ScopeSummary describes where the pool's devices are usable: their common
// node scope, or "mixed (N)" when devices differ, as with per-device node
// selection.
func (p *Pool) ScopeSummary() string {
	var scopes []string
	for _, s := range p.scopes() {
		if str := s.String(); !slices.Contains(scopes, str) {
			scopes = append(scopes, str)
		}
	}
	switch len(scopes) {
	case 0:
		return ""
	case 1:
		return scopes[0]
	default:
		return fmt.Sprintf("mixed (%d)", len(scopes))
	}
}

// ReachableFrom reports whether any device of the pool is usable on node.
func (p *Pool) ReachableFrom(node *corev1.Node) (bool, error) {
	for _, s := range p.scopes() {
		ok, err := s.Matches(node)
		if ok || err != nil {
			return ok, err
		}
	}
	return false, nil
}

// scopes returns the node scope of every device. A pool without devices
// (for example one that only publishes shared counters) falls back to the
// slice-level scope.
func (p *Pool) scopes() []NodeScope {
	var scopes []NodeScope
	for _, d := range p.Devices() {
		scopes = append(scopes, ScopeOf(d.Slice, d.Device))
	}
	if len(scopes) == 0 {
		for _, s := range p.Slices {
			scopes = append(scopes, ScopeOf(s, &resourceapi.Device{}))
		}
	}
	return scopes
}

// Pools groups slices into pools, sorted by driver and pool name.
func Pools(resourceSlices []*resourceapi.ResourceSlice) []*Pool {
	byID := make(map[PoolID][]*resourceapi.ResourceSlice)
	for _, s := range resourceSlices {
		id := PoolID{Driver: s.Spec.Driver, Pool: s.Spec.Pool.Name}
		byID[id] = append(byID[id], s)
	}

	pools := make([]*Pool, 0, len(byID))
	for id, poolSlices := range byID {
		pools = append(pools, newPool(id, poolSlices))
	}
	sort.Slice(pools, func(i, j int) bool {
		if pools[i].ID.Driver != pools[j].ID.Driver {
			return pools[i].ID.Driver < pools[j].ID.Driver
		}
		return pools[i].ID.Pool < pools[j].ID.Pool
	})
	return pools
}

func newPool(id PoolID, poolSlices []*resourceapi.ResourceSlice) *Pool {
	p := &Pool{ID: id, Generation: poolSlices[0].Spec.Pool.Generation}
	for _, s := range poolSlices {
		p.Generation = max(p.Generation, s.Spec.Pool.Generation)
	}

	var counts []int64
	for _, s := range poolSlices {
		if s.Spec.Pool.Generation < p.Generation {
			p.OutdatedSlices++
			continue
		}
		p.Slices = append(p.Slices, s)
		if !slices.Contains(counts, s.Spec.Pool.ResourceSliceCount) {
			counts = append(counts, s.Spec.Pool.ResourceSliceCount)
		}
	}
	sort.Slice(p.Slices, func(i, j int) bool { return p.Slices[i].Name < p.Slices[j].Name })

	// The scheduler takes the count from whichever slice it sees first, so
	// disagreement makes its view of the pool depend on ordering.
	if len(counts) == 1 {
		p.ResourceSliceCount = counts[0]
	} else {
		slices.Sort(counts)
		p.Problems = append(p.Problems, fmt.Sprintf(
			"slices at generation %d disagree on resourceSliceCount (%s), so the scheduler's view depends on slice order",
			p.Generation, joinInts(counts)))
	}

	// Matches validateDeviceNames in the upstream allocator, which marks
	// the whole pool invalid.
	publishedBy := make(map[string]string)
	reported := make(map[string]bool)
	for _, s := range p.Slices {
		for _, d := range s.Spec.Devices {
			prev, dup := publishedBy[d.Name]
			if !dup {
				publishedBy[d.Name] = s.Name
				continue
			}
			if !reported[d.Name] {
				reported[d.Name] = true
				p.Problems = append(p.Problems, fmt.Sprintf(
					"device name %q is published by both %s and %s, which makes the pool invalid", d.Name, prev, s.Name))
			}
		}
	}
	return p
}

func joinInts(values []int64) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}
