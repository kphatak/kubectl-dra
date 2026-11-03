package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"

	"github.com/kphatak/kubectl-dra/pkg/analyze"
	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

// whyList is the -o json|yaml schema.
type whyList struct {
	Explanations []*analyze.Explanation `json:"explanations"`
}

func newCmdWhy(o *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "why (CLAIM | resourceclaim/CLAIM | pod/POD)",
		Short: "Explain why a ResourceClaim, or a pod's claims, are not allocated",
		Long: `Explain why a ResourceClaim is not allocated, or why a pod is stuck on
its claims.

The first line is the verdict. For an unallocated claim, each request is
then run through a funnel that shows where candidate devices disappear:

  published                      devices in the newest generation of every pool
  in usable pools                minus pools that are incomplete or invalid
  match the class                minus devices the DeviceClass selectors reject
  match the request selectors    minus devices the request's selectors reject
  not allocated to other claims  minus devices in use (skipped for admin access)
  taints tolerated               minus devices with untolerated taints
  reachable from candidate nodes minus devices no candidate node can use

followed by the count check on a single node. Candidate nodes are where the
pod can run (nodeSelector, required node affinity, taints, cordons), or all
nodes for a claim without a pod.

Finally the upstream allocator, the same code the scheduler runs, tries the
claim on every candidate node. The verdict follows the allocator; the
funnel explains it.`,
		Example: `  # Why is this claim pending?
  kubectl dra why my-claim -n team-a

  # Why is this pod stuck on its claims?
  kubectl dra why pod/trainer-0 -n team-a

  # The same, for scripts
  kubectl dra why pod/trainer-0 -n team-a -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.allNamespaces {
				return fmt.Errorf("why explains one object; use -n instead of -A")
			}
			ns, err := o.namespace()
			if err != nil {
				return err
			}
			s, err := o.loadSnapshot(cmd.Context(), snapshot.LoadOptions{Namespace: ns, Events: true})
			if err != nil {
				return err
			}
			return runWhy(cmd.Context(), o, s, ns, args[0])
		},
	}
}

func runWhy(ctx context.Context, o *Options, s *snapshot.Snapshot, namespace, target string) error {
	kind, name, found := strings.Cut(target, "/")
	if !found {
		kind, name = "resourceclaim", target
	}

	var explanations []*analyze.Explanation
	switch strings.ToLower(kind) {
	case "pod", "pods", "po":
		pod := findPod(s, namespace, name)
		if pod == nil {
			return fmt.Errorf("pod %q not found in namespace %q", name, namespace)
		}
		if len(pod.Spec.ResourceClaims) == 0 {
			return fmt.Errorf("pod %s/%s has no resource claims", namespace, name)
		}
		m := analyze.MapClaims([]*corev1.Pod{pod}, namespaceClaims(s, namespace))
		for _, use := range m.Claims {
			// Only claims the pod references; reservedFor entries of
			// other pods show up as consumers too.
			for _, c := range use.Consumers {
				if c.Pod == pod {
					explanations = append(explanations, analyze.Explain(ctx, s, use.Claim, pod))
					break
				}
			}
		}
		for _, mc := range m.Missing {
			explanations = append(explanations, missingExplanation(mc))
		}
	case "resourceclaim", "resourceclaims":
		claim := findClaim(s, namespace, name)
		if claim == nil {
			return fmt.Errorf("ResourceClaim %q not found in namespace %q", name, namespace)
		}
		explanations = append(explanations, analyze.Explain(ctx, s, claim, claimPod(s, claim)))
	default:
		return fmt.Errorf("cannot explain %q: use a claim name, resourceclaim/NAME or pod/NAME", target)
	}

	switch o.output {
	case outputJSON, outputYAML:
		return printStructured(o.Out, o.output, whyList{Explanations: explanations})
	}
	for i, e := range explanations {
		if i > 0 {
			fmt.Fprintln(o.Out)
		}
		if err := printExplanation(o.Out, e); err != nil {
			return err
		}
	}
	return nil
}

// claimPod returns the first pod using the claim, whose node constraints
// then apply. Shared claims are allocated for the first pod scheduled.
func claimPod(s *snapshot.Snapshot, claim *resourceapi.ResourceClaim) *corev1.Pod {
	m := analyze.MapClaims(s.Pods, []*resourceapi.ResourceClaim{claim})
	for _, c := range m.Claims[0].Consumers {
		if c.Pod != nil {
			return c.Pod
		}
	}
	return nil
}

func missingExplanation(mc analyze.MissingClaim) *analyze.Explanation {
	e := &analyze.Explanation{
		Claim:   mc.Pod.Namespace + "/" + mc.ClaimName,
		Pod:     mc.Pod.Namespace + "/" + mc.Pod.Name,
		Verdict: analyze.VerdictNotAllocatable,
		Summary: fmt.Sprintf("pod claim %q has no ResourceClaim: %s", mc.PodClaim, mc.Reason),
	}
	if mc.ClaimName == "" {
		e.Claim = mc.Pod.Namespace + "/<from template " + mc.Template + ">"
		e.Hints = []string{fmt.Sprintf("The resource claim controller creates this claim. Check that ResourceClaimTemplate %s/%s exists: kubectl get resourceclaimtemplate -n %s %s",
			mc.Pod.Namespace, mc.Template, mc.Pod.Namespace, mc.Template)}
	} else {
		e.Hints = []string{fmt.Sprintf("Create ResourceClaim %s/%s, or fix spec.resourceClaims in the pod", mc.Pod.Namespace, mc.ClaimName)}
	}
	return e
}

func printExplanation(w io.Writer, e *analyze.Explanation) error {
	header := "Claim " + e.Claim
	if e.Pod != "" {
		header += ", for pod " + e.Pod
	}
	fmt.Fprintln(w, header)
	fmt.Fprintf(w, "%s: %s\n", e.Verdict, e.Summary)

	if len(e.Allocation) > 0 {
		fmt.Fprintln(w, "\nAllocated devices:")
		for _, r := range e.Allocation {
			fmt.Fprintf(w, "  %s: %s/%s/%s\n", r.Request, r.Driver, r.Pool, r.Device)
		}
	}
	if e.PodScheduled != "" || len(e.Events) > 0 {
		fmt.Fprintln(w, "\nScheduler:")
		if e.PodScheduled != "" {
			fmt.Fprintf(w, "  PodScheduled: %s\n", e.PodScheduled)
		}
		for _, ev := range e.Events {
			fmt.Fprintf(w, "  %s\n", ev)
		}
	}

	if e.Pod != "" && (len(e.CandidateNodes) > 0 || len(e.ExcludedNodes) > 0) {
		total := len(e.CandidateNodes)
		for _, d := range e.ExcludedNodes {
			total += d.Count
		}
		if len(e.Allocation) > 0 {
			fmt.Fprintf(w, "\nNodes where the claim's devices are usable: %d; the pod can run on %d\n", total, len(e.CandidateNodes))
		} else {
			fmt.Fprintf(w, "\nCandidate nodes (where the pod can run): %d of %d\n", len(e.CandidateNodes), total)
		}
		for _, d := range e.ExcludedNodes {
			fmt.Fprintf(w, "  -%d %s: %s\n", d.Count, d.Reason, strings.Join(d.Examples, ", "))
		}
	}

	for _, r := range e.Requests {
		for i, f := range r.Alternatives {
			fmt.Fprintln(w)
			printFunnel(w, r, i, f)
		}
	}

	if len(e.PoolWarnings) > 0 {
		fmt.Fprintln(w, "\nPool warnings:")
		for _, p := range e.PoolWarnings {
			fmt.Fprintf(w, "  - %s\n", p)
		}
	}
	if a := e.Allocator; a != nil {
		features := strings.Join(a.Features, ", ")
		if features == "" {
			features = "none"
		}
		fmt.Fprintf(w, "\nAllocator: allocation possible on %d of %d candidate nodes (features assumed: %s)\n", len(a.Nodes), a.NodesTried, features)
		for _, ne := range a.NodeErrors {
			fmt.Fprintf(w, "  %s\n", ne)
		}
	}
	if len(e.Hints) > 0 {
		fmt.Fprintln(w, "\nHints:")
		for _, h := range e.Hints {
			fmt.Fprintf(w, "  - %s\n", h)
		}
	}
	return nil
}

func printFunnel(w io.Writer, r analyze.RequestExplanation, index int, f analyze.Funnel) {
	what := fmt.Sprintf("exactly %d", f.Count)
	if f.AllocationMode == string(resourceapi.DeviceAllocationModeAll) {
		what = "all"
	}
	title := fmt.Sprintf("Request %q: %s from class %s", f.Request, what, f.Class)
	if len(r.Alternatives) > 1 {
		title = fmt.Sprintf("Request %q, alternative %d of %d: %s from class %s", r.Name, index+1, len(r.Alternatives), what, f.Class)
	}
	if f.AdminAccess {
		title += ", admin access"
	}
	status := "not satisfiable"
	if f.Satisfiable {
		status = fmt.Sprintf("satisfiable on %d node(s)", len(f.Nodes))
	}
	fmt.Fprintf(w, "%s (%s)\n", title, status)
	if f.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", f.Error)
	}
	if len(f.Stages) == 0 {
		fmt.Fprintf(w, "  %s\n", f.Problem)
		return
	}

	tw := newTable(w)
	for _, s := range f.Stages {
		fmt.Fprintf(tw, "  %s\t%d\n", s.Name, s.Remaining)
		for _, d := range s.Dropped {
			fmt.Fprintf(tw, "  \t-%d\t%s\n", d.Count, d.Reason)
			for _, ex := range d.Examples {
				fmt.Fprintf(tw, "  \t\t  e.g. %s\n", ex)
			}
		}
		if s.Remaining == 0 {
			break
		}
	}
	_ = tw.Flush()
	if f.Satisfiable {
		fmt.Fprintf(w, "  needs %d on one node; best node %s has %d\n", f.Need, f.BestNode, f.BestCount)
	} else if f.Problem != "" && f.Error == "" {
		fmt.Fprintf(w, "  %s\n", f.Problem)
	}
}

func findPod(s *snapshot.Snapshot, ns, name string) *corev1.Pod {
	for _, p := range s.Pods {
		if p.Namespace == ns && p.Name == name {
			return p
		}
	}
	return nil
}

func findClaim(s *snapshot.Snapshot, ns, name string) *resourceapi.ResourceClaim {
	for _, c := range s.ResourceClaims {
		if c.Namespace == ns && c.Name == name {
			return c
		}
	}
	return nil
}

func namespaceClaims(s *snapshot.Snapshot, ns string) []*resourceapi.ResourceClaim {
	var out []*resourceapi.ResourceClaim
	for _, c := range s.ResourceClaims {
		if c.Namespace == ns {
			out = append(out, c)
		}
	}
	return out
}
