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

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	platformmeshconfig "go.platform-mesh.io/golang-commons/config"
	"go.platform-mesh.io/tenancy-operator/internal/bootstrap"
	"go.platform-mesh.io/tenancy-operator/internal/config"
	"go.platform-mesh.io/tenancy-operator/internal/operator"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
	"go.platform-mesh.io/tenancy-operator/pkg/membership"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"
	"go.platform-mesh.io/tenancy-operator/pkg/paths"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kcp-dev/logicalcluster/v3"
	mcpclient "github.com/kcp-dev/multicluster-provider/client"
	"github.com/kcp-dev/multicluster-provider/envtest"
	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpapisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

var (
	kcpConfig  *rest.Config
	kcpClient  mcpclient.ClusterClient
	testScheme *runtime.Scheme
)

func TestMain(m *testing.M) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))
	os.Exit(runTests(m))
}

// runTests starts one kcp for the whole binary.
func runTests(m *testing.M) int {
	env := &envtest.Environment{
		AttachKcpOutput:       false,
		KcpStartTimeout:       2 * time.Minute,
		KcpStopTimeout:        30 * time.Second,
		BinaryAssetsDirectory: "../../../../bin", // TEST_KCP_ASSETS overrides
	}

	if os.Getenv("USE_EXISTING_KCP") != "" && os.Getenv("EXISTING_KCP_CONTEXT") == "" {
		env.ExistingKcpContext = "base"
	}

	// kcp's own fixtures clean up workspaces mid-run without this. The instance
	// envtest controls is ephemeral, so there is nothing to preserve it from.
	if os.Getenv("PRESERVE") == "" {
		if err := os.Setenv("PRESERVE", "true"); err != nil {
			fmt.Fprintf(os.Stderr, "setting PRESERVE: %v\n", err)
			return 1
		}
	}

	var err error
	kcpConfig, err = env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting kcp: %v\n", err)
		return 1
	}
	defer func() {
		if err := env.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "stopping kcp: %v\n", err)
		}
	}()

	testScheme = newTestScheme()
	kcpClient, err = mcpclient.New(kcpConfig, ctrlruntimeclient.Options{Scheme: testScheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "building kcp client: %v\n", err)
		return 1
	}

	return m.Run()
}

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		kcptenancyv1alpha1.AddToScheme,
		kcpcorev1alpha1.AddToScheme,
		kcpapisv1alpha1.AddToScheme,
		kcpapisv1alpha2.AddToScheme,
		pmtenancyv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			panic(err)
		}
	}
	return scheme
}

// install runs the real installer into a fresh root workspace and returns the
// layout it built.
func install(tb testing.TB) paths.Layout {
	tb.Helper()

	// A root per test, so tests cannot see each other's tree and one failure does
	// not cascade. Every path in the model is derived from this, which is the
	// property --paths-root exists for.
	ws, _ := envtest.NewWorkspaceFixture(tb, kcpClient, core.RootCluster.Path(), envtest.WithNamePrefix("tenancy"))

	layout, err := paths.New(paths.Options{Root: core.RootCluster.Path().Join(ws.Name).String()})
	require.NoError(tb, err)

	reinstall(tb, layout)
	return layout
}

