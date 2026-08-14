# platform-mesh-deployer

Operator that deploys and manages Platform Mesh installations on a management
cluster. It reconciles the `deploy.platform-mesh.io/v1alpha1` resources
(`PlatformMesh`, `Module`, `ModuleSetup`) defined in the shared
[`apis`](../../apis) module.

## Development

```bash
task build      # go build ./...
task lint       # format + golangci-lint
task test       # unit tests
task generate   # regenerate CRDs (config/crd) and deepcopy code
```

The CRDs are installed on the management cluster (not in kcp), so no `apigen`
step is required.

### Running it

#### Tilt setup

```bash
kind create cluster --name platform-mesh
tilt up -f contrib/tilt/Tiltfile
```

[`Tiltfile`](Tiltfile) here stands the operator up hot-reloaded from source
against a dev `PlatformMesh`, in the same single-cluster shape as `test/e2e`'s
`TestSingleCluster`. No profile flag: this component **is** the dev
environment's kcp — it replaced the static install, and everything else in the
repo's Tilt env sits on the kcp it builds.

It runs kcp-operator's config and workload controller groups inside its own
manager, from the ntnn/kcp-operator fork pinned by the `replace` in `go.mod`.
Nothing deploys kcp-operator itself; only its CRDs are installed, from
`config/bases/kcp-operator/crds`, pinned to the same commit — bump the two
together. See
[contrib/tilt/README.md](../../contrib/tilt/README.md#how-kcp-gets-built).

#### Kind setup, without Tilt

The same single-cluster environment stood up with plain `kubectl`, `helm` and
`kind` — no Tilt session owning it. Step by step, including the pieces that are
easy to miss (the CoreDNS sslip.io block, the gateway NodePort, the kubeconfig
Secret that engages the cluster with itself, and an admin kubeconfig at the end):
[`docs/local-kind.md`](docs/local-kind.md).


## Samples

See [`config/samples`](config/samples) for example resources.
