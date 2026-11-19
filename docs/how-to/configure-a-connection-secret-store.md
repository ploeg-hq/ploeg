---
type: how-to
audience: [operator]
owner: ploeg
last_verified: 2026-11-18
verified_by: "Read pkg/secretstore, pkg/secretstore/openbao, pkg/secretstore/kubernetes, cmd/ploegd/secret_store.go and ops/helm/ploeg; go test ./pkg/secretstore/... ./cmd/ploegd/; scripts/helm-golden.sh check"
---

# Configure a connection secret store

Use this before you let an operator consumer make tracker or forge connections through the operator API. Ploeg writes the secrets of those connections (access and refresh tokens, a GitHub App's private key and webhook secret) into a store and keeps only a reference (store, path, version) in its database, logs, responses and events ([ADR-0184](../adrs/0184-tracker-and-forge-connection-secrets-live-in-a-secret-store-ploeg-writes-itself-and-no-person-handles-them.md)).

Connections configured through environment variables or the Secrets named in the chart's values never use the store and keep working without one. Without a store, every call that would write a connection secret fails closed and writes nothing.

## Choose a store

| `secretStore.type` | Use it when | Ploeg's reach |
| --- | --- | --- |
| `openbao` | The cluster runs OpenBao or Vault | One KV v2 path prefix, through a Kubernetes auth role whose policy names only that prefix |
| `kubernetes` | There is no vault | Secrets in one namespace, through a namespaced Role. RBAC cannot limit a Role to the Secrets Ploeg created, so give it a namespace that holds nothing else; ploegd itself refuses to read, change or delete a Secret without its labels |

Either type mounts ploegd's ServiceAccount token, which both adapters sign in with, and opens ploegd's network policy to the store (`networkPolicy.secretStore` for OpenBao, `networkPolicy.kubernetesApi` for the API server).

## OpenBao or Vault

1. Enable a KV v2 mount if you have none (`bao secrets enable -path=secret kv-v2`) and Kubernetes auth (`bao auth enable kubernetes`, then `bao write auth/kubernetes/config kubernetes_host=https://kubernetes.default.svc`).
2. Write a policy limited to Ploeg's prefix. With the defaults (`kvMount: secret`, `pathPrefix: ploeg`):

   ```hcl
   path "secret/data/ploeg/*" {
     capabilities = ["create", "read", "update", "delete"]
   }
   path "secret/metadata/ploeg/*" {
     capabilities = ["read", "list", "delete"]
   }
   path "secret/metadata/ploeg" {
     capabilities = ["list"]
   }
   ```

   `bao policy write ploeg-connections ploeg-connections.hcl`
3. Bind a role to ploegd's ServiceAccount (the release's full name) and that policy:

   ```sh
   bao write auth/kubernetes/role/ploeg \
     bound_service_account_names=ploeg \
     bound_service_account_namespaces=ploeg \
     token_policies=ploeg-connections token_ttl=1h
   ```

4. Set the values and roll out:

   ```yaml
   secretStore:
     type: openbao
     openbao:
       address: https://openbao.openbao.svc:8200
       role: ploeg
   ```

   ploegd accepts only an `https://` address and never follows a redirect, because every request carries its token. For a test cluster without TLS, `secretStore.openbao.allowInsecureHttp: true` accepts `http://`.

## Kubernetes Secrets

```yaml
secretStore:
  type: kubernetes
  kubernetes:
    namespace: ploeg-connection-secrets
```

Create the namespace first. The chart refuses an empty `namespace` and the release namespace, which holds the database URL, the gateway key and the forge credentials. It renders a Role (`get`, `list`, `create`, `update`, `delete` on Secrets) and a RoleBinding to ploegd's ServiceAccount in it. Kubernetes RBAC cannot narrow that Role further: a label selector on `list` is not enforced, and `resourceNames` cannot cover `create` or `list`, nor names ploegd derives at run time, so the namespace must hold nothing else. Each connection is one Secret named `ploeg-secret-<hash>`, labelled `app.kubernetes.io/managed-by: ploeg` and `ploeg.dev/secret-store: connection`, with its path and version in annotations. A Secret keeps only its latest version.

## Verify

* ploegd logs `connection secret store configured` with `store=openbao` or `store=kubernetes` at boot, and never a value.
* A wrong `secretStore.type` stops ploegd at boot; a missing `secretStore.openbao.address` or `role` fails the render.

| Symptom | Cause | Fix |
| --- | --- | --- |
| Connection calls answer `503 secret_store_unavailable` | No store configured, or ploegd cannot sign in | Set `secretStore`; for OpenBao, check the role's bound ServiceAccount and namespace |
| OpenBao answers 403 on writes | The policy does not cover `pathPrefix` | Align the policy paths with `kvMount` and `pathPrefix` |
| Kubernetes answers 403 | The Role is in another namespace than `secretStore.kubernetes.namespace` | Let the chart render the Role; do not set `PLOEG_SECRET_STORE_KUBERNETES_NAMESPACE` by hand |
