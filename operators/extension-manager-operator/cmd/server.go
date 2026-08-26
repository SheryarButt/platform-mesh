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
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"

	pmuiv1alpha1 "go.platform-mesh.io/apis/ui/v1alpha1"
	"go.platform-mesh.io/extension-manager-operator/internal/server"
	"go.platform-mesh.io/extension-manager-operator/pkg/validation"
	platformmeshcontext "go.platform-mesh.io/golang-commons/context"
	"go.platform-mesh.io/golang-commons/traces"

	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "server with configuration validation endpoint",
	Run:   RunServer,
}

func RunServer(_ *cobra.Command, _ []string) { // coverage-ignore
	ctrl.SetLogger(log.ComponentLogger("srv").Logr())

	ctx, cancelMain, shutdown := platformmeshcontext.StartContext(log, operatorCfg, defaultCfg.ShutdownTimeout)
	defer shutdown()

	var err error
	var providerShutdown func(ctx context.Context) error
	if defaultCfg.Tracing.Enabled {
		providerShutdown, err = traces.InitProvider(ctx, defaultCfg.Tracing.Collector)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to start gRPC-Sidecar TracerProvider")
		}
	} else {
		providerShutdown, err = traces.InitLocalProvider(ctx, defaultCfg.Tracing.Collector, false)
		if err != nil {
			log.Fatal().Err(err).Msg("unable to start local TracerProvider")
		}
	}

	defer func() {
		if err := providerShutdown(ctx); err != nil {
			log.Fatal().Err(err).Msg("failed to shutdown TracerProvider")
		}
	}()

	// Create Prometheus metrics handler
	metricsHandler := promhttp.Handler()

	// Initialize entity type registry from cluster if enabled
	var registry *validation.EntityTypeRegistry
	if serverCfg.EntityTypeValidationEnabled {
		registry = initServerEntityTypeRegistry(ctx)
	}

	// Register Prometheus metrics endpoint
	rt := server.CreateRouter(defaultCfg.IsLocal, log, validation.NewContentConfiguration(), registry)
	rt.Handle("/metrics", metricsHandler)

	srv := &http.Server{
		Addr:         ":" + serverCfg.ServerPort,
		Handler:      rt,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("Server failed")
			cancelMain(err)
		}
	}()
	log.Info().Msg("Server started")

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = srv.Shutdown(shutdownCtx)
	if err != nil {
		log.Error().Err(err).Msg("Graceful shutdown failed")
	}
	log.Info().Msg("Server stopped")
}

func initServerEntityTypeRegistry(ctx context.Context) *validation.EntityTypeRegistry { // coverage-ignore
	registry := validation.NewEntityTypeRegistry()

	k8sClient, err := buildServerK8sClient()
	if err != nil {
		log.Fatal().Err(err).Msg("entity type validation is enabled but failed to connect to cluster")
	}

	loadRegistryFromCluster(ctx, k8sClient, registry)
	log.Info().Int("entityTypes", len(registry.KnownTypes())).Msg("initialized server entity type registry")
	go refreshRegistryPeriodically(ctx, k8sClient, registry)
	return registry
}

func buildServerK8sClient() (ctrlruntimeclient.Reader, error) { // coverage-ignore
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}

	k8sClient, err := ctrlruntimeclient.New(restCfg, ctrlruntimeclient.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	return k8sClient, nil
}

func loadRegistryFromCluster(ctx context.Context, k8sClient ctrlruntimeclient.Reader, registry *validation.EntityTypeRegistry) { // coverage-ignore
	var ccList pmuiv1alpha1.ContentConfigurationList
	if err := k8sClient.List(ctx, &ccList); err != nil {
		log.Warn().Err(err).Msg("failed to list ContentConfigurations, entity type validation will only recognize 'global'")
		return
	}

	var configs []validation.ContentConfiguration
	for _, cc := range ccList.Items {
		if cc.Status.ConfigurationResult == "" {
			continue
		}
		var parsed validation.ContentConfiguration
		if err := json.Unmarshal([]byte(cc.Status.ConfigurationResult), &parsed); err != nil {
			log.Warn().Err(err).Str("name", cc.Name).Msg("failed to parse ConfigurationResult for entity type registry")
			continue
		}
		configs = append(configs, parsed)
	}

	registry.Bulkload(configs)
	log.Debug().Int("entityTypes", len(registry.KnownTypes())).Msg("refreshed server entity type registry")
}

func refreshRegistryPeriodically(ctx context.Context, k8sClient ctrlruntimeclient.Reader, registry *validation.EntityTypeRegistry) { // coverage-ignore
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			loadRegistryFromCluster(ctx, k8sClient, registry)
		}
	}
}
