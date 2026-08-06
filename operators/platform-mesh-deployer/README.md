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

```bash
kind create cluster --name platform-mesh
tilt up -f contrib/tilt/Tiltfile
```

[`Tiltfile`](Tiltfile) here stands the operator up hot-reloaded from source
against a dev `PlatformMesh`, in the same single-cluster shape as `test/e2e`'s
`TestSingleCluster`. No profile flag: this component **is** the dev
environment's kcp — it replaced the static install, and everything else in the
repo's Tilt env sits on the kcp it builds.

It needs the ntnn/kcp-operator fork pinned by the `replace` in `go.mod`, which
owns the same CRDs as the upstream operator; bump the two together. See
[contrib/tilt/README.md](../../contrib/tilt/README.md#how-kcp-gets-built).

## Samples

See [`config/samples`](config/samples) for example resources.
