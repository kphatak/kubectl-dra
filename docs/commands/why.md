# kubectl dra why

```
kubectl dra why CLAIM -n NAMESPACE
kubectl dra why resourceclaim/CLAIM -n NAMESPACE
kubectl dra why pod/POD -n NAMESPACE
```

Explains why a ResourceClaim is not allocated, or why a pod is stuck on its
claims. It starts with a one-line verdict, then shows where the candidate
devices for each request disappear, then asks the scheduler's own allocator
for the final answer.

## Why it matters

When a pod waits for devices, the scheduler's message is usually just
`0/N nodes are available: N node(s) cannot allocate all claims`. It does not
say which request failed, which selector rejected which device, whether the
devices are taken, tainted, in an incomplete pool, or on nodes the pod cannot
use. Working that out by hand means reading every ResourceSlice and
DeviceClass, evaluating CEL in your head, collecting allocations from every
claim in the cluster, and checking the pod's node constraints.

`why` does that work and shows it as a funnel, so you can see the step where
the candidates run out. Because it also runs the upstream allocator, the
verdict is the scheduler's verdict, not a guess.

## Example

```console
$ kubectl dra why all-busy -n why
Claim:    why/all-busy
Verdict:  NOT ALLOCATABLE
Reason:   request "gpu": every matching device (5) is allocated to another claim

Request "gpu": 1 device from class a100
  STAGE                           LEFT   DROPPED
  published                          8
  in usable pools                    7   -1 pool gpu.example.com/node-c is incomplete (1 of 2 slices)
                                              gpu.example.com/node-c/gpu-0
  match the class                    5   -2 class selector #1 is false: device.attributes['gpu.example.com'].model == 'A100'
                                              gpu.example.com/node-b/gpu-0  gpu.example.com/model="L4"
                                              gpu.example.com/node-b/gpu-1  gpu.example.com/model="L4"
  match the request selectors        5
  not allocated to other claims      0   -5 allocated to another claim
                                              gpu.example.com/node-a/gpu-0  other/holder
                                              gpu.example.com/node-a/gpu-1  other/holder
                                              gpu.example.com/node-a/gpu-2  other/holder
                                              +2 more
  ✗ every matching device (5) is allocated to another claim

Warnings
  ! pool gpu.example.com/node-c has devices this claim could use but is incomplete (1 of 2 slices); the scheduler does not allocate from it

Allocator
  ✗ no allocation on any of the 3 candidate nodes
  features assumed: DRAPrioritizedList

Next steps
  • Every matching device is in use. See holders with: kubectl dra devices --class a100
  • Incomplete pools usually mean a driver is republishing or failing. Check: kubectl dra pools and kubectl dra nodes --issues
```

Read it top down. Five A100s exist and match; all five are held by
`other/holder`. A sixth A100 exists on `node-c`, but its pool is incomplete,
so the scheduler cannot use it either; fixing the driver on `node-c` would
unblock the claim.

On a terminal the verdict is colored (green when allocated or allocatable,
yellow for a pending pod, red otherwise), as are the `✓`, `✗` and `!`
markers and the stage where the devices run out. Set `NO_COLOR=1` to turn
colors off. Output to a pipe or a file is never colored.

## Targets

| Target | What is explained |
|---|---|
| `CLAIM` or `resourceclaim/CLAIM` | That claim. If a pod in the namespace uses it, that pod's node constraints apply; for a claim shared by several pods, the first one is used. A claim no pod uses is checked against every node. |
| `pod/POD` (also `pods/`, `po/`) | Every claim in the pod's `spec.resourceClaims`, one report each. Template claims are resolved to their generated names. A claim that does not exist, or a template claim not created yet, gets a short report saying so. |

