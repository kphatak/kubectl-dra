# Design notes

Running log of decisions and of places where upstream behavior or API shape
surprised us. Newest entries go at the bottom of each section.

## Dependencies

- `k8s.io/api`, `apimachinery`, `client-go`, `cli-runtime` and
  `dynamic-resource-allocation` are pinned to the same release, currently
  **v0.37.2** (latest on 2026-11-02). Bump them together.
- Module path: `github.com/kphatak/kubectl-dra`.

## API version

- Only `resource.k8s.io/v1` is supported. Load fails with a clear message
  that names the versions the server does serve when v1 is missing.
- A v1beta2 fallback is deferred. client-go ships no conversion between
  `resource/v1beta2` and `resource/v1`, so the fallback would mean hand-written
  conversion or a parallel code path. Revisit if users on 1.32/1.33 ask.

## Snapshot

- One `snapshot.Snapshot` per command, loaded with parallel paged `list` calls
  (`client-go/tools/pager`). Analyzers never call the API.
- **ResourceClaims are always listed cluster-wide**, even with `-n`, because
  allocated device state is cluster-wide. Pods, events and claim templates follow
  `-n` / `-A`.
- Required resources fail the load; optional ones (claim templates,
  DeviceTaintRules, events) become `Snapshot.Warnings`, which commands print to
  stderr. Partial data must never look complete.
- DeviceTaintRules are only listed if discovery says the server serves them in
  v1. They are behind their own feature gate.
- `snapshot.LoadFiles` reads multi-document YAML, including `kind: List`. Test
  fixtures use it, and the hidden `kubectl dra dump -o yaml` writes a file it
  can read back. A natural follow-up is a global flag to run any command against
  a dump offline (useful for support and bug reports).

## Newer API fields in v0.37.2

Verified in `k8s.io/api/resource/v1/types.go` and
`k8s.io/dynamic-resource-allocation`:

- `DeviceAttribute` now has list values (`ints`, `bools`, `strings`,
  `versions`) besides the scalar `int`, `bool`, `string`, `version`. The CEL
  compiler gates these with `cel.Features.EnableListTypeAttributes`.
- `DeviceConstraint` has `distinctAttribute` alongside `matchAttribute`. The
  `why` constraint step must handle both.
- `ExactDeviceRequest` and `DeviceSubRequest` have `capacity` (consumable
  capacity) and `derivedAttributes`.
- `Device` has `bindsToNode`, `bindingConditions`,
  `bindingFailureConditions`, `allowMultipleAllocations` and
  `nodeAllocatableResources`. `ResourceSliceSpec` has
  `partitionTypeAttribute` and `skipNodeOperations`.
- `DeviceTaintRule` is served in `resource.k8s.io/v1`.
- Claims can be reserved for a **PodGroup** as well as a pod
  (`resourceclaim.IsForPodGroup`, `IsReservedForPod(..., acceptPodGroupReservation)`).
  `map` and `why` must not assume every `reservedFor` entry is a pod.

## Allocator inputs

`structured.NewAllocator(ctx, features, allocatedState, classLister, slices, celCache)`:

- `features structured.Features`: AdminAccess, CompatibilityGroups,
  ConsumableCapacity, DerivedAttributes, DeviceBindingAndStatus, DeviceTaints,
  FractionalCapacityRange, ListTypeAttributes, OptionalNodeOperations,
  PartitionableDevices, PrioritizedList. NewAllocator picks the most stable
  implementation (stable, incubating, experimental) that supports every
  enabled feature, so enabling features we do not need may route us to less
  stable code. The feature set must mirror what the scheduler has on, which
  we cannot read directly; we will infer it from which fields appear in the
  snapshot.
- `allocatedState structured.AllocatedState`: `AllocatedDevices`
  (set of `DeviceID`), `AllocatedSharedDeviceIDs`, `AggregatedCapacity`.
  Build it from every claim's `status.allocation`.
- `classLister`: interface with `List()` and `Get(name)` over DeviceClasses.
  A small in-memory implementation over the snapshot is enough.
- `slices []*resourceapi.ResourceSlice`. The scheduler feeds slices through
  `resourceslice/tracker`, which applies DeviceTaintRules to devices. That
  tracker wants informers, so to match the scheduler we will need to apply
  DeviceTaintRules to slices ourselves before calling the allocator.
- `celCache *cel.Cache` from `cel.NewCache(max, cel.Features{...})`.

