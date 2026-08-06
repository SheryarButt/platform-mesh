/*
Copyright The Platform Mesh Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package deploy holds everything this operator deploys, and makes the kcp half
// of it available to the binary.
//
//	helm/   the chart — how the operator itself is deployed onto Kubernetes.
//	kcp/    the tenancy tree — what `tenancy-operator init` installs into kcp.
//
// This file exists because //go:embed cannot reach outside its own package
// directory. Making deploy/ a package lets the manifests live at a sensible path
// AND be compiled in, so the binary stays the unit of delivery — there is no way
// to run one version of the installer against a different version of the schemas
// it installs.
package deploy

import "embed"

// KcpAssets holds the manifests `tenancy-operator init` applies. `task generate`
// writes into kcp/resources, so regenerating the API and rebuilding the installer
// are the same act.
//
//go:embed kcp/resources/*.yaml kcp/bootstrap/*.yaml
var KcpAssets embed.FS

// Directories within KcpAssets.
const (
	// KcpResourcesDir holds the generated APIResourceSchemas and the four
	// APIExports built from them.
	KcpResourcesDir = "kcp/resources"

	// KcpBootstrapDir holds the hand-written pieces: WorkspaceTypes, the bind
	// RBAC, the endpoint slices and the two install-time APIBindings.
	KcpBootstrapDir = "kcp/bootstrap"
)
