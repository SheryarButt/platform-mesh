# tenancy-operator

The controller half of [RFC 010 — Tenants and Tenancy Model][rfc].

The model adds exactly two things to the platform: a **tenancy virtual workspace**
(the singleton clients write intent to) and a **tenancy controller** (which acts on
that intent across the fleet). Both live here — the virtual workspace under
`internal/virtualworkspace`, served by `tenancy-operator virtual-workspace`.

kcp stays exposed. Nothing here sits in a request path, and nothing here decides
whether a tenant may do something: kcp RBAC does, against the real user's token.
This operator's whole job is to make the RBAC that RBAC then enforces.

[rfc]: https://github.com/platform-mesh/architecture/blob/main/rfc/010_organizations-and-tenancy-model.md

## The model in one screen

```
root                                    ← configurable; the platform must not claim root:
├── tenants/                            the fleet. BINDS tenancy-provisioner
│   └── <tenant-uuid>/                     WorkspaceType: tenant
│       │                                 BINDS tenancy (+ tenancy-provisioner via initializer)
│       │                                 holds: Membership objects
│       │                                 tenants have NO RBAC here, so they cannot reach it
│       └── <project-uuid>/             WorkspaceType: project
│                                         BINDS tenancy-access (claims only, no new API)
│                                         holds: all tenant work
└── system/
    ├── controllers/                    DEFINES all four APIExports. Stores nothing.
    └── directory/                      BINDS tenancy-platform
                                          holds: User / Tenant / UserMembershipIndex
```

Two tiers, both enforced by kcp:

- **The Tenant tier is unreachable to tenants** — not because something
  intercepts the request, but because nothing ever created a role binding for their
  identity in that logical cluster. That survives a new client, a forgotten code
  path and a misconfigured route, because it is not a check at all.
- **The child tier is ordinary kcp RBAC.** A `Membership` causes a binding;
  removing it removes the binding. A user's reachable workspaces are exactly the
  workspaces they have bindings in — no allow-list, no index on the request path,
  no cache that can go stale against a revocation.

