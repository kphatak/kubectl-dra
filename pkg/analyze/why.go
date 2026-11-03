package analyze

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/resourceclaim"

	"github.com/kphatak/kubectl-dra/pkg/celx"
	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

// Verdict is the one-word answer of an Explanation.
type Verdict string

const (
	VerdictAllocated           Verdict = "ALLOCATED"
	VerdictAllocatedPodPending Verdict = "ALLOCATED, POD PENDING"
	VerdictAllocatable         Verdict = "ALLOCATABLE"
	VerdictNotAllocatable      Verdict = "NOT ALLOCATABLE"
	VerdictError               Verdict = "ERROR"
)

// Funnel stage names, in order.
const (
	StagePublished = "published"
	StageUsable    = "in usable pools"
	StageClass     = "match the class"
	StageSelectors = "match the request selectors"
	StageFree      = "not allocated to other claims"
	StageTaints    = "taints tolerated"
	StageNodes     = "reachable from candidate nodes"
)

// maxExamples bounds the devices listed per drop reason.
const maxExamples = 3

// Explanation answers "why is this claim not allocated".
type Explanation struct {
	Claim   string  `json:"claim"`
	Pod     string  `json:"pod,omitempty"`
	Verdict Verdict `json:"verdict"`
	Summary string  `json:"summary"`

	// Allocation is set for allocated claims.
	Allocation []resourceapi.DeviceRequestAllocationResult `json:"allocation,omitempty"`
	// PodScheduled and Events explain a pending pod whose claim is
	// allocated: the scheduler's PodScheduled message and recent warnings.
	PodScheduled string   `json:"podScheduled,omitempty"`
	Events       []string `json:"events,omitempty"`

	// CandidateNodes are the nodes the claim may be allocated for: where
	// the pod can run, or every node for a claim without a pod.
	CandidateNodes []string `json:"candidateNodes,omitempty"`
	// ExcludedNodes says why other nodes are not candidates.
	ExcludedNodes []Drop `json:"excludedNodes,omitempty"`

	Requests     []RequestExplanation `json:"requests,omitempty"`
	PoolWarnings []string             `json:"poolWarnings,omitempty"`
	Allocator    *AllocatorCheck      `json:"allocator,omitempty"`
	Hints        []string             `json:"hints,omitempty"`
}

// RequestExplanation is the funnel for one request. An exactly request has
// one alternative; a firstAvailable request has one per subrequest, tried in
// order.
type RequestExplanation struct {
	Name         string   `json:"name"`
	Alternatives []Funnel `json:"alternatives"`
	Satisfiable  bool     `json:"satisfiable"`
}

// Funnel shows where candidate devices for one request disappear.
type Funnel struct {
	Request        string `json:"request"`
	Class          string `json:"class"`
	AllocationMode string `json:"allocationMode"`
	Count          int64  `json:"count,omitempty"`
	AdminAccess    bool   `json:"adminAccess,omitempty"`

	Stages []Stage `json:"stages,omitempty"`

	// Need is the number of devices needed on one node. For allocationMode
	// All it is the number of matching devices on BestNode.
	Need      int    `json:"need"`
	BestNode  string `json:"bestNode,omitempty"`
	BestCount int    `json:"bestCount"`
	// Nodes are where this request alone can be satisfied.
	Nodes       []string `json:"nodes"`
	Satisfiable bool     `json:"satisfiable"`
	Problem     string   `json:"problem,omitempty"`
	Error       string   `json:"error,omitempty"`

	// unusablePools are unusable pools with devices the class selects.
	unusablePools []string
}

// Stage is one filtering step: how many devices remain and why the others
// were dropped.
type Stage struct {
	Name      string `json:"name"`
	Remaining int    `json:"remaining"`
	Dropped   []Drop `json:"dropped,omitempty"`
}

// Drop groups devices (or nodes) dropped for the same reason.
type Drop struct {
	Reason   string   `json:"reason"`
	Count    int      `json:"count"`
	Examples []string `json:"examples,omitempty"`
}

// whyEnv is the precomputed cluster state shared by all funnels.
type whyEnv struct {
	ctx         context.Context
	snap        *snapshot.Snapshot
	pools       []*Pool
	devices     []*DeviceInfo
	allocations *Allocations
	cel         *celx.Evaluator
	candidates  []*corev1.Node
}

