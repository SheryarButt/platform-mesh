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

package memberships

import (
	"fmt"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"

	rbacv1 "k8s.io/api/rbac/v1"
)

// RulesFor returns what a role grants inside a tenant workspace.
//
// Each tier is genuinely less than the one above rather than all mapping to
// cluster-admin. What separates them is `escalate` and `bind`: without those,
// kube's escalation prevention stops a caller writing a Role granting more than
// they hold. admin has both through `*`; member and viewer have neither.
//
// A member can still write ordinary RBAC in their own workspace, which looks lax
// until you notice the Membership is the source of truth — this reconciler
// rewrites the binding it owns, and Memberships live one tier up where a member
// cannot reach them.
func RulesFor(role string) ([]rbacv1.PolicyRule, error) {
	switch role {
	case pmtenancyv1alpha1.MembershipRoleAdmin:
		return append(workspaceEntryRules(), rbacv1.PolicyRule{
			APIGroups: []string{"*"},
			Resources: []string{"*"},
			Verbs:     []string{"*"},
		}), nil

	case pmtenancyv1alpha1.MembershipRoleMember:
		return append(workspaceEntryRules(), rbacv1.PolicyRule{
			APIGroups: []string{"*"},
			Resources: []string{"*"},
			// Enumerated rather than `*`, which is the whole point: it omits
			// escalate, bind and impersonate.
			Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"},
		}), nil

	case pmtenancyv1alpha1.MembershipRoleViewer:
		return append(workspaceEntryRules(), rbacv1.PolicyRule{
			APIGroups: []string{"*"},
			Resources: []string{"*"},
			// The three read verbs and nothing else. Notably absent: `create`,
			// which on some resources is a write in disguise — SubjectAccessReview,
			// TokenRequest, and any `.../exec`-style subresource are all reached
			// with create, so handing viewers a blanket create would leak far more
			// than the name suggests.
			Verbs: []string{"get", "list", "watch"},
		}), nil

	default:
		return nil, fmt.Errorf("unknown role %q: expected %s, %s or %s",
			role, pmtenancyv1alpha1.MembershipRoleAdmin, pmtenancyv1alpha1.MembershipRoleMember,
			pmtenancyv1alpha1.MembershipRoleViewer)
	}
}

// ClusterRoleFor names the ClusterRole a role maps to.
func ClusterRoleFor(role string) (string, error) {
	switch role {
	case pmtenancyv1alpha1.MembershipRoleAdmin:
		return pmtenancyv1alpha1.ClusterRoleProjectAdmin, nil
	case pmtenancyv1alpha1.MembershipRoleMember:
		return pmtenancyv1alpha1.ClusterRoleProjectMember, nil
	case pmtenancyv1alpha1.MembershipRoleViewer:
		return pmtenancyv1alpha1.ClusterRoleProjectViewer, nil
	default:
		return "", fmt.Errorf("unknown role %q", role)
	}
}

// bindingName is the ClusterRoleBinding this reconciler owns for a Membership.
//
// Derived from the Membership's name so the binding is found again on update and
// on delete. Prefixed so it is obvious in `kubectl get clusterrolebindings` that
// the platform owns it and hand-editing will be undone.
func bindingName(membershipName string) string {
	return bindingNamePrefix + membershipName
}

// bindingNamePrefix marks a binding as platform-owned, which is also how the
// binding watcher tells ours apart from a tenant's own RBAC.
const bindingNamePrefix = "platform:membership:"

// workspaceEntryRules are what it takes to get INTO a workspace at all.
//
// Both are non-resource rules, which is why granting every resource is not enough
// on its own: kcp's content authorizer runs before resource RBAC and wants verb
// `access` on `/`, and kubectl asks for `/api` and `/apis` before anything else.
// Missing either reads as a broken grant rather than a missing one.
//
// Identical for every role: entering a workspace is the precondition for having
// privileges, not a privilege itself.
func workspaceEntryRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			NonResourceURLs: []string{"/"},
			Verbs:           []string{"access"},
		},
		{
			NonResourceURLs: []string{"/api", "/api/*", "/apis", "/apis/*", "/openapi", "/openapi/*", "/version"},
			Verbs:           []string{"get"},
		},
	}
}