// reinstall converges an EXISTING layout again, which is what every pod restart
// and every upgrade does.
func reinstall(tb testing.TB, layout paths.Layout) {
	tb.Helper()

	inst := bootstrap.New(kcpConfig, bootstrap.Options{
		Layout:                layout,
		WorkspaceReadyTimeout: 2 * time.Minute,
	}, func(format string, args ...any) { tb.Logf(format, args...) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(tb, inst.Run(ctx), "the installer must converge")
}

// clusterClient returns a client scoped to one workspace path.
func clusterClient(path string) ctrlruntimeclient.Client {
	return kcpClient.Cluster(logicalcluster.NewPath(path))
}

// bindExport binds one of the installed exports into a workspace and waits until
// it is Bound.
func bindExport(tb testing.TB, path, exportPath, exportName string) {
	tb.Helper()

	cl := clusterClient(path)
	binding := &kcpapisv1alpha2.APIBinding{}
	binding.Name = exportName
	binding.Spec.Reference.Export = &kcpapisv1alpha2.ExportBindingReference{
		Path: exportPath,
		Name: exportName,
	}
	if err := cl.Create(context.Background(), binding); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(tb, err)
	}

	envtest.Eventually(tb, func() (bool, string) {
		current := &kcpapisv1alpha2.APIBinding{}
		if err := cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: exportName}, current); err != nil {
			return false, fmt.Sprintf("getting APIBinding: %v", err)
		}
		if current.Status.Phase != kcpapisv1alpha2.APIBindingPhaseBound {
			return false, fmt.Sprintf("phase %s", current.Status.Phase)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 200*time.Millisecond, "APIBinding %s should be bound in %s", exportName, path)
}

// servable waits until a workspace answers for a kind.
func servable(tb testing.TB, path string, obj ctrlruntimeclient.Object) {
	tb.Helper()

	envtest.Eventually(tb, func() (bool, string) {
		err := clusterClient(path).Create(context.Background(), obj)
		switch {
		case err == nil, apierrors.IsAlreadyExists(err):
			return true, ""
		case meta.IsNoMatchError(err):
			return false, "not in discovery yet"
		default:
			// A validation error means the resource IS served — the server got far
			// enough to judge the object. That is the answer this helper wants, and
			// the caller inspects the error itself.
			return true, ""
		}
	}, wait.ForeverTestTimeout, 200*time.Millisecond, "%T should become servable in %s", obj, path)
}

// allowed asks KCP ITSELF whether a subject may do something in a workspace.
func allowed(tb testing.TB, path, user string, groups []string, verb, group, resource string) bool {
	tb.Helper()

	sar := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   user,
			Groups: groups,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:     verb,
				Group:    group,
				Resource: resource,
			},
		},
	}
	require.NoError(tb, clusterClient(path).Create(context.Background(), sar))
	return sar.Status.Allowed
}

// allowedNonResource is the same question for a non-resource URL, which is how
// kcp's content authorizer gates entry to a workspace at all.
func allowedNonResource(tb testing.TB, path, user string, groups []string, verb, url string) bool {
	tb.Helper()

	sar := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:                  user,
			Groups:                groups,
			NonResourceAttributes: &authorizationv1.NonResourceAttributes{Verb: verb, Path: url},
		},
	}
	require.NoError(tb, clusterClient(path).Create(context.Background(), sar))
	return sar.Status.Allowed
}

// bindRole writes the ClusterRole and binding for one role, exactly as the
// Membership reconciler does, into a workspace.
func bindRole(tb testing.TB, path, roleName string, rules []rbacv1.PolicyRule, subject rbacv1.Subject) {
	tb.Helper()
	cl := clusterClient(path)
	ctx := context.Background()

	role := &rbacv1.ClusterRole{}
	role.Name = roleName
	role.Rules = rules
	require.NoError(tb, cl.Create(ctx, role))

	binding := &rbacv1.ClusterRoleBinding{}
	binding.Name = "platform:membership:" + roleName + ":" + subject.Kind
	binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: roleName}
	binding.Subjects = []rbacv1.Subject{subject}
	require.NoError(tb, cl.Create(ctx, binding))
}

// fleet is an installed tenancy tree with the REAL controllers running against
// it, one per test.
type fleet struct {
	layout paths.Layout
}

// newFleet installs the tree and starts the operator against it.
func newFleet(tb testing.TB, opts ...fleetOption) *fleet {
	tb.Helper()

	f := &fleet{layout: install(tb)}
	startOperator(tb, f.layout, opts...)
	return f
}

// tenantPath is where a Tenant's own objects live — its Memberships and Projects.
func (f *fleet) tenantPath(tenant *pmtenancyv1alpha1.Tenant) string {
	return tenant.Status.ClusterID
}

// createUser writes the User the way the virtual workspace would.
func (f *fleet) createUser(tb testing.TB, email string) *pmtenancyv1alpha1.User {
	tb.Helper()

	const issuer = "https://idp.example"
	subject := "sub-" + email

	name, err := identity.UserName(issuer, subject)
	require.NoError(tb, err)

	user := &pmtenancyv1alpha1.User{}
	user.Name = name
	user.Spec = pmtenancyv1alpha1.UserSpec{
		Email:        email,
		Issuer:       issuer,
		Subject:      subject,
		RBACIdentity: "pm:" + email,
		Tenancy:      pmtenancyv1alpha1.UserTenancySpec{SeedTenant: true, SeedProject: true},
	}
	// Through servable: the directory's binding to tenancy-platform is Bound before
	// the resource reaches discovery, and creating in that window fails with "no
	// matches for kind", which reads like a missing schema rather than a race.
	servable(tb, f.layout.Directory, user)
	return user
}

