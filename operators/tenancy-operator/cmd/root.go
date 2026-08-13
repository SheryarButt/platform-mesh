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
	"github.com/spf13/cobra"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	platformmeshconfig "go.platform-mesh.io/golang-commons/config"
	"go.platform-mesh.io/golang-commons/logger"
	"go.platform-mesh.io/tenancy-operator/internal/config"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpcorev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

var (
	scheme      = runtime.NewScheme()
	operatorCfg config.OperatorConfig
	defaultCfg  *platformmeshconfig.CommonServiceConfig
	log         *logger.Logger
)

var rootCmd = &cobra.Command{
	Use:   "tenancy-operator",
	Short: "operator reconciling Tenants, Workspaces and Memberships",
	// A failing `init` container should print why it failed, not the flag help.
	// Cobra prints usage on any RunE error by default, which buries the cause
	// under sixty lines of unrelated text in the one place someone is reading
	// logs to find it.
	SilenceUsage: true,
}

func init() {
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(scheme))
	utilruntime.Must(kcpapisv1alpha1.AddToScheme(scheme))
	utilruntime.Must(kcpcorev1alpha1.AddToScheme(scheme))
	utilruntime.Must(kcptenancyv1alpha1.AddToScheme(scheme))
	// RBAC is what a Membership becomes inside a workspace, so the scheme needs
	// it even before the Membership reconciler lands.
	utilruntime.Must(rbacv1.AddToScheme(scheme))
	// Namespaces: the `workspace` WorkspaceType omits `extend: root:universal`,
	// so kcp creates no `default` and WorkspaceReconciler must.
	utilruntime.Must(corev1.AddToScheme(scheme))

	rootCmd.AddCommand(operatorCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(virtualWorkspaceCmd)

	defaultCfg = platformmeshconfig.NewDefaultConfig()
	operatorCfg = config.NewOperatorConfig()
	defaultCfg.AddFlags(rootCmd.PersistentFlags())
	// Persistent, so `init` and `operator` take the SAME --paths-* values. The
	// installer building one tree while the operator watches another is the
	// failure this arrangement is designed to make impossible.
	operatorCfg.AddFlags(rootCmd.PersistentFlags())

	cobra.OnInitialize(initLog)
}

func initLog() { // coverage-ignore
	logcfg := logger.DefaultConfig()
	logcfg.Level = defaultCfg.Log.Level
	logcfg.NoJSON = defaultCfg.Log.NoJson

	var err error
	log, err = logger.New(logcfg)
	if err != nil {
		panic(err)
	}
	ctrl.SetLogger(log.Logr())
}

// Execute runs the root command.
func Execute() { // coverage-ignore
	cobra.CheckErr(rootCmd.Execute())
}
