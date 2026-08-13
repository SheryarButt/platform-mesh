# Provider bootstrap manifests (source of truth)

Static kcp-side manifests that register KROaaS as a provider. They apply to **two**
workspaces, so they are not a single kustomize base:

**Into the provider workspace `root:providers:kro-provider`:**
- `apiresourceschema-resourcegraphdefinitions.kro.run.yaml` — the RGD type schema
- `apiexport-kro.run.yaml` — publishes the RGD type (labeled `content-for=kro.run`)
- `providermetadata.yaml` — Marketplace listing (name == the APIExport name)
- `contentconfiguration.yaml` — the "kro" nav node listing ResourceGraphDefinitions

**Into `root:orgs`:**
- `apiexportpolicy.yaml` — shares `kro.run` to org/account workspaces so Accounts can
  bind it and it shows in the Marketplace

These are mirrored into `platform-mesh/helm-charts` under
`local-setup/example-data/root/providers/kro-provider/` and `.../root/orgs/` so KROaaS
ships with the local-setup. In production they'd be applied by the provider-bootstrap
path (ManagedProvider / PM install).

Per-generated-type ContentConfigurations (the instances a given RGD produces) are NOT
here — the operator emits one at runtime per generated type.
