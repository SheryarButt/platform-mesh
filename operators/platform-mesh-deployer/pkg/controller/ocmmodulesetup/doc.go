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

// Package modulesetup performs the kcp side of a module. It is the only part
// of the deployer that writes inside kcp.
//
// A pass runs as a chain:
//
//  1. Load the OCMModuleSetup. A deleted one goes down the finalize path instead:
//     its workspaces are removed deepest first, so a parent never outlives a
//     child, and only then is the finalizer dropped.
//  2. Fetch the PlatformMesh the setup belongs to and wait until its root
//     structure exists — the module's workspaces are created below it. The
//     PlatformMesh is watched, so this waits without polling.
//  3. Mint and read the kcp admin kubeconfig. kcp-operator writes the secret
//     asynchronously and nothing watches it, so this one polls.
//  4. Create each declared workspace along its path and apply the content the
//     module ships for it, downloaded from the module's component version.
//     A workspace that is not schedulable yet also polls.
//  5. Publish each workspace's URL on the status, so the module's payload can
//     address its own workspaces without knowing how the front proxy is
//     exposed.
//
// Ready is the handshake the module controller waits on before it deploys the
// workloads that talk to these workspaces.
package ocmmodulesetup
