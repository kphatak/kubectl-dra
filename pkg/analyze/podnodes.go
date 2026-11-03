package analyze

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
)

// NodeFit is whether a pod can run on a node, ignoring resources.
type NodeFit struct {
	Node *corev1.Node
	// Reason is empty when the pod fits.
	Reason string
}

// PodNodes checks, for every node, whether the pod could be scheduled there
// as far as DRA is concerned: a bound pod fits only its node; otherwise the
// node must match the pod's nodeSelector and required node affinity,
// tolerate the node's NoSchedule and NoExecute taints, and not be cordoned.
// This is basic filtering, not the full scheduler:
// CPU, memory, ports, pod affinity and topology spread are not considered.
func PodNodes(pod *corev1.Pod, nodes []*corev1.Node) []NodeFit {
	affinity := nodeaffinity.GetRequiredNodeAffinity(pod)
	fits := make([]NodeFit, 0, len(nodes))
	for _, n := range nodes {
		fits = append(fits, NodeFit{Node: n, Reason: podNodeReason(pod, n, affinity)})
	}
	return fits
}

func podNodeReason(pod *corev1.Pod, node *corev1.Node, affinity nodeaffinity.RequiredNodeAffinity) string {
	if pod.Spec.NodeName != "" {
		if pod.Spec.NodeName != node.Name {
			return "pod is bound to " + pod.Spec.NodeName
		}
		return ""
	}
	if ok, err := affinity.Match(node); err != nil {
		return fmt.Sprintf("node affinity cannot be evaluated: %v", err)
	} else if !ok {
		return "does not match the pod's nodeSelector or node affinity"
	}
	if node.Spec.Unschedulable && !tolerates(pod, corev1.Taint{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule}) {
		return "node is cordoned"
	}
	taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(klog.Background(), node.Spec.Taints, pod.Spec.Tolerations, func(t *corev1.Taint) bool {
		return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
	}, false)
	if untolerated {
		return fmt.Sprintf("taint %s is not tolerated", taint.ToString())
	}
	return ""
}

func tolerates(pod *corev1.Pod, taint corev1.Taint) bool {
	return corev1helpers.TolerationsTolerateTaint(klog.Background(), pod.Spec.Tolerations, &taint, false)
}
