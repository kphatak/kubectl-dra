# Glossary

Terms used in kubectl-dra's output and documentation. The first part covers
Kubernetes Dynamic Resource Allocation itself; the second covers terms
kubectl-dra introduces. Each part is in alphabetical order.

- [Kubernetes DRA terms](#kubernetes-dra-terms)
- [kubectl-dra terms](#kubectl-dra-terms)

## Kubernetes DRA terms

**Admin access**: A request flag (`adminAccess: true`) for monitoring and diagnostic tools.
A claim with admin access gets devices even when other claims hold them,
and does not make them unavailable to anyone else. The namespace must allow
it.

**Allocation**: The devices the scheduler picked for a claim, written to
`status.allocation` of the ResourceClaim: one result per device (request,
driver, pool, device) and a node selector saying where the devices can be
used. A claim is allocated when the first pod using it is scheduled.

**Allocation mode**: How many devices a request asks for. `ExactCount` (the default) asks for
`count` devices, 1 unless set. `All` asks for every matching device on the
node, and at least one must exist.

**Allocator**: The code in `k8s.io/dynamic-resource-allocation/structured` that picks
devices for claims on a given node. The scheduler runs it during scheduling;
`kubectl dra why` runs the same code to confirm its answer.

**Attribute**: A named, typed property a driver publishes for a device, such as
`model: "A100"` or `index: 0`. Types are int, bool, string and version, and
lists of those. Selectors read them as `device.attributes["domain"].name`.

**Binding conditions**: Conditions a device can list that must be met before a pod using it is
bound to a node, for devices that need preparation outside the node, such as
attaching over a fabric.

**Capacity**: A named quantity a driver publishes for a device, such as
`memory: 80Gi`. Selectors read it as `device.capacity["domain"].name`.

**CEL**: Common Expression Language, the language of DRA selectors. An expression
receives one `device` and must return true or false, for example
`device.attributes["gpu.example.com"].model == "A100"`.

**Claim**: Short for ResourceClaim.

**Consumable capacity**: A feature that lets several claims share one device, each taking part of
its capacity (for example bandwidth on a NIC). Each share is recorded in the
allocation with a `shareID`. The device opts in with
`allowMultipleAllocations: true`.

**Constraint**: A rule across requests of one claim. `matchAttribute` requires all devices
of the listed requests to have the same value for an attribute (for
example, the same NUMA node). `distinctAttribute` requires different values.

**Count**: The number of devices an `ExactCount` request asks for.

**Device**: One allocatable unit a driver publishes in a ResourceSlice: a GPU, a GPU
partition, a NIC, a storage volume. It has a name, attributes, capacity, and
optionally taints.

**Device ID**: The triple `driver/pool/device`, for example
`gpu.example.com/node-a/gpu-0`. It identifies a device uniquely in the
cluster; device names alone are only unique within a pool.

**DeviceClass**: A cluster-wide, named category of devices, defined by CEL selectors (and
optional configuration). Every request names one. A device must pass the
class's selectors before the request's own selectors are applied.

**DeviceTaintRule**: An object that adds a taint to every device matching its selector (driver,
pool and device name, each optional). Administrators use it to take devices
out of service without changing the driver. A rule without a selector
matches nothing.

**Domain**: The prefix of an attribute or capacity name, usually the driver's name,
as in `gpu.example.com/model`. Names published without a domain belong to
the driver's domain.

**DRA (Dynamic Resource Allocation)**: The Kubernetes API for requesting and sharing devices such as GPUs,
generalizing persistent volumes to arbitrary hardware. Generally available
since Kubernetes 1.34 as `resource.k8s.io/v1`.

**DRA driver**: The component that knows a kind of hardware. It publishes ResourceSlices
describing the devices, and prepares devices for pods on the node through
its kubelet plugin.

**Feature gate**: A switch in Kubernetes components that turns a feature on or off. Several
DRA features (taint rules, consumable capacity, partitionable devices,
binding conditions) are behind gates, which change how the scheduler
allocates.

**firstAvailable (prioritized list)**: A request written as an ordered list of alternatives (subrequests), for
example "one H100, or else two A100s". The scheduler uses the first
alternative that can be satisfied. Results name the alternative as
`request/subrequest`, such as `gpu/large`.

**Generation (pool generation)**: A number in every ResourceSlice of a pool. A driver republishes a pool by
writing slices with a higher generation. Only slices of the highest
generation count; older ones are ignored.

**Kubelet plugin**: The part of a DRA driver that runs on each node, usually as a DaemonSet.
It registers with the kubelet through the `plugins_registry` directory,
prepares devices for pods, and publishes the node's ResourceSlices.

**Node selection**: Where a device can be used, set on a slice (or on each device with
`perDeviceNodeSelection`): `nodeName` for one node, `allNodes` for every
node, or `nodeSelector` for nodes matching labels or fields.

**Partitionable devices**: A feature for devices that can be split, such as GPU partitions. Several
devices draw on a shared pool of counters (for example memory slices), so
allocating one partition reduces what the others can use.

**Pool**: A group of devices one driver manages together, published as one or more
ResourceSlices with the same pool name. Node-local pools are usually named
after their node. Pool names are unique per driver.

**PodGroup**: A group of pods scheduled together (workload-aware scheduling). A claim
can be reserved for a whole PodGroup instead of a single pod.

**Request**: One entry in a claim's `spec.devices.requests`: a DeviceClass, optional
selectors, an allocation mode and count (`exactly`), or a list of
alternatives (`firstAvailable`).

**reservedFor**: The list in a claim's status of the consumers (pods, or a PodGroup)
currently using it. A claim can be shared by up to 256 consumers.

**resource.k8s.io/v1**: The API group and version of the DRA objects. Served from Kubernetes 1.34;
earlier releases had only beta versions (`v1beta1`, `v1beta2`).

**ResourceClaim**: A namespaced request for devices. It holds the requests and constraints
in its spec, and the allocation and `reservedFor` in its status.

**ResourceClaimTemplate**: A template from which a controller creates one ResourceClaim per pod. The
generated claim's name appears in the pod's
`status.resourceClaimStatuses`.

**resourceSliceCount**: The number of slices a pool has at its current generation, written in
every slice of the pool. A pool is complete when that many slices are
visible.

**ResourceSlice**: An object a DRA driver publishes to describe devices: the driver, the pool
(name, generation, slice count), where the devices can be used, and the
devices themselves.

**Selector**: A CEL expression that filters devices. A DeviceClass has selectors, and so
can each request. All selectors must be true for a device to qualify.

**Shared counters**: See partitionable devices.

**Subrequest**: One alternative of a `firstAvailable` request.

**Taint**: A key, value and effect attached to a device, by its driver or by a
DeviceTaintRule. `NoSchedule` keeps new claims off the device unless they
tolerate it; `NoExecute` also evicts pods already using it; `None` only
informs.

**Toleration**: A request's permission to use a device with a matching taint.

## kubectl-dra terms

**Aborting node**: A candidate node where the allocator returned an error rather than "no",
for example an `All` request next to an incomplete pool. If the scheduler
evaluates such a node for the pod, it aborts the whole scheduling attempt.
See [why](commands/why.md#4-the-allocator-check).

**Candidate node**: A node where a claim could be allocated: for a pod, nodes it can be
scheduled on (its bound node, or nodes matching its node selector and
affinity, tolerating its taints, and not cordoned); for a claim without a
pod, every node. See [why](commands/why.md#1-candidate-nodes).

**Consumer**: Something using a claim: a pod that references it, or an entry in its
`reservedFor`, such as a PodGroup. See [map](commands/map.md).

**Device status**: The one-word state `devices` shows for each device: `Free`, `Allocated`,
`Shared`, `Tainted` or `Unavailable`. See
[devices](commands/devices.md#reading-the-output).

**Dump**: The hidden command `kubectl dra dump -o yaml`, which writes everything
kubectl-dra reads as one file, for bug reports and test fixtures.

**Features assumed**: The allocator features `why` turns on because some object in the cluster
uses them. The scheduler's real feature gates cannot be read from outside,
so they are inferred and printed.

**Funnel**: The stages `why` runs each request through, showing how many devices
remain at each step and why the others were dropped. See
[why](commands/why.md#2-the-funnel).

**Holder**: A claim that has a device in its allocation. Holders with admin access are
listed but do not make the device busy.

**Issue**: A problem `nodes` reports for a node, such as a driver plugin that runs
but publishes nothing. See [nodes](commands/nodes.md#issues).

**Local device**: A device that can be used on exactly one node, named by its slice or by
the device itself. `nodes` counts local devices per node.

**Map status**: The state `map` shows for each row: `Allocated`, `Pending`, `Missing`
(the pod names a claim that does not exist) or `NotCreated` (a template
claim not created yet). See [map](commands/map.md#reading-the-output).

**Outdated slice**: A slice of an older generation than its pool's newest. Ignored, and
normal for a moment while a driver republishes. Shown by `pools -o wide`.

**Plugin pod**: A pod that mounts the kubelet's `plugins_registry` directory, which DRA
drivers and CSI storage drivers do. `nodes` uses plugin pods to spot drivers
that run on a node without publishing for it. See
[nodes](commands/nodes.md#how-it-works).

**Pool status**: `Complete`, `Incomplete` (slices missing) or `Invalid` (duplicate device
names, or slices that disagree on `resourceSliceCount`). Only complete pools
can be allocated from. See [pools](commands/pools.md#reading-the-output).

**Pool warning**: A note from `why` that an incomplete or invalid pool contains devices the
claim's class would select. Fixing that pool may unblock the claim.

**Reachable device**: A device that is not local to one node (network-attached, or selected by
labels) but can be used on a given node. Counted separately by `nodes`.

**Snapshot**: The cluster state a command reads once, at start, and analyzes. Nothing is
read again during the command. See
[the command guide](commands/README.md#one-snapshot-per-command).

**Stage**: One step of the funnel: published, in usable pools, match the class,
match the request selectors, not allocated to other claims, taints
tolerated, reachable from candidate nodes.

**Usable pool**: A pool the scheduler can allocate from: complete and valid.

**Verdict**: The first line of a `why` report: `ALLOCATED`,
`ALLOCATED, POD PENDING`, `ALLOCATABLE`, `NOT ALLOCATABLE` or `ERROR`. It
follows the upstream allocator. See [why](commands/why.md#verdicts).