`metadata.name` is server-assigned everywhere (a UUID by default — see [Object
naming is pluggable](#object-naming-is-pluggable)); `displayName` is metadata. Two
tenants may share a display name, and renaming never moves a path.

### Four exports, not one

An `APIBinding` imports **all** of an export's resources — there is no subsetting.
One `tenancy` export would therefore mean that binding it in a Tenant (which
every Tenant must do to store Memberships) also makes `User` and
`Tenant` servable there. So the group is served by four exports:

| export | schemas | claims | bound in |
|---|---|---|---|
| `tenancy` | `Membership` | — | every Tenant |
| `tenancy-platform` | `User`, `Tenant`, `UserMembershipIndex` | — | the directory only |
| `tenancy-provisioner` | **none** | `tenancy.kcp.io/workspaces` | fleet root + every Tenant |
| `tenancy-access` | **none** | namespaces, serviceaccounts, RBAC | every child workspace |

Two split by **audience**, two by **capability** — *provision a workspace* versus
*reach inside one*. That makes "a tenant workspace cannot serve a `User`" a property
of the API surface rather than a rule someone has to remember, and it means the two
most dangerous powers in the system are named, granted and revoked separately.

### No cluster-admin, by construction

Every write goes through an APIExport virtual workspace with declared claims, so a
bug here is bounded by a claim list an operator can read — and read *per workspace*,
with `kubectl get apibindings`. The alternative, writing into child workspaces with
an admin client, makes that blast radius "everything".

This is why the process runs **more than one manager**: a controller cannot watch
across logical clusters by wishing, and kcp gives it exactly one wildcard endpoint
per export. One manager per export is the concrete cost of the split above.

## Layout

| path | what |
|---|---|
| `cmd/` | cobra entrypoint; builds one manager per APIExport |
| `internal/config/` | every knob, including the configurable workspace roots |
| `internal/controller/` | one package per controller; see [file naming](#controller-file-naming) |
| `pkg/paths/` | **the** source of the workspace layout — never concatenate a path elsewhere |
| `pkg/naming/` | how server-assigned Tenant/Project names are minted; pluggable |
| `pkg/identity/` | the `rbacIdentity` mirror and the subject hash |
| `pkg/membership/` | deterministic Membership names |
| `config/crd/` | generated CRDs (also the input to the APIExport generator) |
| `internal/bootstrap/` | the installer behind `tenancy-operator init` |
| `deploy/kcp/` | the manifests it applies — **embedded** into the binary via `deploy/embed.go` |
| `deploy/helm/` | the chart, which runs `init` as an init container or a Job |

### Object naming is pluggable

`metadata.name` on a Tenant or Project is server-assigned and a client can
never supply it — the name is also the kcp Workspace name, so choosing it is
choosing a path. What *is* configurable is which server-assigned name,
via `--tenancy-naming-strategy`:

| strategy | example | unique by |
|---|---|---|
| `uuid` (default) | `7a1f…-…` | 122 bits of randomness; never collides |
| `base36` | `k3f9q2m1x7t0b` | 64 bits, kcp's own identifier shape |
| `words` | `ruby-lunar-plateau` | ~258k triples (64x63x64), then a suffix on collision |
| `displayname` | `acme-co` | the slug, then a suffix on collision |

Collision handling is not the strategy's problem: `naming.Apply` creates, retries
on `AlreadyExists` with an incremented `Attempt`, and gives up when the strategy
returns `ErrExhausted`. A strategy is therefore a pure function that never talks
to the API server.

Two things to know before switching away from `uuid`:

- **It is not retroactive, and cannot be.** Existing objects keep their names;
  renaming would move a workspace. A cluster that switches ends up with a mix.
- **`displayname` publishes tenant input into paths.** The display name ends up
  in workspace paths, kubeconfig server URLs, logs and error messages. It is
  defensible for Projects, which are unique only within one Tenant, and
  much less so for Tenants, where the first tenant to create `platform`
  holds that name against the whole platform.

An installation with its own scheme implements `naming.Strategy` and calls
`naming.Register` from an `init`; the interface is deliberately small so that
this does not require forking anything.

### Controller file naming

A controller and the reconcilers it runs are named after the controller, so the
file list alone says how many reconcilers a controller has and what each one does:

```
<controller>_controller.go              # the controller: struct, New*, SetupWithManager, Reconcile
<controller>_reconcile_<reconciler>.go  # one file per chain.Reconciler step
```

Three reconcilers is therefore four files:

```
internal/controller/tenants/
  organization_controller.go
  organization_reconcile_index.go
  organization_reconcile_ownermembership.go
  organization_reconcile_workspace.go
```

`<controller>` is the singular resource reconciled, **not** the package name. That
is what disambiguates a package hosting more than one controller: `memberships/`
holds both `membership_controller.go` and `binding_controller.go`, and the name
says which one you are opening.

Files that are neither a controller nor a reconciler keep their own descriptive
name — the shared step runner (`chain/chain.go`) and helpers a reconciler happens
to call (`memberships/roles.go`). Naming those `_reconcile_` would make the file
name lie about what is in it.

This convention is local to this operator; the rest of the repo has not adopted it.

### Two things that must not drift

**`pkg/paths`.** Every root is a flag. The platform must not claim `root:` (kcp's
root may already belong to someone else's tree), and two installs on one kcp need
disjoint subtrees *and* disjoint export workspaces, because an APIBinding references
its export **by path**. A sub-tree install is `--paths-root=root:acme:platform` and
nothing else. What is *not* configurable is the shape: a Tenant is always a
direct child of the fleet root, a Workspace always a direct child of a Tenant.

**`pkg/identity`.** `User.spec.rbacIdentity` is a *mirror* of kcp's own authenticator
configuration — `UsernamePrefix` + the value of `UsernameClaim`. `pm:alice@acme.example`
is what one deployment happens to produce, not a format this model owns. If this
operator and kcp disagree, every `ClusterRoleBinding` written names a subject that
never authenticates: the user is silently 403'd in a workspace they are a member of,
with a Membership and a binding that both look correct. Set
`--oidc-username-claim` / `--oidc-username-prefix` from the same source as kcp's.

Choosing a *mutable* claim like `email` extends that hazard to a single user — an
address change invalidates their bindings. The operator logs a warning at boot when
the configured claim is mutable. The `User` object itself is safe either way: it is
keyed on `hash(issuer + "/" + sub)`, which no claim convention affects.

## Two commands, one binary

```
tenancy-operator init       install the tree into kcp, then exit
tenancy-operator operator   reconcile it
```

`init` is the only thing that needs kcp admin — creating workspaces, APIExports,
WorkspaceTypes and the two install-time APIBindings does. It runs as an **init
container**, so the credential leaves when that container exits; the manager that
follows reaches the fleet only through APIExport virtual workspaces, bounded by
permission claims. It is idempotent, so it runs on every pod start and upgrade.

Both read the **same** `--paths-*` flags (they are persistent flags on the root
command, and the chart renders them from one value). An installer that builds one
tree while the operator watches another would leave a healthy-looking pod
reconciling nothing, so the arrangement makes that impossible rather than
documenting it.

The manifests `init` applies live in `deploy/kcp/` and are **embedded** in the
binary rather than mounted: the binary is the unit of delivery, and there is no way
to pair one version of the installer with a different version of the schemas it
installs. `task generate` writes straight there, so regenerating the API and
rebuilding the installer are the same act.

`deploy/` is a Go package for exactly one reason — `//go:embed` cannot reach
outside its own directory. That is what lets the manifests sit at a path a reader
would look in, rather than under `internal/`, while still being compiled in.

The mounted kubeconfig needs no pre-scoping either — both commands append
`/clusters/<exports>` themselves from the layout, so one credential serves both and
neither can be aimed at a workspace the other did not use.

Run it by hand against any kcp:

```sh
KUBECONFIG=<kcp-admin> go run . init --paths-root=root:tenancy
```

## Signing in locally

`tenancyctl` is a **developer** CLI — an OIDC client and nothing more. Against the
`tenancy` Tilt profile, three tasks cover the loop:

```sh
task dev-login       # browser → dex → token cached in ~/.tenancy
task dev-whoami      # what the token says, and the User name it derives
CLUSTER=<id> task dev-kubeconfig
```

`dev-login` pulls dex's CA and kcp's CA out of the running cluster first (into the
gitignored `contrib/tilt/.secret/kcp/`), because dex's serving cert is signed by a
private CA. The dev credentials are `dex@pm.localhost` / `dex`. Your **browser**
must trust that CA too, or the issuer URL shows a warning — the `--oidc-ca-file`
flag only covers the CLI.

Two constraints worth knowing before they surprise you:

- **Port 8000 is fixed.** dex registers `http://127.0.0.1:8000` as this client's
  only loopback redirect, so the CLI cannot fall back to an ephemeral port the way
  RFC 8252 would prefer. If something else holds 8000, free it.
- **`dev-kubeconfig` requires `CLUSTER`.** There is no default workspace, so
  it refuses rather than picking. The task prints how to list what you can reach.

`dev-kubeconfig` writes the CLI in as a **credential plugin**, so tokens refresh
without a browser. That is why these tasks build to `bin/tenancyctl` instead of
using `go run`: `go run`'s binary lives in a temp directory that is deleted on
exit, so a kubeconfig pointing at it would work once and then break.

Signing in **provisions nothing**. `dev-whoami` shows the `sha256(issuer + "\n" +
sub)` that a `User` object *will* have; the object itself appears only when
something calls `create users` against the tenancy virtual workspace.

## What runs today

The first slice of the bootstrap state machine:

```
User appears (an explicit `create users` against the VW — never a side
              effect of a read)
 └─ PersonalOrgReady   create the personal Tenant           [UserReconciler]

Tenant appears
 ├─ WorkspaceReady     create <fleet-root>:<tenant-uuid>, resolve its cluster ID
 └─ IndexSynced        write the tenant-scope row into the owner's index
                                                          [TenantReconciler]
```

Each subroutine reports its own condition, so a stalled Tenant shows *which*
step it is on rather than one opaque `Ready=False`. Nobody blocks on any of it: a
client watches and rows appear as they become Ready. Cold start is seconds, and a
brand-new user simply has no memberships until it finishes — kcp denies them, which
is the correct answer and needs no special-casing.

Every step is idempotent and re-checks for existing state before creating, because
kcp workspace initializers are async with no rollback. That is a hard requirement,
not a style choice.

## Roadmap

Ordered by what unblocks the most. The first two are what make the model actually
work end to end.

1. **Membership reconciler + RBAC symmetry** — the fleet-wide watch over the
   `tenancy` export, writing a `ClusterRoleBinding` per Membership into the target
   workspace through `tenancy-access`, and **removing it when the Membership goes**.
   The most urgent item: today's reference implementation
   deletes the index row and leaves the binding, and since kcp RBAC is the only
   thing that authorizes, *a stale binding is live access*. Ships with the three
   `platform:project:{admin,member,viewer}` ClusterRoles, so each tier is
   genuinely less than the one above rather than all mapping to cluster-admin.
2. **Child Workspace provisioning through the workspace initializer** — steps 4–7
   (`WorkspaceCreated`, `NamespaceReady`, `WorkspaceAdminReady`,
   `ProfileReady`). These run inside the initializer via the
   `initializingworkspaces` virtual workspace, which is the only answer to the
   chicken-and-egg of stamping the first binding in a workspace nothing can yet
   write to. A tenant never observes a half-built workspace.
3. **The tenancy virtual workspace** — the singleton every client
   talks to, and the only thing that materializes a `User`. Not in this repository
   yet. Blocked on the cross-shard spike, which is the one open question that can
   invalidate the design.
4. **Membership pruning on Project delete** — deleting a child Workspace does not
   dispose of its Memberships implicitly, because they live one tier up in the
   Tenant. The Project reconciler must prune workspace-scope rows explicitly;
   it does not yet.
5. **Group→Membership sync** and **group-based platform admin** —
   same claim plumbing, worth doing in one pass.

Deletion is **hard**: there is no soft-delete window, no `spec.lifecycle`, and no
undelete. Deleting a Tenant or Project deletes it, and the finalizer chain
is what makes the cascade orderly rather than a grace period.

## Development

```sh
task build-tenancy-operator      # compile
task test-tenancy-operator       # unit tests
task lint-tenancy-operator       # fmt + golangci-lint
task generate-tenancy-operator   # CRDs, deepcopy, and the four APIExports
```

Locally, the `tenancy` Tilt profile brings up kcp, bootstraps the tree, and
hot-reloads this operator:

```sh
tilt up -f contrib/tilt/Tiltfile -- --profile=tenancy
tilt up -f contrib/tilt/Tiltfile -- --profile=core,tenancy   # profiles compose
```

See [`contrib/tilt/README.md`](../../contrib/tilt/README.md).

### `task tidy-check-tenancy-operator` fails until `apis` is released

`go.mod` requires `go.platform-mesh.io/apis v0.0.3`, which predates
`apis/tenancy/v1alpha1`; the repository-root `go.work` is what resolves it to the
local copy, so build, test and lint all pass. `tidy-check` deliberately runs with
`GOWORK=off` to prove each module is self-contained, and it cannot pass until the
new package ships:

```sh
task release -- apis --minor        # tag the apis module
task bump-deps -- apis <version>    # point every consumer at it
```

That is the normal monorepo flow for adding API types, not a defect in this
component — but it does mean `task verify-tenancy-operator` is red until the tag
lands.

### A note on `task generate`

`hack/gen-apiexports.sh` runs apigen twice over disjoint CRD sets, because apigen
emits one export per API group and this group needs four. Only `metadata.name` is
rewritten; schema references keep their generated version hashes, so nothing is
hand-maintained.
