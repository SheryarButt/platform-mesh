# search-operator

![Platform Mesh Search](PMSearch.svg)

## Description

The search-operator makes Platform Mesh resources searchable in OpenSearch, scoped and permission-filtered per organization. It watches kcp across workspaces and works in three phases:

1. **SearchIndex provisioning.** When an `APIBinding` reconciles in an organization-rooted workspace (a workspace under `root:orgs`), the operator lists the other `APIBinding`s bound in that same workspace and resolves the workspace to its organization. For each organization, and for each `APIResourceSchema` those bindings bring in, it creates or updates a `SearchIndex` in the org workspace (`root:orgs`). The searchable fields are derived from the leaf fields of the schema's served version. If a `SearchConfig` (named after the `APIResourceSchema`) exists in the provider workspace where the schema lives, its `excludedFields`, `exactFields`, and `semanticFields` reclassify those fields into filterable, semantic, or excluded. Without a `SearchConfig`, every leaf field becomes a default field. A `SearchConfig` only reclassifies fields already present in the schema, meaning it cannot add new ones. A small set of default filterable fields (kind, name, namespace, cluster_name, workspace_path, account_fga_object) is always added.

2. **OpenSearch index creation.** Each `SearchIndex` is reconciled by the `IndexLifecycleSubroutine`, which manages the corresponding OpenSearch index. The index mapping is built from the `SearchIndex`'s default, semantic, and filterable fields (semantic fields require a configured semantic model), and the resolved index name is written back to the `SearchIndex` status.

3. **Resource indexing.** For every resource whose GVK is configured for indexing, the `IndexableResourceWatcherSubroutine` resolves the current org and the resource's plural name, looks up the matching `SearchIndex`, and stores the resource as a document in that org's index. It populates the document's fields exactly as declared by the `SearchIndex` (default / semantic / filterable), attaches the full payload for full-text search, and embeds permission derived from the resource's `AccountInfo` hierarchy so search results can be permission-filtered per user upon retrieval.

### Message Sequence

The following diagram traces the end-to-end flow, from a resource being created through to it being indexed as a searchable, permission-filtered document.

```mermaid
sequenceDiagram
    participant User
    participant Platform as Platform Mesh
    participant SProv as Search provider
    participant GHProv as GitHub provider
    participant SearchOp as search-operator
    participant OS as OpenSearch

    User->>Platform: Installs Search provider
    Platform->>SProv: Binds Search Export
    User->>Platform: Installs Github provider
    Platform->>GHProv: Binds Github Export
    Note over GHProv: Github Export has one resource<br/>schema called `Commit`
    SearchOp->>GHProv: Reads Github Export's schemas
    SearchOp->>GHProv: Reads SearchConfig resources
    SearchOp->>Platform: Creates org-scoped SearchIndex resource `Commit`
    SearchOp->>OS: Manages org-scoped index for `Commit` resource
    User->>Platform: Creates new Github resource
    Note over SearchOp: multicluster-provider<br/>detects Commit resource
    SearchOp->>Platform: Fetch Github resource
    SearchOp->>OS: Indexes resource according to SearchIndex
```

### Custom Resources

- **`SearchIndex`** (`search.platform-mesh.io`): declares an OpenSearch index for one org and one resource type, including its default, semantic, and filterable fields. Managed by the operator; lives in the org workspace.
- **`SearchConfig`** (`search.platform-mesh.io`): optional, authored in the provider workspace next to an `APIResourceSchema`. Overrides how that schema's fields are classified (`excludedFields`, `exactFields`, `semanticFields`).

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/search-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/search-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/search-operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/search-operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## Test Locally

Copy the `.env.example` to `.env` and replace urls:

```sh
cp .env.example .env
```

run the operator to reconcile the searchindex APIResource:

```sh
go run cmd/main.go
```

test by manually adding a searchindex resource:

```sh
export KUBCONFIG=<path to an kcp admin kubeconfig>
kubectl apply -f ./scripts/searchindex-test-resource.yaml --server="https://localhost:8443/clusters/root:orgs"
```

observe logs of successful reconciliation (start with kcp kubeconfig configured with path :root:platform-mesh-system):

```sh
# In shell:
searchindex.core.platform-mesh.io/testindex5 created
# In Operator
{"level":"info","service":"...","operator":"searchindex","controller":"SearchIndexReconciler","name":"<index name>","namespace":"","reconcile_id":"...","time":"...","caller":"...","message":"start reconcile"}
```

check if the url with your new index name in the path returns the desired values:

`https://opensearch.portal.localhost:8443/<index name>`

observe

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
