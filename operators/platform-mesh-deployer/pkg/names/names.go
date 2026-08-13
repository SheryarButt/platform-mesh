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

// Package names builds the kcp-operator admin CR names the deployer creates.
//
// kcp-operator derives Deployment, Service and Certificate names, X.509
// CommonNames and certificate revision annotation keys from the admin CR name.
// The annotation keys bind hardest: kcp-operator stamps
// "operator.kcp.io/cert-<certificate>-revision" onto the Compiled* CRs, and a
// label/annotation name part may not exceed 63 bytes. With the longest
// certificate kind being "external-logical-cluster-admin", a Shard or RootShard
// name has 18 bytes. Names are therefore a truncated stub plus a hash rather
// than anything fully readable.
package names

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Name budgets, each the tightest value kcp-operator derives from the admin CR
// name. Pinned by TestBudgets.
const (
	// MaxRootShard is bounded by the cert-<name>-external-logical-cluster-admin-revision
	// annotation key.
	MaxRootShard = 18
	// MaxShard is bounded by the same annotation key as MaxRootShard.
	MaxShard = 18
	// MaxFrontProxy is bounded by MaxRootShardFrontProxy, since a front proxy
	// certificate is named after both the root shard and the front proxy.
	MaxFrontProxy = 16
	// MaxCacheServer is bounded by the cert-<name>-client-certificate-revision
	// annotation key.
	MaxCacheServer = 30
	// MaxVirtualWorkspace is bounded by the cert-<name>-client-revision
	// annotation key.
	MaxVirtualWorkspace = 42

	// MaxRootShardFrontProxy bounds a root shard and a front proxy name
	// together, from the cert-<rootShard>-<frontProxy>-requestheader-revision
	// annotation key.
	MaxRootShardFrontProxy = 34
)

// hashLen is the fixed-width identity hash every name carries. It covers the
// untruncated identity, so names that had to be shortened stay distinct.
const hashLen = 6

// Scoped returns the admin CR name for a component of platformMesh on the given
// cluster, of at most budget bytes.
//
// The name is a readable stub of platformMesh and component followed by a hash
// of the full identity. Budgets are far too small to spell the identity out;
// the LabelPlatformMesh, LabelComponent and LabelCluster labels on every admin
// CR carry it unabbreviated.
func Scoped(budget int, platformMesh, component, clusterID string) string {
	sum := hash(platformMesh + "\x00" + component + "\x00" + clusterID)
	stub := stub(platformMesh, component, budget-hashLen-1)
	if stub == "" {
		return sum
	}
	return stub + "-" + sum
}

// stub renders platformMesh and component into at most budget bytes, splitting
// the room evenly and passing on whatever either does not need.
func stub(platformMesh, component string, budget int) string {
	if budget < 3 {
		return ""
	}
	share := (budget - 1) / 2
	pmLen, componentLen := share, budget-1-share
	if len(platformMesh) < pmLen {
		componentLen += pmLen - len(platformMesh)
		pmLen = len(platformMesh)
	}
	if len(component) < componentLen {
		pmLen += componentLen - len(component)
		componentLen = len(component)
	}
	pm, c := truncate(platformMesh, pmLen), truncate(component, componentLen)
	if pm == "" || c == "" {
		return truncate(pm+c, budget)
	}
	return pm + "-" + c
}

// truncate cuts s to n bytes without leaving a separator at the edge, which
// would make the name an invalid DNS label.
func truncate(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.Trim(s, "-.")
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:hashLen]
}

// RootShard names the RootShard admin CR of a root shard group.
func RootShard(platformMesh, group, clusterID string) string {
	return Scoped(MaxRootShard, platformMesh, group, clusterID)
}

// Shard names the Shard admin CR of a shard group.
func Shard(platformMesh, group, clusterID string) string {
	return Scoped(MaxShard, platformMesh, group, clusterID)
}

// FrontProxy names the FrontProxy admin CR.
func FrontProxy(platformMesh, frontProxy, clusterID string) string {
	return Scoped(MaxFrontProxy, platformMesh, frontProxy, clusterID)
}

// CacheServer names the CacheServer admin CR.
func CacheServer(platformMesh, cacheServer, clusterID string) string {
	return Scoped(MaxCacheServer, platformMesh, cacheServer, clusterID)
}

// VirtualWorkspace names the VirtualWorkspace admin CR serving a shard group.
// It has its own budget and so does not match the shard's name.
func VirtualWorkspace(platformMesh, group, clusterID string) string {
	return Scoped(MaxVirtualWorkspace, platformMesh, group, clusterID)
}