// startOperator builds and starts the four managers, exactly as `tenancy-
// operator operator` does, and stops them when the test ends.
func startOperator(tb testing.TB, layout paths.Layout, opts ...fleetOption) {
	tb.Helper()

	cfg := config.NewOperatorConfig()
	cfg.Paths.Root = layout.Root
	// Word-pair names make a failure message readable; UUIDs are the production
	// default and are exercised by the naming suite.
	cfg.Tenancy.NamingStrategy = naming.StrategyWords
	cfg.OIDC.GroupsPrefix = "pm:"
	for _, o := range opts {
		o(&cfg)
	}

	identities, err := cfg.IdentityResolver()
	require.NoError(tb, err)

	// Pointed at the EXPORTS workspace, which is where every manager resolves its
	// APIExportEndpointSlice — the same derivation the command makes from a mounted
	// kubeconfig.
	restCfg := rest.CopyConfig(kcpConfig)
	restCfg.Host = bootstrap.WorkspaceConfig(restCfg, layout.Exports).Host

	common := platformmeshconfig.NewDefaultConfig()
	common.MaxConcurrentReconciles = 1

	ctx, cancel := context.WithCancel(context.Background())
	tb.Cleanup(cancel)

	managers, err := operator.Build(ctx, operator.Options{
		RestConfig: restCfg,
		Config:     cfg,
		Layout:     layout,
		Identities: identities,
		Scheme:     testScheme,
		Common:     common,
		// No metrics, no health, no leader election: several fleets can run in one
		// test binary and two managers binding the same port would fail the second.
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
		// Several fleets run in one test binary — one per test, so that one test's
		// reconciles cannot be seen by another's assertions. controller-runtime
		// otherwise refuses the second registration of "UserReconciler".
		SkipNameValidation: true,
	})
	require.NoError(tb, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := managers.Start(ctx); err != nil && ctx.Err() == nil {
			tb.Errorf("managers stopped: %v", err)
		}
	}()
	tb.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			tb.Log("managers did not stop within 30s")
		}
	})
}

// fleetOption configures a fleet before its operator starts.
type fleetOption func(*config.OperatorConfig)

// withoutPersonalTenants is the group-driven shape: nobody gets a home tenant,
// and every Tenant is provisioned deliberately by somebody who already has one.
func withoutPersonalTenants() fleetOption {
	return func(c *config.OperatorConfig) { c.Tenancy.PersonalTenantsEnabled = false }
}

// provisionTenant creates a Tenant the way an admin would, rather than the way a
// personal one is seeded, and waits for its workspace.
func (f *fleet) provisionTenant(tb testing.TB, name, displayName, firstAdmin string) *pmtenancyv1alpha1.Tenant {
	tb.Helper()

	tenant := &pmtenancyv1alpha1.Tenant{}
	tenant.Name = name
	tenant.Spec = pmtenancyv1alpha1.TenantSpec{DisplayName: displayName}
	servable(tb, f.layout.Directory, tenant)

	// Retried on conflict, and it genuinely conflicts: the Tenant controller is
	// running and writing status to this object while the test stamps its owner.
	directory := clusterClient(f.layout.Directory)
	require.NoError(tb, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := directory.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: name}, tenant); err != nil {
			return err
		}
		tenant.Status.FirstAdmin = firstAdmin
		return directory.Status().Update(context.Background(), tenant)
	}))

	envtest.Eventually(tb, func() (bool, string) {
		got := &pmtenancyv1alpha1.Tenant{}
		if err := directory.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: name}, got); err != nil {
			return false, fmt.Sprintf("getting the Tenant: %v", err)
		}
		if got.Status.ClusterID == "" {
			return false, "no workspace yet: " + conditionSummary(got.Status.Conditions)
		}
		tenant = got
		return true, ""
	}, 3*time.Minute, time.Second, "the provisioned Tenant must get a workspace")

	return tenant
}

