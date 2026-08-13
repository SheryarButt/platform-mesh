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

// Package module deploys one OCMModule onto the clusters engaged for its
// PlatformMesh.
//
// A pass runs as a chain, each step the prerequisite of the next:
//
//  1. Load the OCMModule. A deleted one goes down the finalize path instead: its
//     objects are pruned from every cluster the PlatformMesh has engaged,
//     deliberately wider than the module's last known placement, and only then
//     is the finalizer dropped.
//  2. Validate the spec and reject dependency cycles. Both are terminal: only
//     an edit can fix them, and that re-triggers the watch, so they are
//     recorded on the status rather than retried.
//  3. Wait for the PlatformMesh, for a post-topology module. A pre-topology
//     module skips this, since the topology is what waits for it.
//  4. Wait for the modules in spec.dependsOn to be ready.
//  5. Resolve the component version and record its digest.
//  6. Fan the components out over the engaged clusters.
//  7. Write the OCMModuleSetup handshake and wait for the provisioner to finish
//     the kcp side, so the payload can rely on its workspaces existing.
//  8. Mint the kubeconfigs and certificates each instance needs, render the
//     payload and apply it, then prune what no longer belongs.
//
// Ready is written last and aggregates the chain. The PlatformMesh's
// pre-topology gate and other modules' dependency gates read it.
package ocmmodule
