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

// Package operator builds and runs the managers this operator reconciles
// through.
//
// Split out of cmd for ONE reason: so a test can run the real thing. What was in
// the cobra handler could only be exercised by starting a process, which meant the
// wiring itself — which controller is registered on which manager, and which
// manager reaches which export — was the least tested part of the operator while
// being the part that decides whether anything reconciles at all.
//
// A controller registered on the wrong manager compiles, starts, logs nothing, and
// reconciles nothing.
package operator

import (
	"context"
	"crypto/tls"
	"fmt"

	platformmeshconfig "go.platform-mesh.io/golang-commons/config"
	"go.platform-mesh.io/tenancy-operator/internal/config"
	"go.platform-mesh.io/tenancy-operator/internal/controller/memberships"
	"go.platform-mesh.io/tenancy-operator/internal/controller/projects"
	"go.platform-mesh.io/tenancy-operator/internal/controller/tenants"
	"go.platform-mesh.io/tenancy-operator/internal/controller/users"
	"go.platform-mesh.io/tenancy-operator/internal/controller/workspaces"
	"go.platform-mesh.io/tenancy-operator/pkg/identity"
	"go.platform-mesh.io/tenancy-operator/pkg/paths"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	pathaware "github.com/kcp-dev/multicluster-provider/path-aware"
)

// Options is everything the wiring needs that is not derived from the operator's
// own configuration.
type Options struct {
	// RestConfig must already be pointed at the EXPORTS workspace: every manager
	// resolves its APIExportEndpointSlice there. Retargeting is the caller's job
	// because `operator` derives it from a mounted kubeconfig and a test derives it
	// from an envtest control plane.
	RestConfig *rest.Config

	Config     config.OperatorConfig
	Layout     paths.Layout
	Identities *identity.Resolver
	Scheme     *runtime.Scheme

	// Common carries the knobs the controllers themselves read, chiefly
	// MaxConcurrentReconciles.
	Common *platformmeshconfig.CommonServiceConfig

	// Serving surfaces. "0" disables one, which is what every manager but the
	// primary uses so that two of them cannot contend for a port.
	MetricsBindAddress     string
	MetricsSecure          bool
	MetricsTLSOpts         []func(*tls.Config)
	HealthProbeBindAddress string

	// LeaderElection is off for a test and for a single-replica dev install.
	LeaderElection       bool
	LeaderElectionConfig *rest.Config

	// SkipNameValidation lifts controller-runtime's requirement that controller
	// names be unique WITHIN A PROCESS.
	//
	// Left false everywhere it matters: the check exists because two controllers
	// sharing a name report the same metric, and in a running operator that is a
	// bug. It is set only by the e2e suite, which runs several complete operators
	// in one test binary on purpose — one per test, so that one test's reconciles
	// are never visible to another's assertions.
	SkipNameValidation bool
}

// Managers is the set this operator runs, one per APIExport.
//
// More than one because a controller cannot watch across logical clusters by
// wishing: kcp gives it exactly one wildcard endpoint per export, and the tenancy
// APIs are split across four by audience and by capability. The split is what
// keeps this process from holding cluster-admin — every write is bounded by the
// claim list on the export it went through.
type Managers struct {
	// Platform reaches the directory: Users, Tenants and the indices. It hosts
	// every controller that watches one of those, serves metrics and health, and
	// takes the leader lock.
	Platform mcmanager.Manager

	// Provisioner reaches the fleet root and every Tenant, through the single
	// claim on `tenancy.kcp.io/workspaces`. Workspaces are created here.
	Provisioner mcmanager.Manager

	// Access reaches INSIDE a child workspace: namespaces, serviceaccounts and
	// RBAC. This is where role bindings are written and watched.
	Access mcmanager.Manager

	// Tenancy spans every Tenant's Memberships through one fleet-wide informer.
	Tenancy mcmanager.Manager
}

// Build creates the four managers and registers every enabled controller on the
// one that can actually see its objects.
//
// Which manager hosts which controller is not arbitrary and not interchangeable:
// a controller watches through the export its manager was built on, so registering
// it on the wrong one produces a process that starts cleanly and never reconciles.
func Build(ctx context.Context, opts Options) (*Managers, error) {
	base := func() mcmanager.Options {
		o := mcmanager.Options{
			Scheme:                 opts.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			BaseContext:            func() context.Context { return ctx },
			HealthProbeBindAddress: "0",
			LeaderElection:         false,
		}
		if opts.SkipNameValidation {
			o.Controller = ctrlconfig.Controller{SkipNameValidation: ptr.To(true)}
		}
		return o
	}

	platformOpts := base()
	platformOpts.Metrics = metricsserver.Options{
		BindAddress:   opts.MetricsBindAddress,
		SecureServing: opts.MetricsSecure,
		TLSOpts:       opts.MetricsTLSOpts,
	}
	platformOpts.HealthProbeBindAddress = opts.HealthProbeBindAddress
	platformOpts.LeaderElection = opts.LeaderElection
	platformOpts.LeaderElectionID = "tenancy-operator.platform-mesh.io"
	platformOpts.LeaderElectionConfig = opts.LeaderElectionConfig
	platformOpts.LeaderElectionReleaseOnCancel = true

	m := &Managers{}
	var err error

	if m.Platform, err = newManager(opts.RestConfig, opts.Config.Kcp.PlatformEndpointSlice, platformOpts); err != nil {
		return nil, err
	}
	if m.Provisioner, err = newManager(opts.RestConfig, opts.Config.Kcp.ProvisionerEndpointSlice, base()); err != nil {
		return nil, err
	}
	if m.Access, err = newManager(opts.RestConfig, opts.Config.Kcp.AccessEndpointSlice, base()); err != nil {
		return nil, err
	}
	if m.Tenancy, err = newManager(opts.RestConfig, opts.Config.Kcp.TenancyEndpointSlice, base()); err != nil {
		return nil, err
	}

	if err := m.register(opts); err != nil {
		return nil, err
	}
	return m, nil
}

