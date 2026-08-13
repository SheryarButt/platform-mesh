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
	"crypto/tls"
	"net/http"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	platformmeshcontext "go.platform-mesh.io/golang-commons/context"
	"go.platform-mesh.io/golang-commons/traces"
	"go.platform-mesh.io/tenancy-operator/internal/bootstrap"
	"go.platform-mesh.io/tenancy-operator/internal/operator"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

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

	// Everything from here is the wiring, and it lives in internal/operator so the
	// e2e suite can run exactly this against a real kcp. What used to be inline
	// could only be exercised by starting a process, which left the question of
	// which controller reaches which export — the question that decides whether
	// anything reconciles — as the least tested part of the operator.
	managers, err := operator.Build(ctx, operator.Options{
		RestConfig:             restCfg,
		Config:                 operatorCfg,
		Layout:                 layout,
		Identities:             identities,
		Scheme:                 scheme,
		Common:                 defaultCfg,
		MetricsBindAddress:     defaultCfg.Metrics.BindAddress,
		MetricsSecure:          defaultCfg.Metrics.Secure,
		MetricsTLSOpts:         tlsOpts,
		HealthProbeBindAddress: defaultCfg.HealthProbeBindAddress,
		LeaderElection:         defaultCfg.LeaderElectionEnabled,
		LeaderElectionConfig:   leaderCfg,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("unable to build the managers")
	}
	platformMgr := managers.Platform

	if err := platformMgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Fatal().Err(err).Msg("unable to set up health check")
	}
	if err := platformMgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Fatal().Err(err).Msg("unable to set up ready check")
	}

	signalCtx := ctrl.SetupSignalHandler()

	log.Info().Msg("starting managers")
	if err := managers.Start(signalCtx); err != nil {
		log.Fatal().Err(err).Msg("problem running managers")
	}
}
