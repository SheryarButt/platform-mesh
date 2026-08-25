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

// Command kro-composition-operator watches ResourceGraphDefinitions across every workspace
// that installed KROaaS and publishes each composite type as a bound API.
package main

import (
	"context"
	"flag"
	"os"
	"sort"
	"strings"
	"time"

	krov1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/dynamiccontroller"
	krofeatures "github.com/kubernetes-sigs/kro/pkg/features"
	krometrics "github.com/kubernetes-sigs/kro/pkg/metrics"

	"go.platform-mesh.io/kro-composition-operator/internal/engine"
	"go.platform-mesh.io/kro-composition-operator/internal/workspace"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpapisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func main() {
	var sliceName, providerWS, healthAddr, metricsAddr string
	var leaderElect bool
	flag.StringVar(&sliceName, "apiexport-endpointslice", "kro.run", "APIExportEndpointSlice serving the kro.run RGD API")
	flag.StringVar(&providerWS, "provider-workspace", "root:providers:kro-provider", "workspace path holding the kro.run APIExport + endpointslice")
	flag.StringVar(&healthAddr, "health-probe-bind-address", ":8081", "address the health/readiness probe endpoint binds to")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":9090", "address the metrics endpoint binds to ('0' disables it)")
	flag.BoolVar(&leaderElect, "leader-elect", false, "enable leader election for controller manager (ensures a single active instance)")
	// The gates belong to the embedded kro engine and are read from its global gate, so
	// setting them here reaches both the graph builder and the instance reconciler.
	flag.Func("feature-gates", "comma-separated kro feature gates as key=value ("+
		strings.Join(krofeatures.FeatureGate.KnownFeatures(), ", ")+")", krofeatures.FeatureGate.Set)
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	logf.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := logf.Log.WithName("setup")

	base := config.GetConfigOrDie()
	shardBase := stripClusters(base.Host)

	// Config pointing at the provider workspace (the APIExport + endpointslice live there).
	providerCfg := rest.CopyConfig(base)
	providerCfg.Host = shardBase + "/clusters/" + providerWS

	// Base used to reach each consumer workspace directly for writes.
	writeBase := rest.CopyConfig(base)
	writeBase.Host = shardBase

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		kcpapisv1alpha1.AddToScheme,
		kcpapisv1alpha2.AddToScheme,
		kcpcorev1alpha1.AddToScheme,
		kcptenancyv1alpha1.AddToScheme,
		krov1alpha1.AddToScheme,
		apiextensionsv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			setupLog.Error(err, "scheme")
			os.Exit(1)
		}
	}

	// kro defines its collectors but registers them nowhere, so without this its metrics go
	// nowhere. No name overlap with controller-runtime's.
	krometrics.Register(crmetrics.Registry)

	// The lease namespace is this pod's, which exists in the host cluster and not in a kcp
	// workspace, so leader election has to point at the host cluster.
	var leaderCfg *rest.Config
	if leaderElect {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			setupLog.Error(err, "in-cluster config for leader election")
			os.Exit(1)
		}
		leaderCfg = cfg
	}

	provider, err := apiexport.New(providerCfg, sliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "apiexport provider")
		os.Exit(1)
	}
	mgr, err := mcmanager.New(providerCfg, provider, manager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: healthAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "kro-composition-operator.platform-mesh.io",
		LeaderElectionConfig:   leaderCfg,
		// Give up the lease on graceful shutdown so a rolling update fails over in seconds
		// rather than waiting for the lease to expire.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "mc manager")
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "healthz")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "readyz")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	wsProvider := workspace.NewProvider(writeBase, scheme)

	// kro's defaults, but applied per consumer workspace rather than per cluster, so these
	// budgets are per tenant and scale with workspace count.
	eng := engine.New(ctx, wsProvider, dynamiccontroller.Config{
		Workers:         1,
		ResyncPeriod:    10 * time.Hour,
		QueueMaxRetries: 20,
		MinRetryDelay:   200 * time.Millisecond,
		MaxRetryDelay:   1000 * time.Second,
		RateLimit:       10,
		BurstLimit:      100,
	})

	reconLog := logf.Log.WithName("reconcile")
	rec := mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
		err := eng.ReconcileRGD(ctx, string(req.ClusterName), req.Name)
		if err == nil {
			return ctrl.Result{}, nil
		}
		// Expected, self-resolving conditions requeue quietly; real failures are logged.
		if engine.IsTransient(err) {
			reconLog.V(1).Info("requeue (transient)", "cluster", string(req.ClusterName), "rgd", req.Name, "reason", err.Error())
		} else {
			reconLog.Error(err, "reconcile failed", "cluster", string(req.ClusterName), "rgd", req.Name)
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	})

	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("kro-composition").
		For(&krov1alpha1.ResourceGraphDefinition{}).
		Complete(rec); err != nil {
		setupLog.Error(err, "builder")
		os.Exit(1)
	}

	setupLog.Info("starting kro-composition-operator", "endpointslice", sliceName, "providerWorkspace", providerWS, "featureGates", enabledFeatures())
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "manager exited")
		os.Exit(1)
	}
}

// enabledFeatures lists the kro feature gates that are on, so the startup log shows whether
// a --feature-gates value took effect.
func enabledFeatures() []string {
	var on []string
	for name := range krofeatures.FeatureGate.GetAll() {
		if krofeatures.FeatureGate.Enabled(name) {
			on = append(on, string(name))
		}
	}
	sort.Strings(on)
	return on
}

// stripClusters returns the shard base URL (scheme://authority) from a kcp host
// that may carry a /clusters/<path> suffix.
func stripClusters(host string) string {
	if i := strings.Index(host, "/clusters/"); i >= 0 {
		return host[:i]
	}
	return strings.TrimSuffix(host, "/")
}
