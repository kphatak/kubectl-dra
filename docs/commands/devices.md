# kubectl dra devices

```
kubectl dra devices [--node NODE] [--driver DRIVER] [--class CLASS] [--free] [-o table|wide|json|yaml]
```

Lists every device that DRA drivers publish, with where it can be used, the
claims using it, its taints, and one status saying whether it can be
allocated right now.

## Why it matters

"How many GPUs are free?" has no answer in plain kubectl. ResourceSlices say
which devices exist but not which are taken. Allocations live in the status
of every ResourceClaim, in every namespace. Taints can come from the slice
or from DeviceTaintRules that do not appear on the slice at all. And "which
devices does this DeviceClass match?" needs a CEL evaluator.

`devices` puts all of that in one table. Use it to check capacity before
submitting work, to find out who holds the devices a pending claim wants,
and to test a DeviceClass against real hardware before anyone depends on it.

## Example

```console
$ kubectl dra devices
DRIVER            POOL     DEVICE   NODES       STATUS        CLAIMS
gpu.example.com   node-a   gpu-0    node-a      Allocated     team-a/train
gpu.example.com   node-a   gpu-1    node-a      Tainted       <none>
gpu.example.com   node-a   gpu-2    node-a      Tainted       <none>
gpu.example.com   node-a   gpu-3    node-a      Free          ops/monitor (admin)
gpu.example.com   node-b   gpu-0    node-b      Unavailable   <none>
net.example.com   fabric   nic-0    all nodes   Shared        team-a/net-1,team-b/net-2
```

The same devices with `-o wide`:

```console
$ kubectl dra devices -o wide
DRIVER            POOL     DEVICE   NODES       STATUS        CLAIMS                      TAINTS                                      REASON                               ATTRIBUTES
gpu.example.com   node-a   gpu-0    node-a      Allocated     team-a/train                <none>                                      <none>                               index=0,model=A100
gpu.example.com   node-a   gpu-1    node-a      Tainted       <none>                      example.com/maintenance:NoSchedule          <none>                               index=1,model=A100
gpu.example.com   node-a   gpu-2    node-a      Tainted       <none>                      example.com/drain:NoExecute (drain-gpu-2)   <none>                               index=2,model=L4
gpu.example.com   node-a   gpu-3    node-a      Free          ops/monitor (admin)         example.com/ecc-warning=1:None              <none>                               index=3,model=L4
gpu.example.com   node-b   gpu-0    node-b      Unavailable   <none>                      <none>                                      pool is incomplete (1 of 2 slices)   index=0,model=A100
net.example.com   fabric   nic-0    all nodes   Shared        team-a/net-1,team-b/net-2   <none>                                      <none>                               speed=400G
```

Here `gpu-1` is tainted by its driver, `gpu-2` by the DeviceTaintRule
`drain-gpu-2`, and `gpu-3` is free even though it has a taint, because the
effect `None` blocks nothing, and even though a monitoring claim holds it,
because that claim has admin access.

## Reading the output

| Column | Meaning |
|---|---|
| `DRIVER`, `POOL`, `DEVICE` | The device's identity. Together they are unique in the cluster. |
| `NODES` | Where the device can be used: a node name, `all nodes`, or a node selector. |
| `STATUS` | One of the statuses below. |
| `CLAIMS` | Claims holding the device, as `namespace/name`. Admin access claims are marked `(admin)`. |
| `TAINTS` (wide) | `key=value:Effect`, followed by the DeviceTaintRule name in parentheses when a rule added it. |
| `REASON` (wide) | Why the device is `Unavailable`. |
| `ATTRIBUTES` (wide) | Attributes as sorted `name=value` pairs. List values print as `[a b]`. |

Statuses, in the order they are decided (the first that applies wins):

| Status | Meaning |
|---|---|
| `Allocated` | At least one claim holds the device exclusively. |
| `Shared` | Only claims sharing it through consumable capacity hold it. It may have room for more. |
| `Unavailable` | Its pool is incomplete or invalid, or it has no node selection this version understands. Nothing can be allocated from it. |
| `Tainted` | Not held, but it has a `NoSchedule` or `NoExecute` taint. Only requests with a matching toleration can get it. |
| `Free` | Any request whose selectors match can get it. |

Admin access holders never change the status.

## Flags

| Flag | Effect |
|---|---|
| `--node NODE` | Only devices usable on that node, including network-attached ones. Fails if the node does not exist. |
| `--driver DRIVER` | Only this driver's devices. |
| `--class CLASS` | Only devices that the DeviceClass's CEL selectors match. Fails if the class does not exist. |
| `--free` | Only devices with status `Free`. |

Flags combine. For example, `--class gpu.nvidia.com --node gpu-7 --free`
answers "what could a new claim for this class get on gpu-7 right now?",
apart from the request's own selectors.

### How `--class` evaluates CEL

`--class` uses the upstream `k8s.io/dynamic-resource-allocation/cel`
package, the library the scheduler uses, with the same inputs the
allocator passes: the driver name, the device's attributes and its capacity.
Attribute names without a domain belong to the driver's domain, as in the
scheduler. The class's selectors are evaluated in order, and a device is
dropped at the first one that is false.

A CEL error is not treated as "no match". A compile error, or a runtime error
such as reading an attribute that a device does not have, makes the command
fail with the class, selector index and device. The scheduler fails every
claim that uses such a class in the same way, so this is worth knowing before
anyone submits a claim. The usual fix for missing attributes is to guard the
expression, for example
`device.driver == "gpu.example.com" && device.attributes["gpu.example.com"].model == "A100"`,
since CEL's `&&` stops at the first false operand.

## How it works

1. Pools are built as in [pools](pools.md). Only devices of the newest
   generation are listed.
2. Holders come from the `status.allocation` of every claim in the cluster.
   See [concepts](README.md#when-a-device-counts-as-allocated).
3. Taints are the device's own taints plus those added by matching
   DeviceTaintRules. See [concepts](README.md#taints).
4. Where the device can be used comes from the slice, or from the device
   itself with per-device node selection.
5. The status is decided in the order of the table above.

`devices` does not list pods or claim templates.

## Limits

- `Shared` does not say how much capacity is left. The device may or may not
  fit another share; [why](why.md) asks the allocator.
- Devices that consume shared counters (partitionable devices) are shown as
  `Free` when nobody holds them, even if their counters are used up by
  sibling partitions. The allocator checks counters; `devices` does not yet.
- The CEL environment enables list-type attributes and consumable capacity,
  so devices using them evaluate instead of erroring. A cluster with those
  features turned off may treat such devices differently.

## JSON fields

`{"devices": [...]}`, one object per device:

| Field | Type | Notes |
|---|---|---|
| `driver`, `pool`, `device` | string | |
| `nodeScope` | string | As in `NODES`. |
| `status` | string | As above. |
| `reason` | string | For `Unavailable`. |
| `claims` | list | `namespace`, `name`, `request`, `subRequest`, `adminAccess`, `shareID`. |
| `taints` | list | `key`, `value`, `effect`, `timeAdded`, and `rule` for taints from a DeviceTaintRule. |
| `attributes`, `capacity` | map | In the same shape as the ResourceSlice, for example `{"model": {"string": "A100"}}`. |

Count free devices per node:

```bash
kubectl dra devices --free -o json | jq -r '.devices | group_by(.nodeScope)[] | "\(.[0].nodeScope): \(length)"'
```

## Related

- [map](map.md) shows the same allocations from the pods' side.
- [why](why.md) runs a claim's selectors, not just the class's, and explains
  why each device was rejected.
- [pools](pools.md) explains `Unavailable` devices in more detail.
