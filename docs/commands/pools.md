# kubectl dra pools

```
kubectl dra pools [--driver DRIVER] [--node NODE] [-o table|wide|json|yaml]
```

Lists every pool that DRA drivers publish, with its generation, how many of
its slices are present, how many devices it has and how many of those are in
use, and whether the scheduler can allocate from it.

## Why it matters

A pool the scheduler considers incomplete fails silently. Nothing is logged
on the claim and no event says "pool incomplete". The pool's devices simply
never get picked, and pods wait with the generic message that a node "cannot
allocate all claims". This happens for ordinary reasons:

- A driver publishes a large pool as several slices and one of them failed
  to write, or was deleted.
- A driver is in the middle of republishing at a new generation and has not
  finished, or crashed halfway.
- A driver bug writes slices that disagree about how many slices the pool
  has, or that publish the same device name twice.

`pools` makes these cases visible in one line each. It is also the quickest
way to check that every node's driver published anything at all.

## Example

```console
$ kubectl dra pools
DRIVER             POOL                      NODES                     GENERATION   SLICES   DEVICES   ALLOCATED   STATUS
fake.example.com   incomplete                all nodes                 1            1/2      2         0           Incomplete
gpu.example.com    kubectl-dra-e2e-worker    kubectl-dra-e2e-worker    1            1/1      8         1           Complete
gpu.example.com    kubectl-dra-e2e-worker2   kubectl-dra-e2e-worker2   1            1/1      8         2           Complete
```

## Reading the output

| Column | Meaning |
|---|---|
| `DRIVER`, `POOL` | The pool's identity. Pool names are only unique per driver. Node-local pools are usually named after the node. |
| `NODES` | Where the pool's devices can be used: a node name, `all nodes`, a node selector, or `mixed (N)` when devices differ (per-device node selection). |
| `GENERATION` | The newest generation published. Only this generation counts. |
| `SLICES` | Slices present at that generation, over the `resourceSliceCount` they declare. `?` means the slices disagree on the count. |
| `DEVICES` | Devices in the newest generation. |
| `ALLOCATED` | Devices held by a claim, exclusively or as a share. Admin access does not count. |
| `STATUS` | `Complete`, `Incomplete` or `Invalid`, below. |

| Status | Meaning |
|---|---|
| `Complete` | Every slice of the newest generation is present. The scheduler can allocate from the pool. |
| `Incomplete` | Slices are missing. The scheduler allocates nothing from this pool until they appear. |
| `Invalid` | The slices are present but inconsistent: a device name appears twice, or the slices disagree on `resourceSliceCount`. |

`-o wide` adds two columns: `OUTDATED SLICES`, the number of slices left
over from older generations, and `PROBLEMS`, the reason for `Invalid`.
Outdated slices are normal for a few seconds while a driver republishes. If
they stay, the driver is not cleaning up.

## Flags

| Flag | Effect |
|---|---|
| `--driver DRIVER` | Only this driver's pools. |
| `--node NODE` | Only pools with at least one device usable on that node, including network-attached pools. Fails if the node does not exist. |

`-n` and `-A` have no effect, since pools are cluster-wide.

## How it works

1. All ResourceSlices are grouped by driver and pool name.
2. For each pool, the highest `pool.generation` among its slices wins. Slices
   at lower generations are counted as outdated and otherwise ignored, as the
   scheduler ignores them.
3. The pool is complete when the number of slices at that generation equals
   the `resourceSliceCount` they all declare.
4. If those slices declare different counts, the pool is reported as
   invalid. The scheduler takes the count from whichever slice it happens to
   see first, so its view of such a pool depends on ordering and cannot be
   relied on.
5. If a device name appears in more than one slice of the pool, the pool is
   invalid. This is the same check as the upstream allocator's
   `validateDeviceNames`, which rejects the whole pool.
6. `ALLOCATED` comes from the allocated state described in
   [concepts](README.md#when-a-device-counts-as-allocated).

The scheduler gathers pools per node, but completeness is a property of the
whole pool, and the scheduler checks it against all slices of the pool. So a
single cluster-wide answer is the same answer every node would get.

`pools` does not list pods or claim templates, which keeps it fast on large
clusters.

## Limits

- Some DRA features make the scheduler skip slices when the feature gate is
  off: slices with shared counters or per-device node selection when
  partitionable devices are disabled, and devices with compatibility groups
  when that feature is disabled. Skipped slices can make a pool incomplete
  from the scheduler's point of view. kubectl-dra cannot read feature gates,
  so `pools` counts every slice.
- Shared counters (partitionable devices) are not validated. The allocator
  can reject a pool whose counter sets are inconsistent; `pools` does not
  check this yet.

## JSON fields

`{"pools": [...]}`, one object per pool:

| Field | Type | Notes |
|---|---|---|
| `driver`, `pool` | string | |
| `nodeScope` | string | As in `NODES`. |
| `generation` | int | |
| `slices` | int | Slices at the newest generation. |
| `resourceSliceCount` | int | Declared count; `0` when the slices disagree. |
| `outdatedSlices` | int | |
| `devices`, `allocated` | int | |
| `status` | string | `Complete`, `Incomplete`, `Invalid`. |
| `problems` | []string | Omitted when empty. |

Find every pool that is not usable:

```bash
kubectl dra pools -o json | jq -r '.pools[] | select(.status != "Complete") | "\(.driver)/\(.pool): \(.status)"'
```

## Related

- [devices](devices.md) lists the devices of each pool.
- [nodes](nodes.md) shows incomplete pools per node, next to the driver's
  plugin pod on that node.
- [why](why.md) warns when an unusable pool has devices a pending claim could
  have used.
