# The tenancy-operator as an OCM module

Packages this operator so the
[platform-mesh-deployer](../../../platform-mesh-deployer) can install it onto a
running `PlatformMesh`.

The released component (`github.com/platform-mesh/images/tenancy-operator`,
published by
[`tenancy-operator.yml`](../../../../.github/workflows/tenancy-operator.yml))
carries the image, the SBOMs, and two deployable payloads — one per `OCMModule`
component:

| resource | deployed as |
| --- | --- |
| `controller-manifests` | the manager, `placement: root-shard` |
| `virtualworkspace-manifests` | the tenancy virtual workspace, `placement: per-front-proxy` + `mapping` |

The chart in [`../helm/tenancy-operator`](../helm/tenancy-operator) is the
single source of truth: [`render.sh`](render.sh) renders it with
[`values.module.yaml`](values.module.yaml) (plus
[`values.module.virtualworkspace.yaml`](values.module.virtualworkspace.yaml)
for the VW), leaving the values the deployer owns at apply time — namespace,
kcp credential, workspace, OIDC — as `${...}` CEL expressions.

For the virtual workspace, the deployer mints the serving certificate
(`<module>-<component>-serving`, trusted by the front proxy) and the `mapping`
owns exposure — the chart's own `Certificate` and `TLSRoute` are switched off
in the packaged values. The payload trusts the system CA pool only; an IdP
serving from a private CA (tilt's dex) still needs the chart deployed directly.

## Building locally

Builds the same component as the release, unsigned and versioned `v0.0.0-dev`
so it can never shadow a released version:

```bash
./build.sh                                   # into ./ctf
./build.sh --push ghcr.io/platform-mesh/dev  # build and transfer
IMAGE_VERSION=v0.0.1 ./build.sh              # run a specific released image
```

Inspect what was built:

```bash
ocm get componentversion ./ctf//github.com/platform-mesh/images/tenancy-operator:v0.0.0-dev -o yaml
ocm download resource ./ctf//github.com/platform-mesh/images/tenancy-operator:v0.0.0-dev controller-manifests -O -
```

## Install

```bash
kubectl apply -f ../../../platform-mesh-deployer/config/samples/deploy_v1alpha1_ocmmodule_tenancy-operator.yaml
```

See that file for what each field decides. The two that matter most:

- **`spec.values.oidc`** must mirror the `RootShardTemplate`'s
  `spec.auth.oidc` of the same installation — a mismatch shows up as a 403 in a
  workspace the user owns rather than as an error.
- **`paths.root`** stays `${workspace}`: the minted kubeconfig is cluster-admin
  inside the module's own workspace subtree only, which is exactly the scope
  `tenancy-operator init` needs.
