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
	"net/http"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	platformmeshcontext "go.platform-mesh.io/golang-commons/context"
	"go.platform-mesh.io/golang-commons/traces"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/deployer"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/ocm"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/providers/multi"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var operatorCmd = &cobra.Command{
	Use:   "operator",
	Short: "operator to deploy Platform Mesh installations",
	Run:   RunController,
}

func RunController(_ *cobra.Command, _ []string) { // coverage-ignore
	var err error

	ctrl.SetLogger(log.ComponentLogger("controller-runtime").Logr())

	ctx, _, shutdown := platformmeshcontext.StartContext(log, operatorCfg, defaultCfg.ShutdownTimeout)
	defer shutdown()

	disableHTTP2 := func(c *tls.Config) {
		log.Info().Msg("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	var tlsOpts []func(*tls.Config)
	if !defaultCfg.EnableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	if defaultCfg.Tracing.Enabled {
		traceShutdown, err := traces.InitProvider(ctx, defaultCfg.Tracing.Collector)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to start gRPC-Sidecar TracerProvider")
		}
		defer func() {
			if err := traceShutdown(ctx); err != nil {
				log.Error().Err(err).Msg("unable to shutdown TracerProvider")
			}
		}()
	}

	restCfg := ctrl.GetConfigOrDie()
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

	provider := multi.New(multi.Options{})

	mgr, err := mcmanager.New(restCfg, provider, mcmanager.Options{
		Scheme: deployer.NewScheme(),
		Metrics: metricsserver.Options{
			BindAddress:   defaultCfg.Metrics.BindAddress,
			SecureServing: defaultCfg.Metrics.Secure,
			TLSOpts:       tlsOpts,
		},
		BaseContext:                   func() context.Context { return ctx },
		HealthProbeBindAddress:        defaultCfg.HealthProbeBindAddress,
		LeaderElection:                defaultCfg.LeaderElectionEnabled,
		LeaderElectionID:              "platform-mesh-deployer.platform-mesh.io",
		LeaderElectionConfig:          leaderCfg,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("unable to start manager")
	}

	cfg := operatorCfg.DeployerConfig(mgr, log, ocm.New())

	if err := deployer.AddProviders(provider, mgr, cfg); err != nil {
		log.Fatal().Err(err).Msg("unable to add providers")
	}

	if err := deployer.Setup(mgr, cfg); err != nil {
		log.Fatal().Err(err).Msg("unable to set up controllers")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Fatal().Err(err).Msg("unable to set up health check")
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Fatal().Err(err).Msg("unable to set up ready check")
	}

	log.Info().Msg("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Fatal().Err(err).Msg("problem running manager")
	}
}
