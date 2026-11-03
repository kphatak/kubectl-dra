# Command guide

This guide explains what each kubectl-dra command does, how it reaches its
answer, and when to use it. The [README](../../README.md) has a one-line
summary of each command; [design.md](../design.md) records the design
decisions behind them.

| Command | Use it when |
|---|---|
| [why](why.md) | A claim or a pod is pending and you want to know why. |
| [map](map.md) | You want to see which pod got which device, on which node. |
| [devices](devices.md) | You want the device inventory: what exists, what is free, what is blocked. |
| [pools](pools.md) | You suspect a driver is publishing incomplete or broken data. |
| [nodes](nodes.md) | You want a per-node view, or to find nodes whose driver is not working. |

A typical investigation goes like this:

1. A pod is Pending. Run `kubectl dra why pod/NAME -n NS`.
2. If the answer is "every matching device is in use", run
   `kubectl dra devices --class CLASS` to see who holds them, and
   `kubectl dra map -A` to see which pods those claims belong to.
3. If the answer points at an incomplete pool, or at fewer devices than you
   expect, run `kubectl dra pools` and `kubectl dra nodes --issues` to find
   the driver or node at fault.

## Concepts used by every command

### DRA objects in one paragraph

A DRA driver describes the devices on a node (or reachable from many nodes)
in **ResourceSlices**. The slices of one driver are grouped into **pools**.
A **DeviceClass** is a named filter over devices, written in CEL. A
**ResourceClaim** asks for devices: each request names a DeviceClass, can add
its own CEL selectors, and asks for a count or for all matching devices. The
scheduler picks devices for a claim when it schedules the first pod that uses
the claim, and writes the result to the claim's `status.allocation`. A
**ResourceClaimTemplate** makes the controller create one claim per pod.

### One snapshot per command

Before listing anything, each command checks with one discovery request that
the API server serves `resource.k8s.io/v1`. If it does not, the command exits
with code 1 and a message that says whether the cluster is too old or has DRA
turned off; see [Clusters without DRA](../../README.md#clusters-without-dra).

Each command lists the objects it needs once, in parallel and in pages, and
then works only on that snapshot. Nothing is created, changed or deleted, and
nothing runs in the cluster. The output is therefore consistent within one
run, but it can be a few seconds behind a cluster that is changing.

Required lists are nodes, DeviceClasses, ResourceSlices and ResourceClaims,
plus pods for the commands that look at pods. If one of these is forbidden,
the command fails and says which list. Optional lists are
ResourceClaimTemplates, DeviceTaintRules and events. If one of these is
forbidden, the command prints a warning on stderr and continues, because the
answer may be incomplete. DeviceTaintRules are only listed when the API
server serves them in `resource.k8s.io/v1`.

### Claims are always cluster-wide

ResourceClaims are listed in every namespace, even with `-n`. A device held
by a claim in another namespace is just as unavailable as one held in yours,
so a namespace-scoped view would call busy devices free. Commands that print
claims (`map`, `why`) still only show the namespace you asked for.

### Pools and generations

A driver republishes a pool by writing new slices with a higher
`pool.generation`. Each slice also says how many slices the pool has at that
generation (`resourceSliceCount`). The scheduler only uses the newest
generation, and only once all of its slices are visible. Until then the
whole pool is treated as unavailable. kubectl-dra applies the same rule
everywhere: older slices are ignored, and devices in an incomplete or invalid
pool are never counted as free. See [pools](pools.md).

### Where a device can be used

Every device is usable on some set of nodes, written in the slice as one of:

- `nodeName`: one node (local GPUs, NICs)
- `allNodes: true`: every node (for example network-attached devices)
- `nodeSelector`: the nodes that match a selector (a rack, a zone)

With `perDeviceNodeSelection`, each device carries its own setting instead of
the slice. kubectl-dra matches nodes with the allocator's own
`structured.NodeMatches`, so it agrees with the scheduler on where a device
can be used. Tables show this as `NODES`: a node name, `all nodes`, or the
selector in short form. A device with none of these settings uses a future
API extension; the scheduler skips it, and so does kubectl-dra.

### When a device counts as allocated

A device is in use if some claim's `status.allocation` names it, following
kube-scheduler's own bookkeeping:

- A result with admin access does not hold the device. Admin access is meant
  for monitoring tools that need to see devices other workloads use.
- A result with a `shareID` (consumable capacity) holds a share of the
  device, not the whole device.
- Anything else holds the device exclusively.

The scheduler also counts allocations it has decided on but not yet written
to the API server. Those are invisible from outside, so for a moment a device
can look free while it is being allocated.

### Taints

A device can be tainted by its driver (in the slice) or by a
**DeviceTaintRule**, which an administrator creates to take devices out of
service without touching the driver. kubectl-dra adds the rule taints to
devices the way the scheduler does: a rule without a `deviceSelector` matches
nothing, and each field of the selector that is set (driver, pool, device)
must match exactly. Only `NoSchedule` and `NoExecute` taints keep a device
from being allocated to a request that does not tolerate them. `None` and
unknown effects are shown but do not block anything.

### Output formats

Every command takes `-o table` (the default), `-o wide`, `-o json` and
`-o yaml`. JSON and YAML print the command's report, not raw Kubernetes
objects; the field names are listed on each command's page. An empty result
is an empty list (`{"pools": []}`), never `null`, so scripts do not need a
special case. Tables print `<none>` for empty cells, and a message on stderr
when there are no rows.

All commands also accept kubectl's connection flags (`--context`,
`--kubeconfig`, `--as`, and so on) and `-n` / `-A`.