`Allocate(ctx, node, claims)` returns `nil, nil` when the claims do not fit and
an error only for fatal input problems such as bad CEL.

Useful exported helpers: `structured.MakeDeviceID`, `structured.NodeMatches`,
`structured.IsDeviceAllocated`, `resourceclaim.Name(pod, podClaim)` (resolves
template-generated claim names), `resourceclaim.ToleratesTaint`,
`resourceclaim.BaseRequestRef` / `IsSubRequestRef` (for `request/subrequest`
names in allocation results), and `cel.CompilationResult.DeviceMatches` for
per-device CEL evaluation in the `why` funnel.

## Analyzers

- **Pools** (`analyze.Pools`). The upstream allocator gathers pools per node
  (`GatherPools`), falling back to all slices of the pool for the completeness
  check. We evaluate each pool once, cluster-wide: the highest generation wins,
  older slices are counted as outdated and ignored, and the pool is complete
  when the number of slices at that generation equals `resourceSliceCount`.
  Duplicate device names make the pool invalid, as in upstream
  `validateDeviceNames`. Slices at one generation that disagree on
  `resourceSliceCount` are flagged: upstream takes the count from whichever
  slice it sees first, so its answer depends on ordering.
- Not mirrored yet, because it depends on scheduler feature gates: upstream
  drops slices with shared counters or per-device node selection when
  `PartitionableDevices` is off, drops slices using compatibility groups when
  `CompatibilityGroups` is off (which makes their pool incomplete), and
  validates counter sets. These belong with feature inference in `why`.
- **Allocated state** (`analyze.BuildAllocations`) copies
  `foreachAllocatedDevice` from kube-scheduler's dynamicresources plugin,
  checked against the v1.37.2 source: admin access results are not allocated,
  results with a `shareID` are shared only when consumable capacity is on, and
  everything else is dedicated. It also keeps every holder per device
  (including admin access) for display. In-flight allocations that the
  scheduler has not written yet are invisible to us.
- **Taints** (`analyze.TaintsOf`) are the device's own plus those from
  DeviceTaintRules, applied as the scheduler's `resourceslice/tracker` does:
  a rule without `deviceSelector` matches nothing, and each set field of the
  selector (driver, pool, device) must match exactly. Only `NoSchedule` and
  `NoExecute` block allocation (`taintPreventsAllocation` in the allocator);
  `None` and unknown effects are ignored.
- **CEL** (`pkg/celx`) uses `cel.Cache` from the upstream package with the
  same inputs as the stable allocator's `selectorsMatch` (driver, attributes,
  capacity), evaluates selectors in order, stops at the first false one, and
  treats compile and runtime errors as errors, not non-matches. Runtime errors
  are easy to hit: a selector reading an attribute some devices lack fails
  with "no such key" unless guarded. List-type attributes and consumable
  capacity are enabled in the CEL environment so such devices evaluate instead
  of erroring; the scheduler may have them off.
- **Node scope** (`analyze.ScopeOf`) takes slice-level node selection, or the
  device's own when `perDeviceNodeSelection` is set, and matches nodes with
  `structured.NodeMatches`, so the result agrees with the allocator.

## Claim linkage

- `analyze.MapClaims` resolves pod references with `resourceclaim.Name`, so
  template-generated names come from `pod.status.resourceClaimStatuses` as
  the scheduler reads them. A nil name means the claim was intentionally not
  created and the reference is ignored. Generated claims must pass
  `resourceclaim.IsForPod` (pod or PodGroup owner); otherwise the pod's real
  claim does not exist yet.
- Consumers are the union of pods referencing a claim and the claim's
  `status.reservedFor` entries, so PodGroup reservations show up even though
  we do not list PodGroups. Entries merge by kind and name only when UIDs
  agree, so a reservation left by a deleted pod never makes a recreated pod
  with the same name look reserved.
- `map` shows the pod's node when it is bound, otherwise where the
  allocation's node selector points (one node, "all nodes", or the
  selector).

## E2E

- `make e2e` runs `test/e2e/run.sh`: kind (pinned in the Makefile, built into
  `bin/`) with one control plane and two workers, dra-example-driver
  (pinned `DRIVER_VERSION`, chart from a shallow clone of its tag, published
  image from registry.k8s.io), then the Go tests in `test/e2e` behind the
  `e2e` build tag. `E2E_KEEP=1` keeps the cluster; `make e2e-up`,
  `e2e-test` and `e2e-down` run one phase each.