// Explain explains one claim. pod, when set, is the pod waiting for it; its
// node constraints limit the candidate nodes. Without a pod every node is a
// candidate.
func Explain(ctx context.Context, s *snapshot.Snapshot, claim *resourceapi.ResourceClaim, pod *corev1.Pod) *Explanation {
	e := &Explanation{Claim: claim.Namespace + "/" + claim.Name}
	if pod != nil {
		e.Pod = pod.Namespace + "/" + pod.Name
	}
	pools := Pools(s.ResourceSlices)
	allocations := BuildAllocations(s.ResourceClaims, true)
	env := &whyEnv{
		ctx:         ctx,
		snap:        s,
		pools:       pools,
		devices:     Devices(pools, allocations, s.DeviceTaintRules),
		allocations: allocations,
		cel:         celx.NewEvaluator(),
	}

	if claim.Status.Allocation != nil {
		explainAllocated(e, s, claim, pod)
		return e
	}

	// Candidate nodes.
	if pod == nil {
		env.candidates = s.Nodes
	} else {
		excluded := map[string]*Drop{}
		for _, fit := range PodNodes(pod, s.Nodes) {
			if fit.Reason == "" {
				env.candidates = append(env.candidates, fit.Node)
				continue
			}
			addDrop(excluded, fit.Reason, fit.Node.Name, "")
		}
		e.ExcludedNodes = sortedDrops(excluded)
	}
	for _, n := range env.candidates {
		e.CandidateNodes = append(e.CandidateNodes, n.Name)
	}

	// One funnel per request or subrequest.
	fatal := ""
	for _, req := range claim.Spec.Devices.Requests {
		re := RequestExplanation{Name: req.Name}
		switch {
		case req.Exactly != nil:
			x := req.Exactly
			re.Alternatives = append(re.Alternatives, env.funnel(req.Name, x.DeviceClassName, x.Selectors, x.AllocationMode, x.Count, x.AdminAccess != nil && *x.AdminAccess, x.Tolerations))
		default:
			for _, sub := range req.FirstAvailable {
				re.Alternatives = append(re.Alternatives, env.funnel(resourceclaim.CreateSubRequestRef(req.Name, sub.Name), sub.DeviceClassName, sub.Selectors, sub.AllocationMode, sub.Count, false, sub.Tolerations))
			}
		}
		for _, alt := range re.Alternatives {
			re.Satisfiable = re.Satisfiable || alt.Satisfiable
			if alt.Error != "" && fatal == "" {
				fatal = alt.Error
			}
		}
		e.Requests = append(e.Requests, re)
	}
	e.PoolWarnings = poolWarnings(env, e.Requests)

	if fatal != "" {
		e.Verdict = VerdictError
		e.Summary = fatal
		e.Hints = hints(e)
		return e
	}

	e.Allocator = CheckWithAllocator(ctx, claim, s.ResourceClaims, s.DeviceClasses, s.ResourceSlices, s.DeviceTaintRules, env.candidates)
	e.Verdict, e.Summary = verdict(e)
	e.Hints = hints(e)
	return e
}

