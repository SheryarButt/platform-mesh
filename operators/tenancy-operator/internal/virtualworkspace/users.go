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

package virtualworkspace

import (
	"context"
	"fmt"
	"time"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// SelfAlias is the name a caller uses for their own User, so a client that does
// not want to compute sha256(issuer + "\n" + sub) itself does not have to.
const SelfAlias = "~"

var usersResource = pmtenancyv1alpha1.Resource("users")

// UserStorage serves `users`: self-registration and self-read, nothing else.
//
// An explicit create is the only way a User comes into existence. Provisioning as
// a side effect of a read would make GET a write — a monitoring probe listing
// tenants would materialize projects — and would fail somebody's unrelated
// call with an error they cannot act on.
//
// It writes intent and returns; the controller reconciles and reports through
// conditions. Keeping it thin is load-bearing: the moment it orchestrates, it is
// an operator with an HTTP frontend and its failures stop being restartable.
type UserStorage struct {
	// client writes into the directory workspace.
	client ctrlruntimeclient.Client

	// resolver mirrors kcp's username convention, so spec.rbacIdentity is what
	// kcp will actually see for this caller.
	resolver *identity.Resolver
}

var (
	_ rest.Storage              = &UserStorage{}
	_ rest.Scoper               = &UserStorage{}
	_ rest.SingularNameProvider = &UserStorage{}
	_ rest.Getter               = &UserStorage{}
	_ rest.Creater              = &UserStorage{} //nolint:misspell // rest.Creater is the upstream interface name
	_ rest.TableConvertor       = &UserStorage{}
)

// NewUserStorage returns the `users` REST storage.
func NewUserStorage(client ctrlruntimeclient.Client, resolver *identity.Resolver) *UserStorage {
	return &UserStorage{client: client, resolver: resolver}
}

// New implements rest.Storage.
func (s *UserStorage) New() runtime.Object { return &pmtenancyv1alpha1.User{} }

// Destroy implements rest.Storage.
func (s *UserStorage) Destroy() {}

// NamespaceScoped implements rest.Scoper. Users are cluster-scoped.
func (s *UserStorage) NamespaceScoped() bool { return false }

// GetSingularName implements rest.SingularNameProvider.
func (s *UserStorage) GetSingularName() string { return "user" }

// Get returns the caller's own User.
//
// Self only: there is no call that enumerates the platform's people. Another
// caller's name is treated as absent rather than forbidden, because a 403 would
// confirm the object exists and make this an oracle for who else is here.
func (s *UserStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	self, err := s.callerName(ctx)
	if err != nil {
		return nil, err
	}

	if name != SelfAlias && name != self {
		return nil, apierrors.NewNotFound(usersResource, name)
	}

	user := &pmtenancyv1alpha1.User{}
	if err := s.client.Get(ctx, ctrlruntimeclient.ObjectKey{Name: self}, user); err != nil {
		if apierrors.IsNotFound(err) {
			// Name the call that fixes it. An unprovisioned caller is the normal
			// first-run state, and a bare 404 reads as "you do not exist" rather than
			// "provision yourself".
			notFound := apierrors.NewNotFound(usersResource, name)
			notFound.ErrStatus.Message = fmt.Sprintf(
				"%s: this identity has not provisioned itself yet. Create your own User to do so: "+
					"POST an empty User to this resource (`kubectl create -f`, or `create users` with an "+
					"empty spec). The call is idempotent and the server fills every field from your token.",
				notFound.ErrStatus.Message)
			return nil, notFound
		}
		return nil, err
	}
	return user, nil
}

// Create self-provisions the caller's User.
//
// The submitted object's spec is ignored entirely: every field is filled from the
// verified token, and the name is sha256(issuer + "\n" + sub). That is what makes
// the call idempotent — two concurrent attempts for one identity collide on
// AlreadyExists and the second gets the existing object back, so two browser tabs
// cannot mint two projects.
//
// A client-supplied name is not an error, it is simply overwritten: the identity
// is the caller's token, never their request body.
func (s *UserStorage) Create(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, _ *metav1.CreateOptions) (runtime.Object, error) {
	if _, ok := obj.(*pmtenancyv1alpha1.User); !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a User, got %T", obj))
	}

	claims, err := claimsFrom(ctx)
	if err != nil {
		return nil, err
	}

	name, err := identity.UserName(claims.Issuer, claims.Subject)
	if err != nil {
		return nil, apierrors.NewBadRequest(err.Error())
	}

	rbacIdentity, err := s.resolver.RBACIdentity(claims)
	if err != nil {
		// The token verified but carries no usable username claim. Refusing here
		// is better than storing a User whose bindings could never match.
		return nil, apierrors.NewBadRequest(err.Error())
	}

	user := &pmtenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: pmtenancyv1alpha1.UserSpec{
			Email:        claims.Email,
			Name:         claims.Name,
			Issuer:       claims.Issuer,
			Subject:      claims.Subject,
			RBACIdentity: rbacIdentity,
			// Stamped here rather than left to CRD defaulting, because the whole
			// spec is server-owned on this path: a client sending
			// seedTenant=false would otherwise have it honoured, and
			// self-provision is not the place to opt out of your own project.
			// Changing the fields afterwards is a normal update, and allowed.
			Tenancy: pmtenancyv1alpha1.UserTenancySpec{
				SeedTenant:  true,
				SeedProject: true,
			},
		},
	}

	if err := s.client.Create(ctx, user); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Idempotent by construction: return what is already there rather
			// than an error the client would have to special-case on every login.
			existing := &pmtenancyv1alpha1.User{}
			if getErr := s.client.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name}, existing); getErr != nil {
				return nil, getErr
			}
			return existing, nil
		}
		return nil, err
	}
	return user, nil
}

