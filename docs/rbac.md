# RBAC

kubectl-dra is read-only. It only issues `list` calls today; `get` and `watch`
are included so that future features (`--watch`, single-object lookups) need no
RBAC change.

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

Bind it with a ClusterRoleBinding.

## Why cluster-wide

ResourceClaims are always listed across all namespaces, even with `-n`. A device
allocated to a claim in another namespace is still unavailable, so a
namespace-scoped view would report busy devices as free and could explain an
allocation failure wrongly.

## What happens with less access

| Resource | If forbidden |
|---|---|
| nodes, pods, deviceclasses, resourceslices, resourceclaims | The command fails and names the resource. |
| resourceclaimtemplates, devicetaintrules, events | The command continues and prints a warning, because the answer may be incomplete. |

DeviceTaintRules are only listed when the API server serves them. Clusters
without that feature get no warning.
