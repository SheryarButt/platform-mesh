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

// Package platformmesh brings up the kcp installation a PlatformMesh
// describes, on the clusters engaged for it.
//
// A pass runs as a chain, each step the prerequisite of the next:
//
//  1. Wait for the modules that have to exist before kcp — the etcd the shards
//     store into, for one. OCMModules are watched, so this waits without polling.
//  2. Render the kcp-operator admin CRs for the root shard, the shards, the
//     front proxy, the cache server and the virtual workspaces, deleting the
//     ones no longer wanted. Every unmet precondition here is an error rather
//     than a wait: no engaged root shard cluster means the installation is
//     misconfigured, not that it is still starting.
//  3. Publish those components through the configured ingress stacks, writing
//     routes on the workload clusters.
//  4. Create the kcp workspaces everything else is installed below. Only runs
//     where this deployer also runs the provisioner; kcp is a separate API
//     server and is not watched, so waiting on it polls.
//  5. Roll the resolved version up onto the status.
//
// Ready is written last and aggregates the chain. The module controller gates
// its post-topology modules on it, and the e2e suite waits on it.
package platformmesh
