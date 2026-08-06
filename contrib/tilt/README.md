# Platform Mesh — Tilt local development environment

Tilt replaces the OCM/Flux/platform-mesh-operator delivery pipeline for the
developer inner loop: static infrastructure is deployed once into kind, and the
operators/services you are working on hot-reload in seconds.

## What it deploys today

- **Local infra** (`manifests/`): `platform-mesh-system` namespace, a
  self-signed cert-manager `Issuer`, an envoy `Gateway` named `platform-mesh`,
  and a dev etcd.
- **Remote infra** (`infra_remote.Tiltfile`, skipped by `TILT_NO_INFRA=1`):
  cert-manager, the envoy gateway controller, Flux and the OCM controller. The
  upstream kcp-operator sits in its own `infra_kcp_operator.Tiltfile` because
  the `deployer` profile replaces it.
- **kcp** (static): the upstream kcp Tilt module `deploy_kcp()`, parameterized
  for our gateway, hostnames (`root.pm.localhost`), OIDC issuer and (in
  `auth`/`full`) the ReBAC authorization webhook. kcp always runs a **pinned
  released image** — it is never built from source here.
- **tenancy** (`tenancy`/`full` profiles): the RFC 010 tenancy tree inside kcp,
  plus the `tenancy-operator` hot-reloaded from source. See
  [Tenancy profile](#tenancy-profile).
- **deployer** (`deployer` profile): `platform-mesh-deployer` hot-reloaded from
  source, building kcp from a `PlatformMesh` **instead of** the static install.
  See [Deployer profile](#deployer-profile).

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
| `deployer` | **replaces** the static kcp install — see below. Not part of `full` |

An unknown profile fails the Tiltfile rather than being silently ignored, so a typo
does not quietly give you a smaller environment than you asked for.

`deployer` is the one profile that does not compose. It swaps the bottom layer
out rather than adding to it, so it is excluded from `full` and combining it with
`tenancy` fails the Tiltfile.

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

## Deployer profile

```sh
kind create cluster --name platform-mesh
tilt up -f contrib/tilt/Tiltfile -- --profile=deployer
```

Runs `platform-mesh-deployer` for real: you create a `PlatformMesh`, the deployer
compiles it into kcp-operator admin CRs, and kcp appears. The wiring lives at
[`operators/platform-mesh-deployer/Tiltfile`](../../operators/platform-mesh-deployer/Tiltfile);
this profile is the same shape as `test/e2e`'s `TestSingleCluster`, with the one
cluster acting as its own config plane and workload cluster.

### Why it replaces the static kcp install

The deployer's job *is* the thing `deploy_kcp()` does by hand. Two consequences,
both enforced by the Tiltfile rather than left to discover:

- `deploy_kcp()` and the deployer would own the same shards, so the static
  install and the plaintext dev etcd are switched off.
- The deployer needs the **ntnn/kcp-operator fork** — split config/workload
  controllers plus the `Compiled*` CRDs its admin CRs compile into, pinned by the
  `replace` in the module's `go.mod`. It owns the same `operator.kcp.io` CRDs and
  the same Deployment as the upstream operator, so the upstream one is switched
  off and the fork installed in its place. **Bump the fork ref in
  `config/bases/kcp-operator/*` and the `go.mod` replace together.**

`--profile=deployer,tenancy` fails: `tenancy` installs into the static kcp and
reads the admin kubeconfig that install publishes, neither of which exists here.
Run them as two environments.

### Addressing

Everything is addressed over **sslip.io on the node IP**, not `*.pm.localhost`:

| | |
|---|---|
| cluster ID | the node's InternalIP, dashed (`172-18-0-2`). Override with `DEPLOYER_CLUSTER_ID` |
| hostnames | `root.<id>.sslip.io`, `fp.<id>.sslip.io`, `etcd.<id>.sslip.io`, … |
| port | `31443`, the gateway's pinned NodePort |

kcp advertises these hostnames and clients dial them verbatim, so they have to
resolve identically inside and outside the cluster — which a `.svc` name and a
`localhost` name both fail to do. That is also why this profile brings its own
`eg` gateway: the base env's `platform-mesh` gateway only accepts
`*.pm.localhost`, so an sslip.io TLSRoute would never attach to it.

The `deployer-dns` resource patches CoreDNS to answer `*.sslip.io` locally. Without
it, kind forwards to the host resolver, which commonly drops answers pointing at
private IPs (DNS rebinding protection) — and every kcp dial fails with
`no such host`.

### Resources

| Resource | What |
|---|---|
| `deployer-cert-issuer` | the self-signed `kcp` ClusterIssuer the shard CAs root from |
| `deployer-gateway` | the `eg` Gateway + the pinned NodePort |
| `deployer-dns` | CoreDNS `*.sslip.io` answers |
| `deployer-etcd` | mutual-TLS etcd, reachable through the gateway |
| `kcp-operator` | the ntnn fork |
| `platform-mesh-deployer` | the operator, built from source and live-updated, plus its CRDs and RBAC |
| `deployer-engage` | the kubeconfig Secret that engages this cluster as its own workload cluster |
| `platformmesh` | the dev `PlatformMesh` and the two topology templates |
| `deployer-admin` | an admin kubeconfig for the built kcp, written to `.secret/kcp/` |

`deployer-engage` is the one with no counterpart in the other profiles. The
deployer reaches every workload cluster through a labelled kubeconfig Secret and
has no "…and also myself" case, so the single-cluster setup hands it a
ServiceAccount kubeconfig pointing back at the cluster it already runs in. The
Secret's **name is load-bearing**: `<platformMesh>--<clusterID>`, and the half
after `--` is what `cluster` resolves to in the CEL templates above.

Watch it build, on the management cluster:

```sh
kubectl -n platform-mesh-system get platformmesh dev -o yaml
kubectl -n platform-mesh-system get rootshards,shards,frontproxies
kubectl -n platform-mesh-system get compiledrootshards,compiledshards,compiledfrontproxies
kubectl -n platform-mesh-system get deployments
```

### Poking the kcp it built

`deployer-admin` writes an admin kubeconfig once the front proxy is up:

```sh
export KUBECONFIG=contrib/tilt/.secret/kcp/tilt-dev-admin.kubeconfig

kubectl get workspaces
kubectl get shards.core.kcp.io
kubectl get logicalcluster
```

It points at `https://fp.<id>.sslip.io:31443/clusters/root` and needs **no
port-forward and no extra envoy config** — the sslip.io hostname resolves to the
node and the gateway's NodePort serves it, which is the whole reason for that
addressing.

> **Use `tilt-dev-admin.kubeconfig`, not `tilt-frontproxy.kubeconfig`.** Both
> profiles write into `.secret/kcp/`, and the static profile's files are left
> behind when you switch. They point at `pm.localhost:8443`, which under
> `deployer` serves nothing — `deploy_kcp()` is gated off, so there are no kcp
> routes on the `platform-mesh` gateway. A stale `KUBECONFIG` export from an
> earlier session shows up as:
>
> ```
> The connection to the server pm.localhost:8443 was refused
> ```
>
> That is the wrong file, not a broken gateway. `kubectl config view --minify -o
> jsonpath='{.clusters[0].cluster.server}'` tells you which one you are on.

The credential is minted by kcp-operator: the resource creates a `Kubeconfig` CR
naming the front proxy, user `tilt-admin` and group `system:kcp:admin`, and the
operator issues the cert and writes a ready-to-use kubeconfig into a Secret.
Deliberately a *different* credential from the `dev-provisioner` Kubeconfig the
deployer mints for itself — sharing that one would make hand-run commands
indistinguishable from the controller's own writes, and expiring it to clean up
would break the running operator.

The front proxy's name is generated (`dev-fp-0fdfeb` — `names.FrontProxy()`
hashes the PlatformMesh, front proxy and cluster ID into it), so the resource
selects it by the `deploy.platform-mesh.io/{platform-mesh,component}` labels the
deployer stamps on everything it generates rather than by a name written down
in advance.

Set `PLATFORM_MESH_NAME` to rename the installation (it prefixes the engage
Secret and every generated shard, so it renames the tree rather than adding one).

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
  Tiltfile                    # entrypoint: config, local infra, kcp, components
  infra_remote.Tiltfile       # ext:// + remote-chart infra (gated by TILT_NO_INFRA)
  infra_kcp_operator.Tiltfile # upstream kcp-operator (off under --profile=deployer)
  helpers.py                  # chart_path(), component_binary(), component_build()
  manifests/                  # local, no-fetch infra manifests
  bootstrap/                  # one-shot installers run against kcp (tenancy.sh)
  runtime.Dockerfile          # thin image for hot-reloaded binaries
  bin/ .cache/ .secret/       # gitignored working dirs
```

Per-component wiring lives **with the component**, not here — see
`operators/tenancy-operator/Tiltfile` and
`operators/platform-mesh-deployer/Tiltfile`. This Tiltfile only decides whether
to deploy them and supplies what is environment-wide. Because those files are
loaded with `load_dynamic()`, their relative paths resolve against *this*
directory, which is why they take `repo_root` and are handed `component_build` /
`component_binary` rather than loading `helpers.py` themselves.

`component_build()` deploys a component from its production Helm chart.
`component_binary()` is the build half on its own — same compile, image and
live_update sync — for a component whose manifests are not a chart;
`platform-mesh-deployer` uses it and then applies its own `config/default`
kustomization.