// funnel runs one request or subrequest through the stages.
func (env *whyEnv) funnel(name, className string, selectors []resourceapi.DeviceSelector, mode resourceapi.DeviceAllocationMode,
	count int64, adminAccess bool, tolerations []resourceapi.DeviceToleration,
) Funnel {
	if mode == "" {
		mode = resourceapi.DeviceAllocationModeExactCount
	}
	if mode == resourceapi.DeviceAllocationModeExactCount && count == 0 {
		count = 1
	}
	f := Funnel{Request: name, Class: className, AllocationMode: string(mode), AdminAccess: adminAccess, Nodes: []string{}}
	if mode == resourceapi.DeviceAllocationModeExactCount {
		f.Count = count
	}

	var class *resourceapi.DeviceClass
	for _, c := range env.snap.DeviceClasses {
		if c.Name == className {
			class = c
		}
	}
	if class == nil {
		f.Problem = fmt.Sprintf("DeviceClass %q does not exist", className)
		return f
	}

	survivors := env.devices
	f.Stages = append(f.Stages, Stage{Name: StagePublished, Remaining: len(survivors)})

	filter := func(stage string, drop func(d *DeviceInfo) (reason, detail string, err error)) error {
		kept := survivors[:0:0]
		drops := map[string]*Drop{}
		for _, d := range survivors {
			reason, detail, err := drop(d)
			if err != nil {
				return err
			}
			if reason == "" {
				kept = append(kept, d)
				continue
			}
			addDrop(drops, reason, d.ID.String(), detail)
		}
		survivors = kept
		f.Stages = append(f.Stages, Stage{Name: stage, Remaining: len(survivors), Dropped: sortedDrops(drops)})
		return nil
	}

	// The allocator skips pools it cannot use and devices without node
	// selection it understands. Remember unusable pools the class would
	// select from, for the pool warnings.
	unusable := map[string]bool{}
	_ = filter(StageUsable, func(d *DeviceInfo) (string, string, error) {
		if !d.Pool.Usable() && !unusable[d.Pool.ID.String()] {
			if m, err := env.cel.Match(env.ctx, d.Slice.Spec.Driver, d.Device, class.Spec.Selectors); err == nil && m == nil {
				unusable[d.Pool.ID.String()] = true
				f.unusablePools = append(f.unusablePools, d.Pool.ID.String())
			}
		}
		switch {
		case len(d.Pool.Problems) > 0:
			return fmt.Sprintf("pool %s is invalid", d.Pool.ID), "", nil
		case !d.Pool.Complete():
			return fmt.Sprintf("pool %s is incomplete (%d of %d slices)", d.Pool.ID, len(d.Pool.Slices), d.Pool.ResourceSliceCount), "", nil
		case !d.Scope.Known():
			return "no node selection that this version understands", "", nil
		}
		return "", "", nil
	})

	selectorStage := func(stage, label string, sels []resourceapi.DeviceSelector, owner string) error {
		return filter(stage, func(d *DeviceInfo) (string, string, error) {
			m, err := env.cel.Match(env.ctx, d.Slice.Spec.Driver, d.Device, sels)
			if err != nil {
				return "", "", fmt.Errorf("%s %s on device %s: %w", owner, label, d.ID, err)
			}
			if m == nil {
				return "", "", nil
			}
			return fmt.Sprintf("%s #%d is false: %s", label, m.Index, oneLine(m.Expression)), celx.Values(m.Expression, d.Slice.Spec.Driver, d.Device), nil
		})
	}
	if err := selectorStage(StageClass, "class selector", class.Spec.Selectors, "DeviceClass "+class.Name); err != nil {
		f.Error = err.Error()
		return f
	}
	if err := selectorStage(StageSelectors, "selector", selectors, fmt.Sprintf("request %q", name)); err != nil {
		f.Error = err.Error()
		return f
	}
	// Devices that match regardless of availability; allocationMode All
	// needs every one of them on the node.
	matching := survivors

	if !adminAccess {
		_ = filter(StageFree, func(d *DeviceInfo) (string, string, error) {
			var dedicated, shared []string
			for _, h := range d.Holders {
				if h.AdminAccess {
					continue
				}
				owner := h.Claim.Namespace + "/" + h.Claim.Name
				if h.ShareID == nil {
					dedicated = append(dedicated, owner)
				} else {
					shared = append(shared, owner)
				}
			}
			switch {
			case len(dedicated) > 0:
				return "allocated to another claim", strings.Join(dedicated, ","), nil
			case len(shared) > 0 && (d.Device.AllowMultipleAllocations == nil || !*d.Device.AllowMultipleAllocations):
				return "allocated to another claim", strings.Join(shared, ","), nil
			}
			return "", "", nil
		})
	}

	_ = filter(StageTaints, func(d *DeviceInfo) (string, string, error) {
		for _, t := range d.Taints {
			if t.Blocks() && !tolerated(t, tolerations) {
				return "taint " + t.String() + " is not tolerated", "", nil
			}
		}
		return "", "", nil
	})

	// Which candidate nodes reach each surviving device.
	reach := func(devices []*DeviceInfo) (map[string]int, error) {
		perNode := map[string]int{}
		for _, d := range devices {
			for _, n := range env.candidates {
				ok, err := d.Scope.Matches(n)
				if err != nil {
					return nil, err
				}
				if ok {
					perNode[n.Name]++
				}
			}
		}
		return perNode, nil
	}
	reachable := map[*DeviceInfo]bool{}
	for _, d := range survivors {
		for _, n := range env.candidates {
			if ok, _ := d.Scope.Matches(n); ok {
				reachable[d] = true
				break
			}
		}
	}
	_ = filter(StageNodes, func(d *DeviceInfo) (string, string, error) {
		if reachable[d] {
			return "", "", nil
		}
		return "usable only on " + d.Scope.String() + ", which is not a candidate node", "", nil
	})

	have, err := reach(survivors)
	if err != nil {
		f.Error = err.Error()
		return f
	}

	if mode == resourceapi.DeviceAllocationModeAll {
		env.countAll(&f, matching, have)
	} else {
		f.Need = int(count)
		for _, n := range env.candidates {
			if have[n.Name] > f.BestCount {
				f.BestNode, f.BestCount = n.Name, have[n.Name]
			}
			if have[n.Name] >= f.Need {
				f.Nodes = append(f.Nodes, n.Name)
			}
		}
		f.Satisfiable = len(f.Nodes) > 0
	}
	if !f.Satisfiable && f.Problem == "" {
		f.Problem = funnelProblem(&f)
	}
	return f
}

