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
tilt up -f contrib/tilt/Tiltfile -- --profile=deployer
```

[`Tiltfile`](Tiltfile) here stands the operator up hot-reloaded from source
against a dev `PlatformMesh`, in the same single-cluster shape as
`test/e2e`'s `TestSingleCluster`. It **replaces** the repo's static kcp install
rather than adding to it — the deployer builds kcp itself, and it needs the
ntnn/kcp-operator fork pinned by the `replace` in `go.mod`, which owns the same
CRDs as the upstream operator that install uses. See
[contrib/tilt/README.md](../../contrib/tilt/README.md#deployer-profile).

## Samples

See [`config/samples`](config/samples) for example resources.