// callerName resolves the authenticated caller to their User name.
func (s *UserStorage) callerName(ctx context.Context) (string, error) {
	claims, err := claimsFrom(ctx)
	if err != nil {
		return "", err
	}
	name, err := identity.UserName(claims.Issuer, claims.Subject)
	if err != nil {
		return "", apierrors.NewBadRequest(err.Error())
	}
	return name, nil
}

// claimsFrom reconstructs the caller's identity from the authenticated request.
//
// The authenticator puts the issuer and subject into the user's Extra fields;
// falling back to parsing the username would re-derive identity from a mutable
// claim, which is precisely what keying on the subject avoids.
func claimsFrom(ctx context.Context) (identity.Claims, error) {
	u, ok := request.UserFrom(ctx)
	if !ok {
		return identity.Claims{}, apierrors.NewUnauthorized("no authenticated user in request")
	}

	extra := u.GetExtra()
	first := func(key string) string {
		if v := extra[key]; len(v) > 0 {
			return v[0]
		}
		return ""
	}

	claims := identity.Claims{
		Issuer:  first(ExtraIssuer),
		Subject: first(ExtraSubject),
		Email:   first(ExtraEmail),
		Name:    first(ExtraName),
		Groups:  u.GetGroups(),
	}

	if claims.Issuer == "" || claims.Subject == "" {
		return identity.Claims{}, apierrors.NewUnauthorized(
			"authenticated token carries no issuer/subject; the tenancy virtual workspace must be configured with the same OIDC issuer as kcp")
	}

	// An absent email claim is left absent. There used to be a fallback to the
	// authenticated username here, and it was worse than nothing: the username
	// carries the RBAC prefix, so with --oidc-username-claim=email it produced
	// `pm:pm:alice@example.com` as the identity — a subject that never
	// authenticates, and therefore a User whose every ClusterRoleBinding is dead
	// while looking correct. Create's own check refuses an underivable identity
	// loudly, which is the behaviour that guard exists for; manufacturing a value
	// here only stopped it from firing.
	return claims, nil
}

// Extra keys the authenticator populates so storage can rebuild the claims it
// needs without re-verifying the token.
const (
	ExtraIssuer  = "tenancy.platform-mesh.io/issuer"
	ExtraSubject = "tenancy.platform-mesh.io/subject"
	ExtraEmail   = "tenancy.platform-mesh.io/email"
	ExtraName    = "tenancy.platform-mesh.io/name"
)

// ConvertToTable renders `kubectl get` output.
//
// Required in practice even though this resource cannot be listed. The apiserver
// only ENFORCES a TableConvertor on listers, but it hands scope.TableConvertor to
// the GET path too, and kubectl asks for a Table on a plain get — so without this
// `kubectl get users ~` dereferences a nil interface and the request dies with a
// 500 instead of printing a row.
//
// The columns mirror the User CRD's printcolumns, so the aggregated view and a
// direct read of the underlying object look the same.
func (s *UserStorage) ConvertToTable(_ context.Context, object runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name", Description: "The User's name: sha256(issuer + newline + sub)"},
			{Name: "Email", Type: "string", Description: "Email claim from the verified token"},
			{Name: "Display Name", Type: "string", Description: "Human-facing name from the token"},
			{Name: "Ready", Type: "string", Description: "Whether bootstrap finished"},
			{Name: "Age", Type: "date", Description: "Time since creation"},
		},
	}

	appendRow := func(u *pmtenancyv1alpha1.User) {
		table.Rows = append(table.Rows, metav1.TableRow{
			Cells:  []any{u.Name, u.Spec.Email, u.Spec.Name, readyOf(u), translateTimestampSince(u.CreationTimestamp)},
			Object: runtime.RawExtension{Object: u},
		})
	}

	switch t := object.(type) {
	case *pmtenancyv1alpha1.User:
		appendRow(t)
	case *pmtenancyv1alpha1.UserList:
		// Not reachable today: there is no lister, deliberately. Handled anyway so
		// adding one cannot regress printing.
		table.ResourceVersion = t.ResourceVersion
		for i := range t.Items {
			appendRow(&t.Items[i])
		}
	default:
		return nil, fmt.Errorf("cannot render %T as a table", object)
	}

	return table, nil
}

// readyOf reports the Ready condition as kubectl would show it.
func readyOf(u *pmtenancyv1alpha1.User) string {
	for _, c := range u.Status.Conditions {
		if c.Type == pmtenancyv1alpha1.UserConditionReady {
			return string(c.Status)
		}
	}
	return "Unknown"
}

// translateTimestampSince matches the apiserver's own age formatting, so a row
// here reads the same as one from any other resource.
func translateTimestampSince(timestamp metav1.Time) string {
	if timestamp.IsZero() {
		return "<unknown>"
	}
	return duration.HumanDuration(time.Since(timestamp.Time))
}