// countAll applies allocationMode All: on a node, every matching device
// must be allocatable, there must be at least one, and every pool the node
// can see must be usable, or the allocator gives up on that node.
func (env *whyEnv) countAll(f *Funnel, matching []*DeviceInfo, have map[string]int) {
	var reasons []string
	for _, n := range env.candidates {
		need := 0
		for _, d := range matching {
			if ok, _ := d.Scope.Matches(n); ok {
				need++
			}
		}
		badPool := ""
		for _, p := range env.pools {
			if p.Usable() {
				continue
			}
			if ok, _ := p.ReachableFrom(n); ok {
				badPool = p.ID.String()
				break
			}
		}
		switch {
		case badPool != "":
			reasons = append(reasons, fmt.Sprintf("%s: pool %s is not usable, and all devices are requested", n.Name, badPool))
		case need == 0:
			reasons = append(reasons, n.Name+": no matching device")
		case have[n.Name] < need:
			reasons = append(reasons, fmt.Sprintf("%s: only %d of %d matching devices can be allocated", n.Name, have[n.Name], need))
		default:
			f.Nodes = append(f.Nodes, n.Name)
			if f.BestNode == "" {
				f.BestNode, f.BestCount, f.Need = n.Name, have[n.Name], need
			}
		}
	}
	f.Satisfiable = len(f.Nodes) > 0
	if !f.Satisfiable {
		if len(reasons) > 3 {
			reasons = append(reasons[:3], fmt.Sprintf("and %d more nodes", len(reasons)-3))
		}
		f.Problem = fmt.Sprintf("request %q asks for all matching devices, and no candidate node can provide them: %s", f.Request, strings.Join(reasons, "; "))
	}
}

// funnelProblem summarizes a failed funnel as a chain of stage counts, up
// to the stage where nothing is left or, if devices remain, the best node.
func funnelProblem(f *Funnel) string {
	plural := "devices"
	if f.Need == 1 {
		plural = "device"
	}
	var chain []string
	for _, s := range f.Stages {
		chain = append(chain, fmt.Sprintf("%d %s", s.Remaining, s.Name))
		if s.Remaining == 0 {
			return fmt.Sprintf("request %q needs %d %s: %s", f.Request, f.Need, plural, strings.Join(chain, ", "))
		}
	}
	best := "no candidate node reaches any of them"
	if f.BestNode != "" {
		best = fmt.Sprintf("the best node, %s, reaches %d", f.BestNode, f.BestCount)
	}
	return fmt.Sprintf("request %q needs %d %s on one node: %s, but %s", f.Request, f.Need, plural, strings.Join(chain, ", "), best)
}

// verdict combines the funnels with the allocator's answer. The allocator
// is the authority; the funnels explain it.
func verdict(e *Explanation) (Verdict, string) {
	if len(e.CandidateNodes) == 0 {
		return VerdictNotAllocatable, "the pod cannot run on any node"
	}

	// Nodes where every request can be satisfied on its own.
	var unsatisfied *RequestExplanation
	common := map[string]int{}
	for i := range e.Requests {
		r := &e.Requests[i]
		if !r.Satisfiable && unsatisfied == nil {
			unsatisfied = r
		}
		nodes := map[string]bool{}
		for _, alt := range r.Alternatives {
			for _, n := range alt.Nodes {
				nodes[n] = true
			}
		}
		for n := range nodes {
			common[n]++
		}
	}
	together := 0
	for _, c := range common {
		if c == len(e.Requests) {
			together++
		}
	}

	if len(e.Allocator.Nodes) > 0 {
		summary := fmt.Sprintf("the allocator finds devices on %d of %d candidate nodes (%s)",
			len(e.Allocator.Nodes), e.Allocator.NodesTried, strings.Join(firstN(e.Allocator.Nodes, 5), ", "))
		if n := len(e.Allocator.AbortingNodes); n > 0 {
			summary += fmt.Sprintf(", but fails with an error on %d (%s), which aborts scheduling if the scheduler evaluates them",
				n, strings.Join(firstN(e.Allocator.AbortingNodes, 5), ", "))
		}
		if unsatisfied != nil {
			summary += "; the funnel below is stricter than the allocator, please report this as a kubectl-dra bug"
		}
		return VerdictAllocatable, summary
	}
	switch {
	case unsatisfied != nil:
		// The funnel names the cause, such as a missing class, even when
		// the allocator only reports it as an error.
		return VerdictNotAllocatable, firstProblem(unsatisfied)
	case e.Allocator.Error != "":
		return VerdictError, "the allocator rejects the claim: " + e.Allocator.Error
	}
	if together == 0 {
		return VerdictNotAllocatable, "each request can be satisfied, but not on the same node"
	}
	return VerdictNotAllocatable, "each request can be satisfied on its own, but the allocator rejected every candidate node; " +
		"the reason is not decomposed (constraints across requests, or requests competing for the same devices)"
}