// register wires the controllers. Every placement here is load-bearing; the
// comments say which export makes each one's objects visible.
func (m *Managers) register(opts Options) error {
	cfg := opts.Config

	if cfg.Controllers.User.Enabled {
		r, err := users.NewReconciler(m.Platform, m.Provisioner, m.Tenancy, opts.Layout, opts.Identities, cfg)
		if err != nil {
			return fmt.Errorf("creating the user reconciler: %w", err)
		}
		if err := r.SetupWithManager(m.Platform, opts.Common); err != nil {
			return fmt.Errorf("setting up the User controller: %w", err)
		}
	}

	if cfg.Controllers.Tenant.Enabled {
		r, err := tenants.NewReconciler(m.Platform, m.Provisioner, m.Tenancy, opts.Layout, cfg)
		if err != nil {
			return fmt.Errorf("creating the tenant reconciler: %w", err)
		}
		if err := r.SetupWithManager(m.Platform, opts.Common); err != nil {
			return fmt.Errorf("setting up the Tenant controller: %w", err)
		}
	}

	if cfg.Controllers.Workspace.Enabled {
		// On the PROVISIONER manager: Workspace objects live in the workspaces that
		// bind that export, not in the tenant workspaces the reconciler writes to.
		r, err := workspaces.NewReconciler(m.Provisioner, m.Access, &cfg)
		if err != nil {
			return fmt.Errorf("creating the workspace reconciler: %w", err)
		}
		if err := r.SetupWithManager(m.Provisioner); err != nil {
			return fmt.Errorf("setting up the Workspace controller: %w", err)
		}
	}

	if cfg.Controllers.Membership.Enabled {
		// On the ACCESS manager: role bindings are only visible through that export.
		// It repairs by nudging the Membership, because a builder cannot take a
		// source from another manager.
		b := memberships.NewBindingReconciler(m.Access, m.Tenancy)
		if err := b.SetupWithManager(m.Access, opts.Common); err != nil {
			return fmt.Errorf("setting up the MembershipBinding controller: %w", err)
		}
	}

	if cfg.Controllers.Project.Enabled {
		r, err := projects.NewReconciler(m.Provisioner, m.Tenancy, opts.Layout, cfg)
		if err != nil {
			return fmt.Errorf("creating the project reconciler: %w", err)
		}
		if err := r.SetupWithManager(m.Tenancy, opts.Common); err != nil {
			return fmt.Errorf("setting up the Project controller: %w", err)
		}
	}

	if cfg.Controllers.Membership.Enabled {
		// On the TENANCY manager, which is where Memberships are readable; it writes
		// through the access manager into whichever workspace the Membership targets.
		r, err := memberships.NewReconciler(m.Platform, m.Tenancy, m.Access, opts.Layout, opts.Identities, cfg)
		if err != nil {
			return fmt.Errorf("creating the membership reconciler: %w", err)
		}
		if err := r.SetupWithManager(m.Tenancy, opts.Common); err != nil {
			return fmt.Errorf("setting up the Membership controller: %w", err)
		}
	}

	return nil
}

// Start runs all four until ctx is cancelled, and returns the first failure.
//
// The three secondary managers only serve clients, so one of them stopping is not
// survivable: the primary would keep reconciling against a manager whose cache
// never warmed, which looks like a controller that has quietly stopped working
// rather than one that has failed.
func (m *Managers) Start(ctx context.Context) error {
	errs := make(chan error, 4)

	for _, mgr := range []mcmanager.Manager{m.Provisioner, m.Tenancy, m.Access, m.Platform} {
		go func(mgr mcmanager.Manager) {
			errs <- mgr.Start(ctx)
		}(mgr)
	}

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		return nil
	}
}

// newManager builds a multicluster manager over one APIExport virtual workspace.
//
// The provider is path-aware so a subroutine can address a workspace by path
// (root:tenants) and not only by logical cluster ID — a path resolves exactly when
// that workspace binds this export, which is what keeps reach tied to bindings.
func newManager(restCfg *rest.Config, endpointSlice string, opts mcmanager.Options) (mcmanager.Manager, error) {
	provider, err := pathaware.New(restCfg, endpointSlice, apiexport.Options{
		Log:    &ctrl.Log,
		Scheme: opts.Scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("creating APIExport provider for %q: %w", endpointSlice, err)
	}
	return mcmanager.New(restCfg, provider, opts)
}