// provisionProject creates a Project inside a Tenant and waits for its workspace,
// which is where work actually happens and where every grant is enforced.
func (f *fleet) provisionProject(tb testing.TB, tenant *pmtenancyv1alpha1.Tenant, name, displayName string) *pmtenancyv1alpha1.Project {
	tb.Helper()

	cl := clusterClient(f.tenantPath(tenant))
	project := &pmtenancyv1alpha1.Project{}
	project.Name = name
	project.Spec = pmtenancyv1alpha1.ProjectSpec{DisplayName: displayName}
	servable(tb, f.tenantPath(tenant), project)

	envtest.Eventually(tb, func() (bool, string) {
		got := &pmtenancyv1alpha1.Project{}
		if err := cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: name}, got); err != nil {
			return false, fmt.Sprintf("getting the Project: %v", err)
		}
		if got.Status.ClusterID == "" {
			return false, "no workspace yet: " + conditionSummary(got.Status.Conditions)
		}
		project = got
		return true, ""
	}, 3*time.Minute, time.Second, "the Project must get a workspace")

	return project
}

// grantUser writes the Membership an admin would create for a person.
func (f *fleet) grantUser(tb testing.TB, tenant *pmtenancyv1alpha1.Tenant, user *pmtenancyv1alpha1.User, scope, project, role string) *pmtenancyv1alpha1.Membership {
	tb.Helper()
	return f.grant(tb, tenant, pmtenancyv1alpha1.MembershipSpec{
		User: user.Name, Scope: scope, Project: project, Role: role,
	})
}

// grantGroup writes the Membership an admin would create for a group.
func (f *fleet) grantGroup(tb testing.TB, tenant *pmtenancyv1alpha1.Tenant, group, scope, project, role string) *pmtenancyv1alpha1.Membership {
	tb.Helper()
	return f.grant(tb, tenant, pmtenancyv1alpha1.MembershipSpec{
		Group: group, Scope: scope, Project: project, Role: role,
	})
}

// grant creates the Membership with the name the virtual workspace would derive,
// so a repeated grant collides on AlreadyExists rather than producing a second
func (f *fleet) grant(tb testing.TB, tenant *pmtenancyv1alpha1.Tenant, spec pmtenancyv1alpha1.MembershipSpec) *pmtenancyv1alpha1.Membership {
	tb.Helper()

	m := &pmtenancyv1alpha1.Membership{}
	m.Spec = spec
	m.Name = membership.NameFor(m.SubjectKind(), m.SubjectName(), spec.Scope, spec.Project)
	require.NoError(tb, clusterClient(f.tenantPath(tenant)).Create(context.Background(), m))
	return m
}

// revoke deletes a Membership and waits for it to actually go.
func (f *fleet) revoke(tb testing.TB, tenant *pmtenancyv1alpha1.Tenant, m *pmtenancyv1alpha1.Membership) {
	tb.Helper()

	cl := clusterClient(f.tenantPath(tenant))
	require.NoError(tb, cl.Delete(context.Background(), m))

	envtest.Eventually(tb, func() (bool, string) {
		err := cl.Get(context.Background(), ctrlruntimeclient.ObjectKey{Name: m.Name}, &pmtenancyv1alpha1.Membership{})
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		return false, "the Membership is still present, so a finalizer is holding it"
	}, 2*time.Minute, time.Second, "the finalizer chain must complete")
}

// awaitDenied waits until kcp stops admitting a subject.
func (f *fleet) awaitDenied(tb testing.TB, cluster, user string, groups []string, msg string) {
	tb.Helper()
	envtest.Eventually(tb, func() (bool, string) {
		if allowed(tb, cluster, user, groups, "get", "", "configmaps") {
			return false, "still admitted"
		}
		return true, ""
	}, 3*time.Minute, time.Second, "%s", msg)
}

// awaitAdmitted waits until kcp answers yes for a subject in a workspace.
func (f *fleet) awaitAdmitted(t *testing.T, cluster, user string, groups []string, msg string, args ...any) {
	t.Helper()
	envtest.Eventually(t, func() (bool, string) {
		if allowed(t, cluster, user, groups, "get", "", "configmaps") {
			return true, ""
		}
		return false, "not admitted yet"
	}, 3*time.Minute, time.Second, append([]any{msg}, args...)...)
}

