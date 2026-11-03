package analyze

import (
	"errors"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
)

// Consumer is something using a claim: a pod that references it in its
// spec, or an entry in the claim's status.reservedFor, which can also be a
// PodGroup for workload claims.
type Consumer struct {
	// Kind is "pod" for pods, otherwise the reservedFor resource, such as
	// "podgroups".
	Kind string
	Name string
	UID  types.UID

	// Pod is set when the consumer is a pod present in the snapshot.
	Pod *corev1.Pod

	// Reserved reports whether the consumer is listed in
	// status.reservedFor, meaning the claim is in use by it now.
	Reserved bool
}

func (c Consumer) String() string { return c.Kind + "/" + c.Name }

// ClaimUse is a claim with everything that uses it.
type ClaimUse struct {
	Claim     *resourceapi.ResourceClaim
	Consumers []Consumer
}

// MissingClaim is a pod's claim reference with no usable ResourceClaim.
type MissingClaim struct {
	Pod *corev1.Pod

	// PodClaim is the entry name in pod.spec.resourceClaims.
	PodClaim string

	// ClaimName is the ResourceClaim the pod needs. It is empty when the
	// claim comes from Template and has not been created yet.
	ClaimName string
	Template  string

	Reason string
}

// ClaimMap links claims to their consumers.
type ClaimMap struct {
	Claims  []*ClaimUse
	Missing []MissingClaim
}

// MapClaims links every claim to the pods referencing it and to its
// reservedFor entries, and reports pod references without a claim.
//
// Pod references are resolved with resourceclaim.Name, so template-generated
// claim names come from pod.status.resourceClaimStatuses exactly as the
// scheduler reads them. A generated claim must also be owned by the pod (or
// its PodGroup), as resourceclaim.IsForPod checks; otherwise the pod's real
// claim does not exist yet.
func MapClaims(pods []*corev1.Pod, claims []*resourceapi.ResourceClaim) *ClaimMap {
	m := &ClaimMap{}
	byName := make(map[string]*ClaimUse, len(claims))
	for _, c := range claims {
		use := &ClaimUse{Claim: c}
		m.Claims = append(m.Claims, use)
		byName[c.Namespace+"/"+c.Name] = use
	}

	for _, pod := range pods {
		for i := range pod.Spec.ResourceClaims {
			podClaim := &pod.Spec.ResourceClaims[i]
			template := ""
			if podClaim.ResourceClaimTemplateName != nil {
				template = *podClaim.ResourceClaimTemplateName
			}
			missing := MissingClaim{Pod: pod, PodClaim: podClaim.Name, Template: template}

			name, mustCheckOwner, err := resourceclaim.Name(pod, podClaim)
			switch {
			case errors.Is(err, resourceclaim.ErrClaimNotFound):
				missing.Reason = fmt.Sprintf("not created yet from ResourceClaimTemplate %s", template)
				m.Missing = append(m.Missing, missing)
				continue
			case err != nil:
				missing.Reason = err.Error()
				m.Missing = append(m.Missing, missing)
				continue
			case name == nil:
				// The claim was intentionally not created; nothing to show.
				continue
			}

			missing.ClaimName = *name
			use, ok := byName[pod.Namespace+"/"+*name]
			if !ok {
				missing.Reason = "ResourceClaim not found"
				m.Missing = append(m.Missing, missing)
				continue
			}
			if mustCheckOwner {
				if err := resourceclaim.IsForPod(pod, use.Claim, true); err != nil {
					missing.Reason = err.Error()
					m.Missing = append(m.Missing, missing)
					continue
				}
			}
			use.addConsumer(Consumer{Kind: "pod", Name: pod.Name, UID: pod.UID, Pod: pod})
		}
	}

	podsByName := make(map[string]*corev1.Pod, len(pods))
	for _, p := range pods {
		podsByName[p.Namespace+"/"+p.Name] = p
	}
	for _, use := range m.Claims {
		for _, ref := range use.Claim.Status.ReservedFor {
			c := Consumer{Kind: ref.Resource, Name: ref.Name, UID: ref.UID, Reserved: true}
			if ref.APIGroup == "" && ref.Resource == "pods" {
				c.Kind = "pod"
				if p := podsByName[use.Claim.Namespace+"/"+ref.Name]; p != nil && p.UID == ref.UID {
					c.Pod = p
				}
			}
			use.addConsumer(c)
		}
		sort.SliceStable(use.Consumers, func(i, j int) bool {
			a, b := use.Consumers[i], use.Consumers[j]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			return a.Name < b.Name
		})
	}
	return m
}

// addConsumer adds c, merging it with an existing entry for the same object.
// Entries with the same name but different UIDs stay apart: a reservation
// left behind by a deleted pod must not make a new pod with the same name
// look reserved.
func (u *ClaimUse) addConsumer(c Consumer) {
	for i := range u.Consumers {
		existing := &u.Consumers[i]
		if existing.Kind != c.Kind || existing.Name != c.Name ||
			(existing.UID != "" && c.UID != "" && existing.UID != c.UID) {
			continue
		}
		if existing.UID == "" {
			existing.UID = c.UID
		}
		existing.Reserved = existing.Reserved || c.Reserved
		if existing.Pod == nil {
			existing.Pod = c.Pod
		}
		return
	}
	u.Consumers = append(u.Consumers, c)
}
