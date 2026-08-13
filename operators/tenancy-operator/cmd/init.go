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
	"time"

	"github.com/spf13/cobra"

	"go.platform-mesh.io/tenancy-operator/internal/bootstrap"

	ctrl "sigs.k8s.io/controller-runtime"
)

var initWorkspaceTimeout time.Duration

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "install the tenancy tree into kcp (runs as kcp-admin, then exits)",
	Long: `Install the tenancy tree under --paths-root.

Creates the system workspaces, the four APIExports and their APIResourceSchemas,
the bind RBAC, the APIExportEndpointSlices, the two WorkspaceTypes, and the two
install-time APIBindings.

This is the one part of the model that legitimately needs kcp admin. It is meant
to run as an INIT CONTAINER: it does its work, exits, and takes the admin
credential with it. The operator container that follows holds no such rights — it
reaches the fleet only through APIExport virtual workspaces, bounded by the
permission claims on them.

Idempotent: safe to run on every pod start and on every upgrade.`,
	RunE: func(c *cobra.Command, _ []string) error { return runInit(c) },
}

// runInit installs the tree and exits.
//
// It reads the SAME --paths-* flags the operator does, so the tree that gets built
// and the tree that gets watched cannot disagree — the failure that would
// otherwise produce a healthy-looking operator reconciling nothing.
func runInit(c *cobra.Command) error {
	if err := operatorCfg.Validate(); err != nil {
		return err
	}
	layout, err := operatorCfg.Layout()
	if err != nil {
		return err
	}

	// The admin kubeconfig may point at any workspace; the installer recomputes
	// every URL from the layout, so the mounted credential needs no pre-scoping.
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}

	cfg = bootstrap.Retarget(cfg, operatorCfg.Kcp.Server, operatorCfg.Kcp.ServerName)

	installer := bootstrap.New(cfg, bootstrap.Options{
		Layout:                layout,
		WorkspaceReadyTimeout: initWorkspaceTimeout,
	}, func(format string, args ...any) {
		log.Info().Msgf(format, args...)
	})

	return installer.Run(c.Context())
}

func init() {
	initCmd.Flags().DurationVar(&initWorkspaceTimeout, "workspace-ready-timeout", 2*time.Minute,
		"How long to wait for each created workspace to become Ready")
}
