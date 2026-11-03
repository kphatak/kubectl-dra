package analyze

import (
	"strings"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/utils/ptr"
)

// Holder is one claim's use of a device.
type Holder struct {
	Claim *resourceapi.ResourceClaim

	// Request is the request name from the claim spec. For a firstAvailable
	// request, SubRequest names the alternative that was chosen.
	Request    string
	SubRequest string

	// AdminAccess holders do not make a device unavailable to others.
	AdminAccess bool

	// ShareID is set when the device is shared through consumable capacity.
	ShareID *types.UID
}

// Allocations records which claims hold which devices.
type Allocations struct {
	// State is the allocated state the scheduler derives from the same
	// claims. It is the input structured.NewAllocator expects.
	State structured.AllocatedState

	holders map[structured.DeviceID][]Holder
}

// BuildAllocations collects device usage from every claim's status.allocation.
//
// It mirrors foreachAllocatedDevice in kube-scheduler's dynamicresources
// plugin: admin access results do not count as allocated, and results with a
// shareID count as shared only when consumableCapacity is on. Otherwise the
// device is dedicated to the claim.
//
// The scheduler also counts allocations it has decided on but not yet written
// to the API server. Those are invisible from outside, so a device can briefly
// look free here while it is being allocated.
func BuildAllocations(claims []*resourceapi.ResourceClaim, consumableCapacity bool) *Allocations {
	a := &Allocations{
		State: structured.AllocatedState{
			AllocatedDevices:         sets.New[structured.DeviceID](),
			AllocatedSharedDeviceIDs: sets.New[structured.SharedDeviceID](),
			AggregatedCapacity:       structured.NewConsumedCapacityCollection(),
		},
		holders: make(map[structured.DeviceID][]Holder),
	}

	for _, claim := range claims {
		if claim.Status.Allocation == nil {
			continue
		}
		for _, result := range claim.Status.Allocation.Devices.Results {
			id := structured.MakeDeviceID(result.Driver, result.Pool, result.Device)
			request, subRequest := splitRequestRef(result.Request)
			holder := Holder{
				Claim:       claim,
				Request:     request,
				SubRequest:  subRequest,
				AdminAccess: ptr.Deref(result.AdminAccess, false),
				ShareID:     result.ShareID,
			}
			a.holders[id] = append(a.holders[id], holder)

			switch {
			case holder.AdminAccess:
			case consumableCapacity && result.ShareID != nil:
				a.State.AllocatedSharedDeviceIDs.Insert(structured.MakeSharedDeviceID(id, result.ShareID))
				if result.ConsumedCapacity != nil {
					a.State.AggregatedCapacity.Insert(structured.NewDeviceConsumedCapacity(id, result.ConsumedCapacity))
				}
			default:
				a.State.AllocatedDevices.Insert(id)
			}
		}
	}
	return a
}

// Holders returns every claim using the device, including admin access.
func (a *Allocations) Holders(id structured.DeviceID) []Holder {
	return a.holders[id]
}

// InUse reports whether the scheduler considers the device allocated, either
// dedicated to one claim or partly consumed by shared claims.
func (a *Allocations) InUse(id structured.DeviceID) bool {
	return structured.IsDeviceAllocated(id, &a.State)
}

// splitRequestRef splits "request/subrequest", as written in allocation
// results for firstAvailable requests, into its parts.
func splitRequestRef(ref string) (request, subRequest string) {
	request = resourceclaim.BaseRequestRef(ref)
	if resourceclaim.IsSubRequestRef(ref) {
		subRequest = strings.TrimPrefix(ref, request+"/")
	}
	return request, subRequest
}