func firstProblem(r *RequestExplanation) string {
	if len(r.Alternatives) == 1 {
		return r.Alternatives[0].Problem
	}
	var parts []string
	for _, a := range r.Alternatives {
		parts = append(parts, a.Problem)
	}
	return fmt.Sprintf("no alternative of request %q works: %s", r.Name, strings.Join(parts, "; "))
}

// explainAllocated covers claims that already have devices.
func explainAllocated(e *Explanation, s *snapshot.Snapshot, claim *resourceapi.ResourceClaim, pod *corev1.Pod) {
	e.Allocation = claim.Status.Allocation.Devices.Results
	e.Verdict = VerdictAllocated
	e.Summary = fmt.Sprintf("%d device(s) allocated", len(e.Allocation))
	if pod == nil || pod.Spec.NodeName != "" {
		return
	}

	e.Verdict = VerdictAllocatedPodPending
	e.Summary = "the claim is allocated, but the pod is not scheduled"
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue && c.Message != "" {
			e.PodScheduled = c.Message
			e.Summary += ": " + c.Message
		}
	}
	e.Events = podWarnings(s.Events, pod)

	// The pod must run where the allocated devices are.
	scope := NodeScope{NodeSelector: claim.Status.Allocation.NodeSelector, AllNodes: claim.Status.Allocation.NodeSelector == nil}
	excluded := map[string]*Drop{}
	for _, fit := range PodNodes(pod, s.Nodes) {
		ok, err := scope.Matches(fit.Node)
		if err != nil || !ok {
			continue
		}
		if fit.Reason == "" {
			e.CandidateNodes = append(e.CandidateNodes, fit.Node.Name)
		} else {
			addDrop(excluded, fit.Reason, fit.Node.Name, "")
		}
	}
	e.ExcludedNodes = sortedDrops(excluded)
	if len(e.CandidateNodes) == 0 {
		e.Summary = fmt.Sprintf("the claim's devices are usable only on %s, and the pod cannot run there", scope.Describe(s.Nodes))
	}
	if !resourceclaim.IsReservedForPod(pod, claim, true) && !resourceclaim.CanBeReserved(claim) {
		e.Summary = fmt.Sprintf("the claim is reserved for the maximum of %d consumers, so the pod cannot use it", len(claim.Status.ReservedFor))
	}
	e.Hints = hints(e)
}

func podWarnings(events []*corev1.Event, pod *corev1.Pod) []string {
	var matched []*corev1.Event
	for _, ev := range events {
		if ev.Type == corev1.EventTypeWarning && ev.InvolvedObject.Kind == "Pod" &&
			ev.InvolvedObject.Namespace == pod.Namespace && ev.InvolvedObject.Name == pod.Name {
			matched = append(matched, ev)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].LastTimestamp.Before(&matched[j].LastTimestamp) })
	if len(matched) > 3 {
		matched = matched[len(matched)-3:]
	}
	var out []string
	for _, ev := range matched {
		out = append(out, fmt.Sprintf("%s: %s", ev.Reason, oneLine(ev.Message)))
	}
	return out
}