- The cluster gets its own kubeconfig in `.e2e/kubeconfig`. The script never
  writes to the user's kubeconfig or switches their context.
- Workloads: a pod with a template claim for one GPU, a pod with a
  standalone two-GPU claim, a claim naming a missing DeviceClass, a claim
  whose selector matches nothing, a hand-made ResourceSlice for an
  incomplete pool, and a DeviceTaintRule on `gpu-7` (applied only when
  DeviceTaintRules are served in v1; the tests skip taint checks otherwise).
- The tests read cluster state through `kubectl dra dump -o yaml` and
  `snapshot.LoadFiles`, so the loader and the List round trip are exercised
  against a real API server on every run.
- Feature gates depend on the Kubernetes version, because a component given
  a gate it does not know refuses to start (the 1.34 kubelet panics on
  `DRADeviceTaintRules`, which first exists in 1.35). `run.sh` reads the
  version from the `KIND_IMAGE` tag and appends gates to the base config.
- Verified locally on 2026-11-02: all e2e tests pass on 1.37.0 (taint checks
  included) and 1.34.11 (taint checks skipped, not served).
- `KIND_IMAGE` overrides the node image. Docker Hub pulls can fail locally
  (expired stored credentials, rate limits); `mirror.gcr.io/kindest/node`
  serves the same digests anonymously.
- CI (`.github/workflows/ci.yaml`) runs vet, lint, unit tests, and e2e on
  the newest and oldest Kubernetes that serve `resource.k8s.io/v1`.

## Next steps

In order. Check items off here as they land.

1. ~~**Tooling.**~~ Done. `make lint` builds golangci-lint
   (pinned in the Makefile) from source with the local Go into `bin/`, because
   prebuilt binaries made with an older Go cannot read Go 1.27 export data. The
   Makefile unexports `GOROOT` so a stale value from another Go install cannot
   break builds.
2. ~~**`pkg/analyze` basics.**~~ Done: `Pools`, `BuildAllocations`, `ScopeOf`.
   See "Analyzers" below.
3. ~~**`kubectl dra pools`.**~~ Done. Statuses are Complete, Incomplete and
   Invalid (duplicate device names, or slices disagreeing on
   `resourceSliceCount`); `-o wide` adds outdated slices and problem text;
   `--driver` and `--node` filter. It loads with `SkipWorkloads`, so it never
   lists pods. Golden files live in `pkg/cmd/testdata`; regenerate with
   `go test ./pkg/cmd -update` and review the diff.
4. ~~**`kubectl dra devices`.**~~ Done. Statuses Free, Allocated, Shared,
   Tainted, Unavailable (see `analyze.Devices` for precedence); `-o wide` adds
   taints, reason and attributes; `--node`, `--driver`, `--class`, `--free`
   filter. A CEL error in `--class` fails the command, because the scheduler
   fails every claim using that class the same way.
5. ~~**kind e2e.**~~ Done; see "E2E" below.
6. ~~**`kubectl dra map`.**~~ Done. One row per consumer and allocated
   device, or per consumer and request while pending. Statuses Allocated,
   Pending, Missing (pod names a claim that does not exist) and NotCreated
   (template claim not generated yet). See "Claim linkage" below.
7. **`kubectl dra nodes`.** Per-node totals, plus the "should publish but does
   not" heuristic (driver DaemonSet pod present, no slices).
8. **`kubectl dra why`.** The planned funnel, then allocator
   confirmation. Prerequisites: apply DeviceTaintRules to slices ourselves
   (the scheduler does this in `resourceslice/tracker`), infer
   `structured.Features` from fields present in the snapshot, and handle
   `distinctAttribute` alongside `matchAttribute`. Cover every scenario in the
   planned unit test list with fixtures.
9. **Release.** LICENSE (Apache-2.0), CONTRIBUTING, CODE_OF_CONDUCT,
   SECURITY, issue templates, goreleaser, `.krew.yaml`, README with demo and
   RBAC, then krew-index submission after testing on a real GPU cluster.

Open questions:

- Expose offline analysis as a global flag (for example
  `--from-file dump.yaml`) so any command can run against a `dump`?
- Make `dump` a visible, documented command for bug reports?
- v1beta2 fallback: only if users on 1.32 or 1.33 ask.