// awaitSeededTenant waits for the User controller to seed a personal Tenant and
// for the Tenant controller to give it a workspace.
func (f *fleet) awaitSeededTenant(t *testing.T, user *pmtenancyv1alpha1.User) *pmtenancyv1alpha1.Tenant {
	t.Helper()

	var tenant *pmtenancyv1alpha1.Tenant
	envtest.Eventually(t, func() (bool, string) {
		got := &pmtenancyv1alpha1.User{}
		if err := clusterClient(f.layout.Directory).Get(context.Background(),
			ctrlruntimeclient.ObjectKey{Name: user.Name}, got); err != nil {
			return false, fmt.Sprintf("getting the User: %v", err)
		}
		if got.Status.DefaultTenant == "" {
			return false, "no tenant seeded yet: " + conditionSummary(got.Status.Conditions)
		}

		candidate := &pmtenancyv1alpha1.Tenant{}
		if err := clusterClient(f.layout.Directory).Get(context.Background(),
			ctrlruntimeclient.ObjectKey{Name: got.Status.DefaultTenant}, candidate); err != nil {
			return false, fmt.Sprintf("getting the Tenant: %v", err)
		}
		if candidate.Status.ClusterID == "" {
			return false, "the tenant has no workspace yet: " + conditionSummary(candidate.Status.Conditions)
		}
		tenant = candidate
		return true, ""
	}, 3*time.Minute, time.Second, "a personal Tenant with a workspace must be seeded")

	return tenant
}

// awaitSeededProject waits for the first Project inside the personal Tenant, and
// for its workspace.
func (f *fleet) awaitSeededProject(t *testing.T, tenant *pmtenancyv1alpha1.Tenant) *pmtenancyv1alpha1.Project {
	t.Helper()

	var project *pmtenancyv1alpha1.Project
	envtest.Eventually(t, func() (bool, string) {
		list := &pmtenancyv1alpha1.ProjectList{}
		if err := clusterClient(f.tenantPath(tenant)).List(context.Background(), list); err != nil {
			return false, fmt.Sprintf("listing Projects: %v", err)
		}
		for i := range list.Items {
			if list.Items[i].Status.ClusterID != "" {
				project = &list.Items[i]
				return true, ""
			}
		}
		return false, fmt.Sprintf("no ready Project yet (%d present)", len(list.Items))
	}, 3*time.Minute, time.Second, "a Project with a workspace must be seeded in the personal Tenant")

	return project
}

func conditionSummary(conds []metav1.Condition) string {
	out := ""
	for _, c := range conds {
		out += fmt.Sprintf("%s=%s(%s) ", c.Type, c.Status, c.Reason)
	}
	if out == "" {
		return "no conditions"
	}
	return out
}

// awaitGroupIndexed waits until the group's read model carries a row for a
// tenant.
func (f *fleet) awaitGroupIndexed(tb testing.TB, group, tenantUUID string) {
	tb.Helper()

	name, err := identity.GroupName(group)
	require.NoError(tb, err)

	envtest.Eventually(tb, func() (bool, string) {
		gmi := &pmtenancyv1alpha1.GroupMembershipIndex{}
		if err := clusterClient(f.layout.Directory).Get(context.Background(),
			ctrlruntimeclient.ObjectKey{Name: name}, gmi); err != nil {
			return false, fmt.Sprintf("getting the group index: %v", err)
		}
		if gmi.Spec.Group != group {
			return false, fmt.Sprintf("index is for %q, not %q", gmi.Spec.Group, group)
		}
		for _, e := range gmi.Spec.Entries {
			if e.TenantUUID == tenantUUID {
				return true, ""
			}
		}
		return false, fmt.Sprintf("no row for tenant %s yet (%d rows)", tenantUUID, len(gmi.Spec.Entries))
	}, 2*time.Minute, time.Second, "the group index must carry the grant")
}

// awaitVerb waits until kcp's answer for one verb settles on want.
func (f *fleet) awaitVerb(tb testing.TB, cluster, user string, groups []string, verb, apiGroup, resource string, want bool, msg string) {
	tb.Helper()
	envtest.Eventually(tb, func() (bool, string) {
		got := allowed(tb, cluster, user, groups, verb, apiGroup, resource)
		if got == want {
			return true, ""
		}
		return false, fmt.Sprintf("allowed(%s %s)=%t, want %t", verb, resource, got, want)
	}, 3*time.Minute, time.Second, "%s", msg)
}