// poolWarnings lists unusable pools with devices that some request's class
// would select: the scheduler treats those pools as unavailable, which
// silently removes candidates.
func poolWarnings(env *whyEnv, requests []RequestExplanation) []string {
	relevant := map[string]bool{}
	for _, r := range requests {
		for _, alt := range r.Alternatives {
			for _, id := range alt.unusablePools {
				relevant[id] = true
			}
		}
	}
	var out []string
	for _, p := range env.pools {
		if p.Usable() || !relevant[p.ID.String()] {
			continue
		}
		status := fmt.Sprintf("incomplete (%d of %d slices)", len(p.Slices), p.ResourceSliceCount)
		if len(p.Problems) > 0 {
			status = "invalid: " + strings.Join(p.Problems, "; ")
		}
		out = append(out, fmt.Sprintf("pool %s has devices this claim could use but is %s; the scheduler does not allocate from it", p.ID, status))
	}
	return out
}

func tolerated(t DeviceTaint, tolerations []resourceapi.DeviceToleration) bool {
	for _, tol := range tolerations {
		if resourceclaim.ToleratesTaint(tol, t.DeviceTaint) {
			return true
		}
	}
	return false
}

func addDrop(drops map[string]*Drop, reason, item, detail string) {
	d := drops[reason]
	if d == nil {
		d = &Drop{Reason: reason}
		drops[reason] = d
	}
	d.Count++
	if len(d.Examples) < maxExamples {
		if detail != "" {
			item += " (" + detail + ")"
		}
		d.Examples = append(d.Examples, item)
	}
}

func sortedDrops(drops map[string]*Drop) []Drop {
	out := make([]Drop, 0, len(drops))
	for _, d := range drops {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(s[:n:n], fmt.Sprintf("and %d more", len(s)-n))
}

// hints suggests next steps, read-only commands first.
func hints(e *Explanation) []string {
	var h []string
	add := func(format string, args ...any) { h = append(h, fmt.Sprintf(format, args...)) }

	switch e.Verdict {
	case VerdictAllocated:
		return nil
	case VerdictAllocatedPodPending:
		if len(e.CandidateNodes) == 0 {
			add("The pod's nodeSelector, affinity or tolerations exclude the nodes where the claim's devices are; fix the pod, or delete the claim so it is allocated again")
		} else {
			add("The devices are usable where the pod can run; it is pending for another reason. Check: kubectl describe pod -n %s %s", podNS(e.Pod), podName(e.Pod))
		}
		return h
	}

	if e.Pod != "" && len(e.CandidateNodes) == 0 {
		add("No node passes the pod's nodeSelector, affinity, tolerations and cordons; see the excluded nodes above")
	}
	for _, r := range e.Requests {
		if r.Satisfiable {
			continue
		}
		for _, f := range r.Alternatives {
			if f.Satisfiable {
				continue
			}
			if f.Error != "" {
				add("Fix the CEL expression; the scheduler fails every claim that evaluates it. Inspect attributes with: kubectl dra devices -o wide")
				continue
			}
			if strings.HasPrefix(f.Problem, "DeviceClass") {
				add("Create DeviceClass %q, or fix deviceClassName in request %q. List classes with: kubectl get deviceclasses", f.Class, f.Request)
				continue
			}
			zero := ""
			for _, s := range f.Stages {
				if s.Remaining == 0 {
					zero = s.Name
					break
				}
			}
			switch zero {
			case StagePublished:
				add("No driver publishes devices. Check driver pods and nodes with: kubectl dra nodes --issues")
			case StageUsable:
				add("Every pool is unusable. See: kubectl dra pools -o wide")
			case StageClass:
				add("No device matches DeviceClass %q. Compare its selectors with: kubectl dra devices -o wide", f.Class)
			case StageSelectors:
				add("No device matches the selectors of request %q. Compare with: kubectl dra devices --class %s -o wide", f.Request, f.Class)
			case StageFree:
				add("Every matching device is in use. See holders with: kubectl dra devices --class %s", f.Class)
			case StageTaints:
				add("Every free matching device is tainted. Add a toleration to request %q, or remove the taint (DeviceTaintRules: kubectl get devicetaintrules)", f.Request)
			case StageNodes:
				add("The matching devices are on nodes the pod cannot use. Check the pod's nodeSelector, affinity and tolerations")
			default:
				add("Request %q needs more devices on one node than any node has; lower the count or free devices on %s", f.Request, f.BestNode)
			}
		}
	}
	if len(e.PoolWarnings) > 0 {
		add("Incomplete pools usually mean a driver is republishing or failing. Check: kubectl dra pools and kubectl dra nodes --issues")
	}
	return h
}

func podNS(ref string) string   { ns, _, _ := strings.Cut(ref, "/"); return ns }
func podName(ref string) string { _, n, _ := strings.Cut(ref, "/"); return n }
