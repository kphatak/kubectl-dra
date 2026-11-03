# kubectl-dra

A read-only kubectl plugin for inspecting and debugging Kubernetes
[Dynamic Resource Allocation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
(DRA).

DRA is how GPUs, NICs and other accelerators are requested and allocated in
Kubernetes 1.34 and later. When a ResourceClaim stays pending, the only tools
today are `kubectl get resourceclaim -o yaml`, `kubectl get resourceslices -o
yaml` and the driver's logs. That leaves you to work out by hand which devices
exist, which ones a CEL selector matches, which are taken, which pools are
incomplete, and which nodes the pod can reach. `kubectl dra` answers those
questions directly, and `kubectl dra why` runs the same allocator code as the
scheduler, so its answer matches what the scheduler would decide.

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

## Contents

- [Commands](#commands)
- [Install](#install)
- [Permissions](#permissions)
- [Supported versions](#supported-versions)
- [Compared with plain kubectl](#compared-with-plain-kubectl)
- [How it works](#how-it-works)
- [Development](#development)

## Commands

| Command | Answers |
|---|---|
| [`why`](#kubectl-dra-why) | Why is this claim, or this pod's claim, not allocated? |
| [`map`](#kubectl-dra-map) | Which pod uses which claim, and which device did it get, on which node? |
| [`devices`](#kubectl-dra-devices) | Which devices exist, and which are free, taken, tainted or unusable? |
| [`pools`](#kubectl-dra-pools) | Is each driver's pool complete enough for the scheduler to use? |
| [`nodes`](#kubectl-dra-nodes) | What does each node have, and which nodes look misconfigured? |

The sections below are short. The [command guide](docs/commands/README.md)
has a full page per command: why it matters, how it reaches its answer, its
limits, and its JSON fields.

All commands accept the usual kubeconfig flags (`--context`, `--kubeconfig`,
`-n`, `-A`, and the rest of `kubectl`'s connection flags) and
`-o table|wide|json|yaml`. JSON and YAML output use a stable report schema
meant for scripts and CI, not raw Kubernetes objects.

### kubectl dra why

Full guide: [docs/commands/why.md](docs/commands/why.md)

```
kubectl dra why CLAIM -n NAMESPACE
kubectl dra why pod/POD -n NAMESPACE
```

Explains a claim, or every claim of a pod, starting with a one-line verdict:

| Verdict | Meaning |
|---|---|
| `ALLOCATED` | The claim has its devices. |
| `ALLOCATED, POD PENDING` | The claim has devices, but the pod is not scheduled. Shows the scheduler's `PodScheduled` message and recent warning events, and whether the pod can run where the devices are. |
| `ALLOCATABLE` | The upstream allocator finds devices for the claim. Lists the nodes. |
| `NOT ALLOCATABLE` | No candidate node can satisfy the claim. The summary names the first stage where candidates run out. |
| `ERROR` | The claim cannot be evaluated, for example because a CEL expression does not compile. The scheduler fails such claims too. |

For an unallocated claim, each request (and each `firstAvailable`
alternative, in order) goes through a funnel that shows where candidate
devices disappear and why:

1. **published**: devices in the newest generation of every pool
2. **in usable pools**: minus pools that are incomplete or invalid
3. **match the class**: minus devices the DeviceClass selectors reject
4. **match the request selectors**: minus devices the request's selectors reject
5. **not allocated to other claims**: minus devices in use (skipped for admin access)
6. **taints tolerated**: minus devices with a `NoSchedule` or `NoExecute` taint the request does not tolerate, including taints from DeviceTaintRules
7. **reachable from candidate nodes**: minus devices that no candidate node can use

Then the count is checked per node, since all devices of a request must be
reachable from one node. `allocationMode: All` follows the allocator's
stricter rules.

When a CEL selector evaluates to false, the example shows the device's values
for the attributes that expression reads, such as
`(gpu.example.com/memory=80Gi)` for a request that wants 100Gi. Compile and
runtime errors are reported as errors, as the scheduler treats them.

Candidate nodes are where the pod can run: its bound node, or nodes that
match its nodeSelector and required node affinity, tolerate its taints and
are not cordoned. A claim with no pod is checked against all nodes.

Finally `why` runs the upstream allocator (`k8s.io/dynamic-resource-allocation/structured`)
on every candidate node. **The verdict follows the allocator.** If every
request fits on its own but the allocator still says no (for example because
of a `matchAttribute` constraint across requests), `why` says so instead of
guessing.

The hints at the end point at the next read-only command to run.

### kubectl dra map

Full guide: [docs/commands/map.md](docs/commands/map.md)

```
kubectl dra map [-n NAMESPACE | -A] [-o wide]
```

One row per consumer and allocated device, or per consumer and request while
the claim is pending.

```console
$ kubectl dra map -A
NAMESPACE   CONSUMER                CLAIM            REQUEST     DRIVER            POOL     DEVICE   NODE     STATUS
team-a      pod/big                 big              gpus        <none>            <none>   <none>   <none>   Pending
team-a      pod/not-yet             <none>           <none>      <none>            <none>   <none>   <none>   NotCreated
team-a      pod/prio                prio             gpu/large   gpu.example.com   node-b   gpu-3    node-b   Allocated
team-a      pod/train               train-gpu-x1     gpu         gpu.example.com   node-a   gpu-0    node-a   Allocated
team-a      pod/typo                does-not-exist   <none>      <none>            <none>   <none>   <none>   Missing
team-a      pod/web-1               shared-nic       nic         net.example.com   fabric   nic-0    node-a   Allocated
team-a      podgroups/train-group   workload         gpu         gpu.example.com   node-b   gpu-1    node-b   Allocated
team-a      <none>                  orphan           gpu         gpu.example.com   node-a   gpu-1    node-a   Allocated
```

- `Pending`: the claim has no allocation yet. Run `kubectl dra why` on it.
- `Missing`: the pod names a ResourceClaim that does not exist.
- `NotCreated`: the pod's ResourceClaimTemplate has not produced a claim yet.
- `gpu/large` is the `firstAvailable` alternative that was chosen.
- Claims generated from templates are resolved the way the scheduler does it.
- Claims reserved for a PodGroup show the PodGroup as consumer.
- `-o wide` adds whether each consumer is in `status.reservedFor`, and the reason for `Missing` and `NotCreated` rows.

### kubectl dra devices

Full guide: [docs/commands/devices.md](docs/commands/devices.md)

```
kubectl dra devices [--node NODE] [--driver DRIVER] [--class CLASS] [--free] [-o wide]
```

Every device in the newest generation of every pool, with the claims using it.

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

| Status | Meaning |
|---|---|
| `Free` | Can be allocated to any request that selects it. |
| `Allocated` | Dedicated to a claim. |
| `Shared` | Used only by claims sharing it through consumable capacity. |
| `Tainted` | Not allocated, but a `NoSchedule` or `NoExecute` taint blocks requests without a matching toleration. |
| `Unavailable` | Cannot be allocated, for example because its pool is incomplete. `-o wide` shows the reason. |

Claims with admin access are listed but do not make a device busy.

- `--class` evaluates the DeviceClass's CEL selectors with the same library as the scheduler. A CEL error fails the command, because the scheduler fails every claim using that class the same way.
- `--node` keeps devices usable on that node, including network-attached ones.
- `--free` keeps only `Free` devices.
- `-o wide` adds taints (with the DeviceTaintRule that added each one), the reason for `Unavailable`, and attributes.

### kubectl dra pools

Full guide: [docs/commands/pools.md](docs/commands/pools.md)

```
kubectl dra pools [--driver DRIVER] [--node NODE] [-o wide]
```

```console
$ kubectl dra pools
DRIVER             POOL                      NODES                     GENERATION   SLICES   DEVICES   ALLOCATED   STATUS
fake.example.com   incomplete                all nodes                 1            1/2      2         0           Incomplete
gpu.example.com    kubectl-dra-e2e-worker    kubectl-dra-e2e-worker    1            1/1      8         1           Complete
gpu.example.com    kubectl-dra-e2e-worker2   kubectl-dra-e2e-worker2   1            1/1      8         2           Complete
```

The scheduler only allocates from a pool once every ResourceSlice of its
newest generation is published. An incomplete pool fails silently: its
devices simply never get picked. `SLICES` is slices seen over
`resourceSliceCount`.

| Status | Meaning |
|---|---|
| `Complete` | All slices are present; the pool is usable. |
| `Incomplete` | Slices are missing, so nothing in the pool can be allocated. |
| `Invalid` | Duplicate device names, or slices that disagree on `resourceSliceCount`. |

`-o wide` adds outdated slices from older generations and the problem text.

### kubectl dra nodes

Full guide: [docs/commands/nodes.md](docs/commands/nodes.md)

```
kubectl dra nodes [--issues] [-o wide]
```

DRA devices per node, and nodes that look misconfigured.

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

`DEVICES` counts devices local to the node; network-attached devices it can
reach are shown separately with `-o wide`. `ISSUES` flags:

- a driver's kubelet plugin runs on the node, but the driver publishes no ResourceSlice for it
- a kubelet plugin pod on the node is not Ready
- the node is NotReady, so its ResourceSlices may be stale
- a pool local to the node is incomplete or invalid
- ResourceSlices name a node that does not exist

The API does not link a driver's plugin pods to the driver. `nodes` finds
kubelet plugin pods by their `plugins_registry` mount, and then:

- matches a plugin to a driver by a mounted `plugins/<driver>` directory when there is one;
- otherwise infers the match from the nodes the plugin runs on, and says "seems to be". CSI node plugins are excluded, so they are never taken for a DRA driver.

A driver that publishes no slices anywhere cannot be matched. `nodes` lists
pods in all namespaces.

## Install

kubectl-dra is not yet in the krew index. Once it is:

```bash
kubectl krew install dra
```

From a release: download the archive for your platform from the
[Releases](https://github.com/kphatak/kubectl-dra/releases) page (for example
`kubectl-dra_v0.1.0_darwin_arm64.tar.gz`), extract it, and put
`kubectl-dra` on your `PATH`. Each release lists the archives' checksums.

From source, with Go 1.27 or newer:

```bash
go install github.com/kphatak/kubectl-dra/cmd/kubectl-dra@latest
```

Or from a checkout:

```bash
make build
```

Then put `bin/kubectl-dra` on your `PATH`. kubectl finds it as `kubectl dra`.

## Permissions

kubectl-dra only lists and reads. It never creates, changes or deletes
anything, and it installs nothing in the cluster. This ClusterRole covers
every command:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubectl-dra-viewer
rules:
- apiGroups: [""]
  resources: ["nodes", "pods", "events"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["resource.k8s.io"]
  resources:
  - deviceclasses
  - resourceslices
  - resourceclaims
  - resourceclaimtemplates
  - devicetaintrules
  verbs: ["get", "list", "watch"]
```

ResourceClaims are always listed in all namespaces, even with `-n`, because a
device held by a claim in another namespace is still unavailable. If
ResourceClaimTemplates, DeviceTaintRules or events cannot be listed, commands
continue and print a warning. See [docs/rbac.md](docs/rbac.md).

## Supported versions

| | |
|---|---|
| Kubernetes | 1.34 and newer (`resource.k8s.io/v1`) |
| DRA API | `resource.k8s.io/v1` only |
| Tested on | 1.34.11 and 1.37.0, with kind and [dra-example-driver](https://github.com/kubernetes-sigs/dra-example-driver) v0.5.0 |
| Built against | `k8s.io/*` v0.37.2 |

### Clusters without DRA

Every command checks first, with one discovery request, that the API server
serves `resource.k8s.io/v1`. If it does not, the command stops before
listing anything, prints the reason on stderr, writes nothing to stdout
(so `-o json` pipelines never see half a document), and exits with code 1.
Against a kind cluster that took about a second on the first call and a few
hundredths of a second after that.

The message tells "too old" apart from "turned off":

```console
$ kubectl dra pools     # Kubernetes 1.33, which only has the beta API
Error: the cluster runs Kubernetes v1.33.4 and does not serve resource.k8s.io/v1 (it serves only v1beta2); kubectl-dra needs Kubernetes 1.34 or newer

$ kubectl dra pools     # Kubernetes 1.34 with the API disabled
Error: the cluster runs Kubernetes v1.34.11 but does not serve resource.k8s.io/v1 (DRA is not enabled); check the API server's --runtime-config and DRA feature gates
```

A cluster that serves the API but has no DRA driver installed is not an
error: the commands run and show empty results (`No pools found.`), which
is the correct answer.

`kubectl dra version` and `--help` work without contacting a cluster. For an
API server that is unreachable rather than missing DRA, kubectl's usual
connection errors apply; pass `--request-timeout=10s` to bound the wait, as
with kubectl itself.

### Feature coverage

Newer DRA features are recognized when present, and the allocator check in
`why` accounts for all of them. The funnel explains prioritized lists, admin
access, device taints and DeviceTaintRules, and PodGroup reservations. It
does not yet explain shared counters (partitionable devices) or consumable
capacity: for those, the allocator's verdict is shown without a breakdown.

## Compared with plain kubectl

| Question | Plain kubectl | kubectl-dra |
|---|---|---|
| Why is my claim pending? | Read the claim, every slice and every class; evaluate CEL in your head | `kubectl dra why CLAIM`: funnel, the failing CEL expression with the device's values, and the allocator's verdict |
| Why is my pod pending when its claim is allocated? | Compare the allocation's node selector with the pod's constraints | `kubectl dra why pod/POD` |
| Which device did my pod get? | Follow the pod's status to a generated claim name, then read its allocation | `kubectl dra map` |
| Which devices are free? | Not available: build allocated state from every claim in every namespace | `kubectl dra devices --free` |
| Which devices does this DeviceClass match? | Not available without a CEL evaluator | `kubectl dra devices --class CLASS` |
| Is a driver's pool complete? | Count slices per generation by hand against `resourceSliceCount` | `kubectl dra pools` |
| Which nodes should publish devices but do not? | Correlate driver pods with slices node by node | `kubectl dra nodes --issues` |

## How it works

- **One snapshot per command.** Nodes, pods, DeviceClasses, ResourceSlices, ResourceClaims, templates, DeviceTaintRules and (for `why`) events are listed once, in parallel and paged. Analysis then runs on that snapshot without further API calls.
- **Upstream libraries, not a reimplementation.** CEL selectors are evaluated with `k8s.io/dynamic-resource-allocation/cel`. Allocation decisions come from `k8s.io/dynamic-resource-allocation/structured`. Pod-to-claim resolution uses `resourceclaim.Name`. Node matching uses `structured.NodeMatches` and component-helpers.
- **The scheduler's view.** DeviceTaintRules are applied to slices the way the scheduler's slice tracker applies them. Allocated state follows kube-scheduler's own bookkeeping: admin access does not hold a device, and shared allocations count as shared.
- **Features are inferred.** The allocator's optional features are turned on when the cluster's objects use them, because the scheduler's feature gates cannot be read from outside. `why` prints the features it assumed.

The [command guide](docs/commands/README.md) explains the concepts every
command shares (pools and generations, where a device can be used, when it
counts as allocated, taints). The [glossary](docs/glossary.md) defines every
DRA and kubectl-dra term used in the output and the docs.

## Development

```bash
make build       # bin/kubectl-dra
make test        # unit and golden tests, with -race
make lint        # golangci-lint, built from source with the local Go
make e2e         # kind cluster + dra-example-driver, then the e2e tests
```

Releases are built by goreleaser when a `v*` tag is pushed; see
[docs/releasing.md](docs/releasing.md) for how to check a release locally,
cut one, and submit it to krew.

- Unit and golden tests run against YAML fixtures in [test/fixtures](test/fixtures). After an intended change to output, run `go test ./pkg/cmd -update` and review the diff in `pkg/cmd/testdata`.
- The e2e tests need Docker, kubectl, helm and git; the Makefile builds a pinned kind. `make e2e` creates the cluster with its own kubeconfig under `.e2e/` (your kubeconfig is not touched), runs the tests and deletes the cluster. Use `E2E_KEEP=1` to keep it, or `make e2e-up`, `make e2e-test` and `make e2e-down` for one phase at a time. `KIND_IMAGE` selects the Kubernetes version.

For a bug report, the hidden `dump` command writes everything kubectl-dra
reads as one YAML file:

```bash
kubectl dra dump -A -o yaml > dra-state.yaml
```

The same file loads back in tests with `snapshot.LoadFiles`. It contains pod
and node specs, so review it before sharing.

## License

Apache License 2.0.
