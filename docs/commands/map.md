# kubectl dra map

```
kubectl dra map [-n NAMESPACE | -A] [-o table|wide|json|yaml]
```

Shows who uses which claim and which device each claim got: one row per
consumer and allocated device, or per consumer and request while a claim is
still pending.

## Why it matters

Following a pod to its device in plain kubectl takes several steps. The pod
spec names a claim, or a claim template. For a template, the real claim name
is generated and only appears in `pod.status.resourceClaimStatuses`. The
claim's `status.allocation` lists driver, pool and device, but not the node
in a readable form. Claims can be shared by several pods, reserved for a
PodGroup instead of a pod, or missing entirely.

`map` resolves all of this the way the scheduler does and prints the result
as a table. Use it to answer "which GPU did my job get?", "who is using this
device?" (together with [devices](devices.md)), and to spot pods whose claim
does not exist or was never created.

## Example

```console
$ kubectl dra map -A
NAMESPACE   CONSUMER                CLAIM            REQUEST     DRIVER            POOL     DEVICE   NODE     STATUS
team-a      pod/big                 big              gpus        <none>            <none>   <none>   <none>   Pending
team-a      pod/big                 big              nic         <none>            <none>   <none>   <none>   Pending
team-a      pod/not-yet             <none>           <none>      <none>            <none>   <none>   <none>   NotCreated
team-a      pod/prio                prio             gpu/large   gpu.example.com   node-b   gpu-3    node-b   Allocated
team-a      pod/train               train-gpu-x1     gpu         gpu.example.com   node-a   gpu-0    node-a   Allocated
team-a      pod/typo                does-not-exist   <none>      <none>            <none>   <none>   <none>   Missing
team-a      pod/web-1               shared-nic       nic         net.example.com   fabric   nic-0    node-a   Allocated
team-a      pod/web-2               shared-nic       nic         net.example.com   fabric   nic-0    node-b   Allocated
team-a      podgroups/train-group   workload         gpu         gpu.example.com   node-b   gpu-1    node-b   Allocated
team-a      <none>                  orphan           gpu         gpu.example.com   node-a   gpu-1    node-a   Allocated
team-b      pod/other               other            gpu         gpu.example.com   node-b   gpu-0    node-b   Allocated
```

Reading it from top to bottom:

- `big` is pending; it has two requests, so it has two rows.
- `not-yet` uses a claim template, and the claim has not been created yet.
- `prio` used a `firstAvailable` request; the scheduler chose the
  alternative `large`.
- `train`'s claim was generated from a template; `map` found its name.
- `typo` references a claim that does not exist.
- `web-1` and `web-2` share one claim, so each row shows its own pod's node.
- `workload` is reserved for a PodGroup rather than a pod.
- `orphan` is allocated, but nothing uses it, so it holds a GPU for nothing.

## Reading the output

| Column | Meaning |
|---|---|
| `NAMESPACE` | Only with `-A`. |
| `CONSUMER` | `pod/NAME` for a pod, or `RESOURCE/NAME` for another consumer from the claim's `status.reservedFor`, such as `podgroups/NAME`. `<none>` when nothing uses the claim. |
| `CLAIM` | The ResourceClaim. `<none>` while a template claim does not exist yet. |
| `REQUEST` | The request name, or `request/alternative` for a `firstAvailable` request. |
| `DRIVER`, `POOL`, `DEVICE` | The allocated device. `<none>` while pending. |
| `NODE` | The pod's node if it is scheduled. Otherwise, where the allocation can be used: a node name, `all nodes`, or a node selector. |
| `STATUS` | One of the statuses below. |

| Status | Meaning |
|---|---|
| `Allocated` | The claim has devices. One row per device. |
| `Pending` | The claim has no allocation yet. One row per request. Run [why](why.md) on it. |
| `Missing` | The pod names a ResourceClaim that does not exist. The pod cannot start until it does. |
| `NotCreated` | The pod uses a claim template, and the claim has not been created yet. Usually brief; if it stays, check that the template exists. |

`-o wide` adds `RESERVED`, whether the consumer is listed in the claim's
`status.reservedFor` (that is, actually using the claim now), and `REASON`
for `Missing` and `NotCreated` rows.

Rows are grouped by namespace and consumer. Claims that nothing uses come
last within each namespace.

## Flags

Only `-n` and `-A`. Without either, the current namespace from your
kubeconfig is used. Claims are loaded from every namespace (see
[concepts](README.md#claims-are-always-cluster-wide)) but only those in
scope are printed.

## How it works

1. **Pod references.** For every pod in scope and every entry in
   `pod.spec.resourceClaims`, the claim name is resolved with the upstream
   helper `resourceclaim.Name`, the same function the scheduler uses:
   - A `resourceClaimName` is used as is.
   - For a `resourceClaimTemplateName`, the generated name is read from
     `pod.status.resourceClaimStatuses`. If there is no entry yet, the row is
     `NotCreated`.
   - If the status entry exists but has no name, the claim was deliberately
     not created and the reference is skipped.
2. **Ownership.** A claim generated from a template must be owned by the pod
   (or by the pod's PodGroup), checked with `resourceclaim.IsForPod`. If it
   is not, the claim with that name is not the pod's claim, and the row is
   `Missing` with the reason.
3. **Reservations.** Every entry in each claim's `status.reservedFor` is
   added as a consumer. This is how PodGroup consumers appear, even though
   kubectl-dra does not list PodGroups.
4. **Merging.** A pod found through its spec and through `reservedFor` is
   shown once, but only if the UIDs match. A reservation left by a deleted
   pod stays a separate row, so a new pod with the same name never looks
   reserved when it is not.
5. **Rows.** Each consumer gets a row per allocated device, or per request
   while the claim is pending.

## Limits

- PodGroups are not listed, so a PodGroup only appears once it has reserved
  a claim. Pending claims of a PodGroup show no consumer.
- `NODE` for a pod that is not scheduled comes from the allocation. A claim
  allocated for one node and a pod that cannot run there is a real problem;
  [why](why.md) detects it.

## JSON fields

`{"rows": [...]}`, one object per table row:

| Field | Type | Notes |
|---|---|---|
| `namespace` | string | |
| `consumer` | string | Omitted when nothing uses the claim. |
| `reserved` | bool | |
| `claim`, `template` | string | `template` is set on `Missing` and `NotCreated` rows for template claims. |
| `request`, `subRequest` | string | `subRequest` for `firstAvailable` alternatives. |
| `driver`, `pool`, `device` | string | Omitted while pending. |
| `adminAccess` | bool | The device is held with admin access. |
| `node` | string | |
| `status` | string | As above. |
| `reason` | string | For `Missing` and `NotCreated`. |

Find allocated claims that nothing uses:

```bash
kubectl dra map -A -o json | jq -r '.rows[] | select(.consumer == null and .status == "Allocated") | "\(.namespace)/\(.claim)"'
```

## Related

- [why](why.md) explains `Pending` rows.
- [devices](devices.md) shows the same allocations from the devices' side.
