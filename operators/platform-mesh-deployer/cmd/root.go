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

	platformmeshconfig "go.platform-mesh.io/golang-commons/config"
	"go.platform-mesh.io/golang-commons/logger"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/config"

	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	operatorCfg config.OperatorConfig
	defaultCfg  *platformmeshconfig.CommonServiceConfig
	log         *logger.Logger
)

var rootCmd = &cobra.Command{
	Use:   "platform-mesh-deployer",
	Short: "operator to deploy Platform Mesh installations",
}

func init() {
	rootCmd.AddCommand(operatorCmd)

	defaultCfg = platformmeshconfig.NewDefaultConfig()
	operatorCfg = config.NewOperatorConfig()

	defaultCfg.AddFlags(rootCmd.PersistentFlags())
	operatorCfg.AddFlags(operatorCmd.Flags())

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

func Execute() { // coverage-ignore
	cobra.CheckErr(rootCmd.Execute())
}
