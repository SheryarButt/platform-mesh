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

## Samples

See [`config/samples`](config/samples) for example resources.
