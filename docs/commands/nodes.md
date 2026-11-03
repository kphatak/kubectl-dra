# kubectl dra nodes

```
kubectl dra nodes [--issues] [-o table|wide|json|yaml]
```

Shows DRA from each node's point of view: which drivers publish devices for
it, how many of those are in use or free, and problems that usually mean a
driver is not working on that node.

## Why it matters

When a driver fails on one node, the cluster does not complain. The node
stays Ready, other nodes keep working, and that node's devices quietly
disappear from the pool of candidates. Pods that need them wait, and the
scheduler only says some nodes "cannot allocate all claims". Common causes:

- the driver's kubelet plugin pod is crash-looping or not Ready on that node
- the plugin runs but never publishes a ResourceSlice for the node
- the node went NotReady, so its slices describe a state that may be gone
- the node was deleted and its slices were left behind

`nodes` lists these per node, so a fleet operator can scan for broken nodes
instead of digging through driver logs one node at a time. Run
`kubectl dra nodes --issues` after a driver rollout or node replacement.

## Example

```console
$ kubectl dra nodes
NODE        DRIVERS           DEVICES   ALLOCATED   FREE   ISSUES
cpu-1       <none>            0         0           0      <none>
gpu-1       gpu.example.com   4         1           2      <none>
gpu-2       <none>            0         0           0      kubelet plugin pod dra-driver/gpu-plugin-b is not Ready; DaemonSet dra-driver/gpu-plugin (dra-driver/gpu-plugin-b) seems to be the kubelet plugin for gpu.example.com, but no gpu.example.com ResourceSlice names this node
gpu-3       gpu.example.com   2         0           2      node is NotReady; its ResourceSlices may be stale
gpu-4       gpu.example.com   1         0           0      pool gpu.example.com/gpu-4 is Incomplete (1 of 2 slices)
gone-node   gpu.example.com   1         0           1      node does not exist; its ResourceSlices are left over
```

`cpu-1` runs a CSI storage plugin but no GPU driver, and is correctly not
flagged. `gpu-2` has the GPU driver's plugin, but it is not Ready and nothing
was published for the node, so its GPUs are missing from the cluster.

## Reading the output

| Column | Meaning |
|---|---|
| `NODE` | A node, or a node name that only appears in ResourceSlices. Existing nodes come first. |
| `DRIVERS` | Drivers that publish ResourceSlices for this node. |
| `DEVICES` | Devices local to this node. |
| `ALLOCATED` | Local devices held by a claim, exclusively or as a share. |
| `FREE` | Local devices with status `Free` in [devices](devices.md). |
| `ISSUES` | Problems found, separated by `;`. |

`-o wide` adds `TAINTED` and `UNAVAILABLE` (the other local device
statuses), `REACHABLE` (devices that are not local to any node, such as
network-attached ones, that this node can use), and `PLUGIN PODS`.

Local devices are those whose slice names this node, or that name it
themselves with per-device node selection. Network-attached devices are kept
out of `DEVICES` on purpose; otherwise every node would count every shared
device and the totals would not add up.

## Issues

| Issue | What it means | What to check |
|---|---|---|
| `kubelet plugin pod NS/NAME is not Ready` | A pod that registers a kubelet plugin on this node is crash-looping, starting or failing its probe. If it is a DRA driver, the node's devices may be missing or stale. | `kubectl describe pod`, the pod's logs. |
| `... is the kubelet plugin for DRIVER, but no DRIVER ResourceSlice names this node` | The driver's plugin runs here but published nothing for this node. Its devices are not available to the scheduler. "seems to be" means the match between plugin and driver was inferred (see below). | The plugin's logs; whether the node has the hardware. |
| `node is NotReady; its ResourceSlices may be stale` | The node's kubelet stopped reporting. The slices still describe its devices, but they may not exist or work any more. Only shown for nodes with slices. | The node's conditions. |
| `pool DRIVER/POOL is Incomplete (N of M slices)` or `is Invalid` | The pool local to this node is unusable. See [pools](pools.md). | The driver's logs on this node. |
| `node does not exist; its ResourceSlices are left over` | Slices name a node that is not in the cluster. Slices published by a kubelet plugin are normally deleted along with their node; these were not. | Whether the node was deleted; the slices' owner references. |

## Flags

| Flag | Effect |
|---|---|
| `--issues` | Only nodes with at least one issue. Prints `No nodes with issues.` on stderr when the cluster is clean. |

`nodes` lists pods in all namespaces regardless of `-n`, because driver pods
usually live in their own namespace.

## How it works

1. Pools and device statuses are built as in [pools](pools.md) and
   [devices](devices.md).
2. Each device is assigned to the node it is local to, or, if it is not local
   to any node, counted as reachable from every node it can be used on.
3. **Kubelet plugin pods** are found by a hostPath volume whose last path
   element is `plugins_registry`, the directory where kubelet plugins
   register. Matching only the last element means kubelets with a
   non-default root directory are covered too. Finished pods (Succeeded or
   Failed) are ignored. Pods are grouped by their controller, usually a
   DaemonSet.
4. **Matching plugins to drivers.** The API has no field that says which
   plugin pod serves which driver, so the match is made in this order:
   1. A group whose pods mount `<kubelet>/plugins/<driver>` for a DRA driver
      is that driver's plugin. This is the default socket directory of the
      upstream kubelet plugin library.
   2. Otherwise the match is inferred. Candidates are groups that do not
      mount a `plugins/<X>` directory of something that is not a DRA driver
      (standard CSI node plugins mount their own, which rules them out), and
      that run on every existing node the driver publishes for. Among those,
      the groups that run on the fewest nodes win. A GPU driver's DaemonSet,
      which only runs on GPU nodes, beats a storage plugin that runs on every
      node.
   3. A node is flagged only if every winning candidate runs on it and the
      driver publishes nothing for it. If two candidates tie, a node one of
      them does not cover is never flagged, so a tie cannot cause a false
      alarm.

   The dra-example-driver used in the e2e tests mounts the whole
   `<kubelet>/plugins` directory, so it is matched by inference; drivers that
   mount their own directory are matched directly.
5. **NotReady** is reported only when the node's `Ready` condition exists and
   is `False` or `Unknown`. A node without the condition is not reported,
   since nothing is known about it.

## Limits

- A driver that publishes no slices anywhere cannot be matched to its
  plugin, so a driver that is broken on every node is not flagged by the
  plugin rule. `kubectl dra pools` simply lists no pools for it, and the
  "not Ready" rule still catches crash-looping plugin pods.
- A non-standard CSI plugin that does not mount its own `plugins/<name>`
  directory could tie with, or beat, a DRA driver's plugin. Ties never cause
  alarms, but a beaten DRA plugin means some broken nodes go unflagged.
- The "node labels that a DeviceClass expects" heuristic is not
  implemented, because there is no reliable way to tie a DeviceClass to node
  labels.

## JSON fields

`{"nodes": [...]}`, one object per node:

| Field | Type | Notes |
|---|---|---|
| `node` | string | |
| `exists` | bool | `false` for names only found in slices. |
| `drivers` | []string | |
| `devices`, `allocated`, `free`, `tainted`, `unavailable` | int | Local devices. |
| `reachable` | int | Non-local devices this node can use. |
| `pluginPods` | []string | `namespace/name`. |
| `issues` | []string | |

Fail a CI job or a cron check when any node has issues:

```bash
test "$(kubectl dra nodes --issues -o json | jq '.nodes | length')" -eq 0
```

## Related

- [pools](pools.md) for pool problems across the cluster.
- [devices](devices.md) with `--node NODE` for one node's devices.
