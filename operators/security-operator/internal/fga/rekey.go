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

package fga

import (
	"slices"
	"strings"

	pmcorev1alpha1 "go.platform-mesh.io/apis/core/v1alpha1"
)

// AccountKey is the parsed form of the two FGA key shapes that embed an
// account's origin cluster id:
//
//	role:<objectType>/<clusterID>/<name>/<role>[#<relation>]
//	<objectType>:<clusterID>/<name>[#<relation>]
//
// When the org workspace backing an account is deleted and re-created, the
// cluster id changes and every tuple built from these keys goes stale.
type AccountKey struct {
	ObjectType string
	ClusterID  string
	Name       string
	Role       string // non-empty for the role shape
	Relation   string // optional "#<relation>" suffix (e.g. "assignee")
}

// ParseAccountKey parses s into an AccountKey. It returns false for strings
// that do not match either of the two shapes (e.g. "user:someone",
// "role:authenticated#assignee").
//
// The match is purely structural: other key families share the raw
// `<type>:<clusterID>/<name>` shape (e.g. the
// `apis_kcp_io_apiexport:<providerClusterID>/<exportName>` user keys written
// into the same org store for APIExportPolicies), so callers MUST additionally
// check ObjectType against the configured account object type before treating
// the result as an account key.
func ParseAccountKey(s string) (AccountKey, bool) {
	var k AccountKey

	if i := strings.IndexByte(s, '#'); i >= 0 {
		k.Relation = s[i+1:]
		s = s[:i]
		if k.Relation == "" {
			return AccountKey{}, false
		}
	}

	typ, rest, found := strings.Cut(s, ":")
	if !found || typ == "" || typ == "user" {
		return AccountKey{}, false
	}

	parts := strings.Split(rest, "/")
	switch {
	case typ == "role" && len(parts) == 4:
		k.ObjectType, k.ClusterID, k.Name, k.Role = parts[0], parts[1], parts[2], parts[3]
	case typ != "role" && len(parts) == 2:
		k.ObjectType, k.ClusterID, k.Name = typ, parts[0], parts[1]
	default:
		return AccountKey{}, false
	}

	if k.ObjectType == "" || k.ClusterID == "" || k.Name == "" || (typ == "role" && k.Role == "") {
		return AccountKey{}, false
	}

	return k, true
}

// String renders the key back into its tuple representation. It round-trips
// with ParseAccountKey.
func (k AccountKey) String() string {
	var s string
	if k.Role != "" {
		s = RenderRolePrefix(k.ObjectType, k.ClusterID, k.Name) + k.Role
	} else {
		s = renderAccountEntity(k.ObjectType, k.ClusterID, k.Name)
	}
	if k.Relation != "" {
		s += "#" + k.Relation
	}
	return s
}

// StaleAccountTupleFilter returns a filter matching tuples that reference
// accountName under the account object type objectType with a cluster id other
// than currentClusterID, in either their Object or their User key.
func StaleAccountTupleFilter(objectType, accountName, currentClusterID string) TupleFilter {
	return func(t pmcorev1alpha1.Tuple) bool {
		return len(StaleClusterIDs(t, objectType, accountName, currentClusterID)) > 0
	}
}

// StaleClusterIDs returns the distinct cluster ids other than currentClusterID
// under which the tuple's Object or User key references accountName with the
// account object type objectType. Keys of any other object type are ignored
// even when they share the raw key shape and the account's name (see
// ParseAccountKey). A tuple built from account keys references exactly one
// cluster id; more than one returned id means the tuple is ambiguous and must
// not be re-keyed blindly.
func StaleClusterIDs(t pmcorev1alpha1.Tuple, objectType, accountName, currentClusterID string) []string {
	if objectType == "" || accountName == "" || currentClusterID == "" {
		return nil
	}

	var ids []string
	for _, s := range []string{t.Object, t.User} {
		k, ok := ParseAccountKey(s)
		if ok && k.ObjectType == objectType && k.Name == accountName && k.ClusterID != currentClusterID && !slices.Contains(ids, k.ClusterID) {
			ids = append(ids, k.ClusterID)
		}
	}
	return ids
}

// RekeyTuple returns a copy of t in which every Object and User key of the
// account object type objectType that carries oldClusterID is rewritten to
// carry newClusterID instead. Keys that do not parse as account keys, that
// carry another object type, or that carry a different cluster id, are left
// untouched.
func RekeyTuple(t pmcorev1alpha1.Tuple, objectType, oldClusterID, newClusterID string) pmcorev1alpha1.Tuple {
	t.Object = rekeyKey(t.Object, objectType, oldClusterID, newClusterID)
	t.User = rekeyKey(t.User, objectType, oldClusterID, newClusterID)
	return t
}

func rekeyKey(s, objectType, oldClusterID, newClusterID string) string {
	k, ok := ParseAccountKey(s)
	if !ok || k.ObjectType != objectType || k.ClusterID != oldClusterID {
		return s
	}
	k.ClusterID = newClusterID
	return k.String()
}
