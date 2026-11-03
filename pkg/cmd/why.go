package cmd

import (
	"context"
	"fmt"
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
	st := newStyle(o.Out)
	for i, e := range explanations {
		if i > 0 {
			fmt.Fprintf(o.Out, "\n%s\n\n", st.dim(strings.Repeat("─", 60)))
		}
		printExplanation(o.Out, e, st)
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
