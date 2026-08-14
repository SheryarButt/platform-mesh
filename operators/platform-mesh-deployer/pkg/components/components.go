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

// Package components names the kcp topology components.
// The names are the multi provider prefixes and thus appear in engaged cluster names as "<component>#<platformMesh>--<clusterID>".
package components

const (
	RootShard        = "rootshard"
	FrontProxy       = "frontproxy"
	CacheServer      = "cacheserver"
	VirtualWorkspace = "virtualworkspace"
	ShardPrefix      = "shards-"
)

// Labels key every object the deployer renders to the installation, component
// and engaged cluster it was rendered for.
const (
	LabelPlatformMesh = "deploy.platform-mesh.io/platform-mesh"
	LabelComponent    = "deploy.platform-mesh.io/component"
	LabelCluster      = "deploy.platform-mesh.io/cluster"
)

// Shard returns the component name for a shard group.
func Shard(group string) string {
	return ShardPrefix + group
}
