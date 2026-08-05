# Platform Mesh — Tilt local development environment

Tilt replaces the OCM/Flux/platform-mesh-operator delivery pipeline for the
developer inner loop: static infrastructure is deployed once into kind, and the
operators/services you are working on hot-reload in seconds.

## What it deploys today

- **Local infra** (`manifests/`): `platform-mesh-system` namespace, a
  self-signed cert-manager `Issuer`, an envoy `Gateway` named `platform-mesh`,
  and a dev etcd.
- **Remote infra** (`infra_remote.Tiltfile`, skipped by `TILT_NO_INFRA=1`):
  cert-manager, the envoy gateway controller, kcp-operator.
- **kcp** (static): the upstream kcp Tilt module `deploy_kcp()`, parameterized
  for our gateway, hostnames (`root.pm.localhost`), OIDC issuer and (in
  `auth`/`full`) the ReBAC authorization webhook. kcp always runs a **pinned
  released image** — it is never built from source here.
- **tenancy** (`tenancy`/`full` profiles): the RFC 010 tenancy tree inside kcp,
  plus the `tenancy-operator` hot-reloaded from source. See
  [Tenancy profile](#tenancy-profile).

## Prerequisites

- docker or podman, `kind`, `kubectl`, `helm`, `tilt`, `git`
- The kcp static-install module (`deploy_kcp`) is fetched automatically from the
  kcp repo — **no local kcp checkout required**. Until the upstream PR merges it
  comes from the in-flight branch (`load_kcp()` in `helpers.py`). Overrides:
  - `KCP_TILT_DIR` — path to a local `kcp/contrib/tilt` checkout; skips the
    fetch (use offline or when hacking on the module itself).
  - `KCP_TILT_REPO` / `KCP_TILT_REF` — git URL / ref to fetch from.

## Usage

```sh
# create the kind cluster first — it MUST be named platform-mesh.
# The Tiltfile refuses any context other than kind-platform-mesh (guard against
# a stray `tilt up` hitting a shared/other cluster). Override with
# TILT_ALLOWED_CONTEXT if you must.
kind create cluster --name platform-mesh
tilt up -f contrib/tilt/Tiltfile -- --profile=core

# optional: export defaults for overriding the kcp module and the helm charts path (for local development)
export HELM_CHARTS_DIR=go/src/github.com/platform-mesh/helm-charts
export KCP_TILT_DIR=go/src/github.com/kcp-dev/kcp/contrib/tilt
kind create cluster --name platform-mesh
tilt up -f contrib/tilt/Tiltfile -- --profile=core
```

### Profiles

Profiles are independent feature layers, not an ordered ladder — each only ever
*adds* resources, so **several can be requested at once**:

```sh
tilt up -f contrib/tilt/Tiltfile -- --profile=core,tenancy
tilt up -f contrib/tilt/Tiltfile -- --profile=core --profile=tenancy   # same thing
tilt up -f contrib/tilt/Tiltfile -- --profile=full                     # every profile
```

| Profile | Adds |
|---|---|
| `infra` | the base environment. **Always on** — naming it is allowed but redundant |
| `core` | nothing yet (reserved; lands with Phase 1) |
| `auth` | the ReBAC authorization webhook secret on kcp (L3) |
| `tenancy` | the RFC 010 tenancy tree in kcp + the `tenancy-operator` |
| `full` | shorthand for all of the above |

An unknown profile fails the Tiltfile rather than being silently ignored, so a typo
does not quietly give you a smaller environment than you asked for.

### Validate manifests without a cluster

`deploy_kcp()`'s output can be inspected offline — no cluster, no remote
fetches:

```sh
TILT_NO_INFRA=1 tilt alpha tiltfile-result -f contrib/tilt/Tiltfile
```

This renders the local manifests and the kcp custom resources (RootShard,
FrontProxy, TLSRoutes, Kubeconfigs) with all hooks applied, so you can confirm
the gateway wiring, OIDC issuer and authorization webhook before deploying.

## Tenancy profile

```sh
tilt up -f contrib/tilt/Tiltfile -- --profile=tenancy
```

Adds two resources on top of the base environment, in this order:

| Resource | What |
|---|---|
| `tenancy-bootstrap` | installs the tree into kcp — `system:controllers`, `system:directory`, `tenants`; the four APIExports and their APIResourceSchemas; the `organization`/`workspace` WorkspaceTypes; the two install-time APIBindings; and the operator's own kubeconfig secret |
| `tenancy-operator` | the controller, built from `operators/tenancy-operator` and live-updated |

`bootstrap/tenancy.sh` is idempotent and re-runs whenever the generated
`config/resources` or `config/bootstrap` manifests change, so
`task generate-tenancy-operator` followed by a save is enough to push a schema
edit into the cluster.

**Why the bootstrap holds admin and the operator does not.** Creating workspaces,
WorkspaceTypes and APIExports is a one-shot administrative act. The operator gets
a kubeconfig scoped to the exports workspace — enough to resolve the
`APIExportEndpointSlice`s — and everything after that goes through the virtual
workspace URLs those slices publish, bounded by each export's permission claims.
That is RFC 010 §3.10's rule that no *long-running* component keeps a cluster-admin
credential.

Poke at the result:

```sh
export KUBECONFIG=contrib/tilt/.secret/kcp/tilt-frontproxy.kubeconfig
BASE=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}' | sed -E 's#/clusters/.*$##')

kubectl --server "$BASE/clusters/root:system:controllers" get apiexports
kubectl --server "$BASE/clusters/root:system:directory"   get users,organizations,usermembershipindices
kubectl --server "$BASE/clusters/root:tenants"            get workspaces
```

Creating a `User` in the directory workspace is enough to watch the state machine
run: a personal `Organization` appears, then its workspace under `root:tenants`,
then the index row.

The operator's OIDC flags in `manifests/tenancy-operator.yaml` **must** match the
`oidc` block passed to `deploy_kcp()` in the Tiltfile. `rbacIdentity` is derived
from them, and a mismatch means every role binding written names a subject that
never authenticates — a silent 403 in a workspace the user is a member of.

## The kcp hooks

The kcp static install lives upstream in
`kcp-dev/kcp/contrib/tilt/kcp_static.Tiltfile` and exposes `deploy_kcp()`. This
project owns its own infrastructure (gateway, etcd, cert issuer) and passes it
in. Hooks exercised here:

| Hook | Purpose |
|---|---|
| `gateway` + `route_version` | attach kcp TLSRoutes to our `platform-mesh` gateway (prod: traefik) |
| `authorization_webhook_secret` | wire the ReBAC authz webhook (L3, `auth`/`full`) |
| `oidc` | front kcp with dex/keycloak as the OIDC issuer |
| `image_tag` / `image_repo` | pin the released kcp image |
| `extra_shards` | opt-in extra shards (`--sharded`) |
| `namespace`, `base_domain`, `etcd_endpoint`, `issuer_ref` | environment wiring |

## Layout

```
contrib/tilt/
  Tiltfile              # entrypoint: config, local infra, kcp, components
  infra_remote.Tiltfile # ext:// + remote-chart infra (gated by TILT_NO_INFRA)
  helpers.py            # chart_path(), component_build(), component_build_manifest()
  manifests/            # local, no-fetch infra manifests
  bootstrap/            # one-shot installers run against kcp (tenancy.sh)
  runtime.Dockerfile    # thin image for hot-reloaded binaries
  bin/ .cache/ .secret/ # gitignored working dirs
```

`component_build_manifest()` is for a component that has no published Helm chart
yet — same build and live_update as `component_build()`, deployed from a manifest
in `manifests/`. When the chart lands, switch the call site and delete the
manifest.
