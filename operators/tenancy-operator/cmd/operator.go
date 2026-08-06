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

package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	platformmeshcontext "go.platform-mesh.io/golang-commons/context"
	"go.platform-mesh.io/golang-commons/traces"
	"go.platform-mesh.io/tenancy-operator/internal/bootstrap"
	"go.platform-mesh.io/tenancy-operator/internal/controller/memberships"
	"go.platform-mesh.io/tenancy-operator/internal/controller/projects"
	"go.platform-mesh.io/tenancy-operator/internal/controller/tenants"
	"go.platform-mesh.io/tenancy-operator/internal/controller/users"
	"go.platform-mesh.io/tenancy-operator/internal/controller/workspaces"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	pathaware "github.com/kcp-dev/multicluster-provider/path-aware"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var operatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "reconcile Tenants, Workspaces and Memberships",
	Run:   RunController,
}

// RunController wires one manager per APIExport virtual workspace and starts them.
//
// There is more than one manager because a controller cannot watch across logical
// clusters by wishing: kcp gives it exactly one wildcard endpoint, an APIExport's
// virtual workspace, spanning every workspace that binds that export. The tenancy
// APIs are split across several exports by audience and by capability, so reading
// the fleet means reading through several of them.
//
// What this buys: no long-running component holds cluster-admin on kcp. Every
// write is bounded by the claim list on the export it went through, readable per
// workspace with `kubectl get apibindings`.
func RunController(_ *cobra.Command, _ []string) { // coverage-ignore
	ctrl.SetLogger(log.ComponentLogger("controller-runtime").Logr())

	if err := operatorCfg.Validate(); err != nil {
		log.Fatal().Err(err).Msg("invalid configuration")
	}
	layout, err := operatorCfg.Layout()
	if err != nil {
		log.Fatal().Err(err).Msg("resolving workspace layout")
	}
	identities, err := operatorCfg.IdentityResolver()
	if err != nil {
		log.Fatal().Err(err).Msg("resolving OIDC identity configuration")
	}
	if identities.Mutable() {
		// Not fatal, but it is the failure mode that is hardest to notice: an
		// address change silently invalidates that one user's role bindings.
		log.Warn().
			Str("usernameClaim", operatorCfg.OIDC.UsernameClaim).
			Msg("configured username claim is mutable; an address change is a per-user identity migration")
	}
	log.Info().
		Str("root", layout.Root).
		Str("tenantFleetRoot", layout.TenantFleetRoot).
		Str("directory", layout.Directory).
		Str("exports", layout.Exports).
		Msg("resolved workspace layout")

	ctx, _, shutdown := platformmeshcontext.StartContext(log, operatorCfg, defaultCfg.ShutdownTimeout)
	defer shutdown()

	var tlsOpts []func(*tls.Config)
	if !defaultCfg.EnableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			log.Info().Msg("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		})
	}

	if defaultCfg.Tracing.Enabled {
		traceShutdown, err := traces.InitProvider(ctx, defaultCfg.Tracing.Collector)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to start gRPC-Sidecar TracerProvider")
		}
		defer func() {
			if err := traceShutdown(ctx); err != nil {
				log.Error().Err(err).Msg("failed to shutdown TracerProvider")
			}
		}()
	}

	restCfg := ctrl.GetConfigOrDie()

	// Point at the EXPORTS workspace, computed from the same --paths-* flags the
	// installer used rather than from a pre-scoped kubeconfig. That is the only
	// thing this static credential is for: resolving the APIExportEndpointSlices.
	// Everything after that goes through the virtual workspace URLs those slices
	// publish, bounded by each export's permission claims.
	//
	// Deriving it here means one mounted kubeconfig serves both `init` and
	// `operator`, and neither can be pointed at a workspace the other did not use.
	restCfg = bootstrap.Retarget(restCfg, operatorCfg.Kcp.Server, operatorCfg.Kcp.ServerName)
	restCfg.Host = bootstrap.WorkspaceConfig(restCfg, layout.Exports).Host

	restCfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return otelhttp.NewTransport(rt)
	})

	var leaderCfg *rest.Config
	if defaultCfg.LeaderElectionEnabled {
		leaderCfg, err = rest.InClusterConfig()
		if err != nil {
			log.Fatal().Err(err).Msg("unable to get in-cluster config")
		}
	}

	// Primary manager: the tenancy-platform export. It hosts every controller,
	// serves metrics and health, and is the one that takes the leader lock.
	platformMgr, err := newManager(restCfg, operatorCfg.Kcp.PlatformEndpointSlice, mcmanager.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   defaultCfg.Metrics.BindAddress,
			SecureServing: defaultCfg.Metrics.Secure,
			TLSOpts:       tlsOpts,
		},
		BaseContext:                   func() context.Context { return ctx },
		HealthProbeBindAddress:        defaultCfg.HealthProbeBindAddress,
		LeaderElection:                defaultCfg.LeaderElectionEnabled,
		LeaderElectionID:              "tenancy-operator.platform-mesh.io",
		LeaderElectionConfig:          leaderCfg,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		log.Fatal().Err(err).Str("export", operatorCfg.Kcp.PlatformEndpointSlice).Msg("unable to start manager")
	}

	// Secondary manager: the tenancy-provisioner export. It hosts NO controllers
	// — it exists so the Tenant chain can reach the fleet root, where Tenant
	// workspaces are created, through that export's single claim. Metrics, health
	// and leader election stay on the primary so the two cannot contend for a
	// port or a lock.
	provisionerMgr, err := newManager(restCfg, operatorCfg.Kcp.ProvisionerEndpointSlice, mcmanager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		BaseContext:            func() context.Context { return ctx },
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		log.Fatal().Err(err).Str("export", operatorCfg.Kcp.ProvisionerEndpointSlice).Msg("unable to start manager")
	}

	// Third manager: the tenancy-access export. Like the provisioner it hosts no
	// controllers — it is how WorkspaceReconciler writes INTO a tenant workspace,
	// bounded by that export's claims. Reading Workspaces and writing into them go
	// through different exports on purpose, so neither claim list has to widen to
	// cover the other.
	accessMgr, err := newManager(restCfg, operatorCfg.Kcp.AccessEndpointSlice, mcmanager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		BaseContext:            func() context.Context { return ctx },
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		log.Fatal().Err(err).Str("export", operatorCfg.Kcp.AccessEndpointSlice).Msg("unable to start manager")
	}

	// Fourth manager: the tenancy export. It owns `memberships`, which live inside
	// each Tenant workspace — neither the platform nor the provisioner
	// export can reach them, so seeding an owner's grant needs this one.
	tenancyMgr, err := newManager(restCfg, operatorCfg.Kcp.TenancyEndpointSlice, mcmanager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		BaseContext:            func() context.Context { return ctx },
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		log.Fatal().Err(err).Str("export", operatorCfg.Kcp.TenancyEndpointSlice).Msg("unable to start manager")
	}

	if operatorCfg.Controllers.User.Enabled {
		r, err := users.NewReconciler(platformMgr, provisionerMgr, tenancyMgr, layout, identities, operatorCfg)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to create user reconciler")
		}
		if err := r.SetupWithManager(platformMgr, defaultCfg); err != nil {
			log.Fatal().Err(err).Str("controller", "User").Msg("unable to create controller")
		}
	}

	if operatorCfg.Controllers.Tenant.Enabled {
		r, err := tenants.NewReconciler(platformMgr, provisionerMgr, tenancyMgr, layout, operatorCfg)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to create tenant reconciler")
		}
		if err := r.SetupWithManager(platformMgr, defaultCfg); err != nil {
			log.Fatal().Err(err).Str("controller", "Tenant").Msg("unable to create controller")
		}
	}

	if operatorCfg.Controllers.Workspace.Enabled {
		// Registered on the PROVISIONER manager: Workspace objects live in the
		// workspaces that bind that export, not in the tenant workspaces the
		// reconciler writes to.
		r, err := workspaces.NewReconciler(provisionerMgr, accessMgr, &operatorCfg)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to create workspace reconciler")
		}
		if err := r.SetupWithManager(provisionerMgr); err != nil {
			log.Fatal().Err(err).Str("controller", "Workspace").Msg("unable to create controller")
		}
	}

	if operatorCfg.Controllers.Membership.Enabled {
		// Registered on the ACCESS manager, not the tenancy one: role bindings are
		// only visible through that export. It repairs by nudging the Membership,
		// because a builder cannot take a source from another manager.
		b := memberships.NewBindingReconciler(accessMgr, tenancyMgr)
		if err := b.SetupWithManager(accessMgr, defaultCfg); err != nil {
			log.Fatal().Err(err).Str("controller", "MembershipBinding").Msg("unable to create controller")
		}
	}

	if operatorCfg.Controllers.Project.Enabled {
		r, err := projects.NewReconciler(provisionerMgr, tenancyMgr, layout, operatorCfg)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to create project reconciler")
		}
		if err := r.SetupWithManager(tenancyMgr, defaultCfg); err != nil {
			log.Fatal().Err(err).Str("controller", "Project").Msg("unable to create controller")
		}
	}

	if operatorCfg.Controllers.Membership.Enabled {
		// Registered on the TENANCY manager, which is where Memberships are
		// readable; it writes through the access manager into whichever workspace
		// the Membership targets.
		r, err := memberships.NewReconciler(platformMgr, tenancyMgr, accessMgr, layout, identities, operatorCfg)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to create membership reconciler")
		}
		if err := r.SetupWithManager(tenancyMgr, defaultCfg); err != nil {
			log.Fatal().Err(err).Str("controller", "Membership").Msg("unable to create controller")
		}
	}

	if err := platformMgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Fatal().Err(err).Msg("unable to set up health check")
	}
	if err := platformMgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Fatal().Err(err).Msg("unable to set up ready check")
	}

	signalCtx := ctrl.SetupSignalHandler()

	// The provisioner manager only serves clients, so a failure there must take
	// the process down rather than leave the primary reconciling against a
	// manager whose cache never warmed.
	go func() {
		if err := provisionerMgr.Start(signalCtx); err != nil {
			log.Fatal().Err(err).Str("export", operatorCfg.Kcp.ProvisionerEndpointSlice).Msg("provisioner manager stopped")
		}
	}()

	go func() {
		if err := tenancyMgr.Start(signalCtx); err != nil {
			log.Fatal().Err(err).Str("export", operatorCfg.Kcp.TenancyEndpointSlice).Msg("tenancy manager stopped")
		}
	}()

	go func() {
		if err := accessMgr.Start(signalCtx); err != nil {
			log.Fatal().Err(err).Str("export", operatorCfg.Kcp.AccessEndpointSlice).Msg("access manager stopped")
		}
	}()

	log.Info().Msg("starting manager")
	if err := platformMgr.Start(signalCtx); err != nil {
		log.Fatal().Err(err).Msg("problem running manager")
	}
}

// newManager builds a multicluster manager over one APIExport virtual workspace.
// The provider is path-aware so a subroutine can address a workspace by path
// (root:tenants) and not only by logical cluster ID — a path resolves exactly when
// that workspace binds this export, which is what keeps reach tied to bindings.
func newManager(restCfg *rest.Config, endpointSlice string, opts mcmanager.Options) (mcmanager.Manager, error) { // coverage-ignore
	provider, err := pathaware.New(restCfg, endpointSlice, apiexport.Options{
		Log:    &ctrl.Log,
		Scheme: opts.Scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("creating APIExport provider for %q: %w", endpointSlice, err)
	}
	return mcmanager.New(restCfg, provider, opts)
}