`why` needs a namespace (`-n`, or your kubeconfig's current one) and rejects
`-A`.

## Verdicts

| Verdict | Meaning |
|---|---|
| `ALLOCATED` | The claim has devices. They are listed. |
| `ALLOCATED, POD PENDING` | The claim has devices, but the pod is not scheduled. See [allocated claims](#5-allocated-claims-with-a-pending-pod). |
| `ALLOCATABLE` | The allocator finds devices on at least one candidate node. The claim is waiting for something else, typically for a pod to be scheduled, or it has no pod at all. |
| `NOT ALLOCATABLE` | No candidate node can satisfy the claim. The summary names the step where the candidates run out. |
| `ERROR` | The claim cannot be evaluated, for example because a CEL expression does not compile or fails at runtime. The scheduler fails such claims too. |

## How it works

### 1. Candidate nodes

For a pod, the candidate nodes are the nodes it could be scheduled on, as
far as DRA is concerned:

- a pod already bound to a node has only that node;
- otherwise, nodes that match the pod's `nodeSelector` and required node
  affinity, whose `NoSchedule` and `NoExecute` taints the pod tolerates, and
  that are not cordoned.

Node affinity and taint matching use the upstream component-helpers, as the
scheduler does. Excluded nodes are listed with the reason, grouped:

```
Candidate nodes
  The pod can run on 1 of 3 nodes.
  ✗ 2 does not match the pod's nodeSelector or node affinity: node-b, node-c
```

A claim without a pod uses all nodes.

### 2. The funnel

Each request, and each alternative of a `firstAvailable` request in order,
goes through these stages. Each line shows how many devices remain (`LEFT`)
and, on the same line, how many were dropped and why (`DROPPED`). Under each
reason come up to three example devices, each with what made it drop (an
attribute value, or the claim holding it), and a count of the rest. The last
line of each request is its result: `✓ satisfiable` with the best node, or
`✗` and the reason.

| Stage | Drops devices that |
|---|---|
| published | (start) Every device in the newest generation of every pool. |
| in usable pools | are in an incomplete or invalid pool, or have no node selection this version understands. |
| match the class | fail one of the DeviceClass's CEL selectors. |
| match the request selectors | fail one of the request's own CEL selectors. |
| not allocated to other claims | are held by another claim. Skipped for admin access requests, which may use devices others hold. A shared device that allows multiple allocations is kept, and the allocator decides whether it has room. |
| taints tolerated | have a `NoSchedule` or `NoExecute` taint (including from DeviceTaintRules) that the request does not tolerate. |
| reachable from candidate nodes | cannot be used on any candidate node. |

The text output stops at the first stage that reaches zero, since nothing
after it can recover. JSON output always has every stage.

**CEL explanations.** When a selector is false, the example shows the
device's values for the attributes and capacities that expression reads,
such as `(gpu.example.com/model="L4")` or `(gpu.example.com/memory=80Gi)`,
or `<missing>` if the device does not have one. That is usually enough to see
a typo or a wrong unit. Selectors are evaluated with the upstream CEL
package, in order, stopping at the first false one, as the allocator does. A
compile or runtime error stops the explanation with `ERROR`, because the
scheduler cannot evaluate the claim either.

**Counting per node.** All devices of a request must be usable from the same
node, so the final check is per candidate node: a request for `count: 4`
needs four surviving devices that one node can reach. The output names the
best node and how many it reaches.

**allocationMode: All** follows the allocator's stricter rules. On a node,
every device that matches the selectors must be allocatable, there must be at
least one, and every pool the node can see must be usable. An incomplete pool
on a node makes that node fail for `All` requests even if the pool belongs to
another driver.

**Several requests.** A claim with several requests is satisfiable only if
one node can satisfy all of them. When each request works on some node but
never on the same one, the verdict says so.

### 3. Pool warnings

An incomplete or invalid pool removes its devices at the second stage. If any
of those devices would have matched the request's DeviceClass, `why` adds a
pool warning, since fixing that pool may be the quickest way to unblock the
claim. Pools whose devices the class would not select are not mentioned.

### 4. The allocator check

Finally, `why` runs the upstream allocator
(`k8s.io/dynamic-resource-allocation/structured`, the code the scheduler
runs) for the claim on every candidate node, with:

- the allocated state of every other claim in the cluster;
- all ResourceSlices, with DeviceTaintRules applied the way the scheduler's
  slice tracker applies them;
- the DeviceClasses;
- allocator features inferred from the cluster's objects (see
  [Limits](#limits)), which are printed as `features assumed`.

**The verdict follows the allocator.** The funnel explains its answer:

- If the allocator finds devices anywhere, the verdict is `ALLOCATABLE` and
  the nodes are listed. If the funnel had said no, the output says the funnel
  was stricter than the allocator, which is a kubectl-dra bug worth
  reporting.
- If the allocator finds nothing and the funnel shows a request that cannot
  be satisfied, the verdict is `NOT ALLOCATABLE` with the funnel's reason.
- If every request can be satisfied on its own but the allocator still
  refuses every node, the verdict says that the reason is not broken down.
  This happens with constraints across requests (`matchAttribute`,
  `distinctAttribute`) and with requests competing for the same devices.

**Errors on one node.** For some inputs the allocator returns an error for a
specific node instead of "no", for example an `All` request on a node that
sees an incomplete pool. `why` records these per node and keeps trying the
others. If the scheduler evaluates such a node, it aborts the whole
scheduling attempt, even if another node would have worked, so an
`ALLOCATABLE` verdict mentions them. Only when every candidate node fails
with an error is the verdict `ERROR`, unless the funnel already has a clearer
answer, such as a missing DeviceClass.

### 5. Allocated claims with a pending pod

For an allocated claim, `why` lists the devices. If the pod is not
scheduled, it also shows:

- the scheduler's `PodScheduled` condition message;
- the pod's last three warning events, such as `FailedScheduling`;
- the nodes where the claim's devices can be used, and which of those the pod
  can run on.

That last check catches a common trap: the claim was allocated on one node
(often for an earlier pod), and the pod now cannot run there because of its
node selector, affinity or taints.

```console
$ kubectl dra why pod/pending-pod -n why
Claim:    why/bound
Pod:      why/pending-pod
Verdict:  ALLOCATED, POD PENDING
Reason:   the claim's devices are usable only on node-a, and the pod cannot run there

Allocated devices
  gpu  gpu.example.com/node-a/gpu-4

Scheduler
  PodScheduled      0/3 nodes are available: 1 node(s) didn't match Pod's node affinity/selector, 1 node(s) had untolerated taint {dedicated: infra}, 1 node(s) cannot allocate all claims.
  FailedScheduling  0/3 nodes are available: 1 node(s) cannot allocate all claims.

Nodes
  The claim's devices are usable on 1 node; the pod can run on 0 of them.
  ✗ 1 does not match the pod's nodeSelector or node affinity: node-a

Next steps
  • The pod's nodeSelector, affinity or tolerations exclude the nodes where the claim's devices are; fix the pod, or delete the claim so it is allocated again
```

It also notices when a claim's `reservedFor` list is full, so no further pod
can use it.

### 6. Hints

The last section suggests what to look at next, based on where the funnel
ran out: a missing DeviceClass, devices that match nothing, devices all in
use, untolerated taints, devices on nodes the pod cannot use, or a count no
single node can meet. Hints point at read-only commands first. Requests that
are satisfied, including through a later `firstAvailable` alternative, get
no hints.

## Limits

- **Constraints across requests** are checked by the allocator but not yet
  explained. The output says "not decomposed" instead of naming the
  conflicting attribute values.
- **Pods with several claims** are explained one claim at a time. The
  scheduler allocates all of a pod's claims together, so claims that each fit
  alone may still not fit together.
- **Consumable capacity and shared counters** are left to the allocator. The
  funnel does not compute remaining capacity on a shared device, or counters
  used by sibling partitions.
- **Features are inferred.** The scheduler's feature gates cannot be read
  from outside, so a feature is assumed on when some object in the cluster
  uses fields only that feature handles. A cluster with such a gate turned
  off may decide differently; the assumed features are printed so this can
  be checked.
- **Candidate nodes** consider node selection, affinity, taints and cordons,
  not CPU, memory, ports, pod affinity or topology spread. A node `why`
  calls a candidate may still be rejected by the scheduler for other reasons.
- **Allocations in flight** that the scheduler has decided but not yet
  written are invisible from outside.

## JSON fields

`{"explanations": [...]}`, one object per claim:

| Field | Type | Notes |
|---|---|---|
| `claim`, `pod` | string | `namespace/name`. |
| `verdict`, `summary` | string | |
| `allocation` | list | Allocated devices, in the API's shape. |
| `podScheduled` | string | For a pending pod. |
| `events` | []string | For a pending pod. |
| `candidateNodes` | []string | |
| `excludedNodes` | list | `reason`, `count`, `examples`. |
| `requests` | list | `name`, `satisfiable`, `alternatives` (one per `firstAvailable` alternative). |
| `requests[].alternatives[]` | object | `request`, `class`, `allocationMode`, `count`, `adminAccess`, `stages`, `need`, `bestNode`, `bestCount`, `nodes`, `satisfiable`, `problem`, `error`. |
| `stages[]` | object | `name`, `remaining`, `dropped` (`reason`, `count`, `examples`). |
| `poolWarnings` | []string | |
| `allocator` | object | `features`, `nodesTried`, `nodes`, `nodeErrors`, `abortingNodes`, `error`. |
| `hints` | []string | |

Print the verdict for every pending pod in a namespace:

```bash
kubectl get pods -n team-a --field-selector=status.phase=Pending -o name \
  | xargs -n1 -I{} kubectl dra why {} -n team-a -o json \
  | jq -r '.explanations[] | "\(.pod) \(.claim): \(.verdict): \(.summary)"'
```

## Related

- [devices](devices.md) `--class CLASS` to see the devices a class matches,
  and who holds them.
- [map](map.md) to see which pods hold the devices a claim wants.
- [pools](pools.md) and [nodes](nodes.md) when the funnel points at
  incomplete pools or missing devices.
