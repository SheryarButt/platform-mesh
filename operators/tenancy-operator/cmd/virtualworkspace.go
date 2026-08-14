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
	"fmt"
	"sync"

	"github.com/spf13/cobra"

	"go.platform-mesh.io/tenancy-operator/internal/bootstrap"
	"go.platform-mesh.io/tenancy-operator/internal/virtualworkspace"

	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var virtualWorkspaceCmd = &cobra.Command{
	Use:   "virtual-workspace",
	Short: "serve the tenancy virtual workspace",
	Long: `Serve the tenancy APIs at <prefix>/clusters/{cluster}/apis/tenancy.platform-mesh.io/...

A global singleton, not one instance per workspace or per APIExport: "which
tenants am I in" is a cross-cutting projection that no single workspace can
answer.

It serves ` + "`users`" + `, ` + "`tenants`" + ` and ` + "`projects`" + `. Creating a User is what turns
an authenticated token into a provisioned project: it triggers the controller,
which creates the personal Tenant, its workspace, and the membership index.

memberships are not served yet — they need a membership-filtered wildcard
list/watch across shards, which needs a spike first.

Runs as its own Deployment: stateless, horizontally scalable, one watch per
caller — the opposite shape to the leader-elected controller.`,
	RunE: func(c *cobra.Command, _ []string) error { return runVirtualWorkspace(c) },
}

func runVirtualWorkspace(c *cobra.Command) error {
	if err := operatorCfg.Validate(); err != nil {
		return err
	}
	layout, err := operatorCfg.Layout()
	if err != nil {
		return err
	}
	resolver, err := operatorCfg.IdentityResolver()
	if err != nil {
		return err
	}

	// The only writer into the tenant tier, so this client is the one whose blast
	// radius matters. Scoped to the directory workspace, computed from the same
	// --paths-* the installer and the controller use.
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	cfg = bootstrap.Retarget(cfg, operatorCfg.Kcp.Server, operatorCfg.Kcp.ServerName)
	directoryCfg := bootstrap.WorkspaceConfig(cfg, layout.Directory)

	directoryClient, err := ctrlruntimeclient.New(directoryCfg, ctrlruntimeclient.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("building a client for the directory workspace %s: %w", layout.Directory, err)
	}

	// One client per Tenant workspace, built from the SAME credential as the
	// directory client and simply pointed elsewhere. Cached because a listing
	// touches every Tenant the caller belongs to, and rebuilding a client
	// per request would mean a new TLS handshake per tenant per call.
	//
	// Which cluster is never taken from the request — ProjectStorage resolves the
	// caller's membership index first and only asks for clusters that answer.
	var (
		clusterMu      sync.Mutex
		clusterClients = map[string]ctrlruntimeclient.Client{}
	)
	clusterClient := func(clusterID string) (ctrlruntimeclient.Client, error) {
		clusterMu.Lock()
		defer clusterMu.Unlock()
		if c, ok := clusterClients[clusterID]; ok {
			return c, nil
		}
		c, err := ctrlruntimeclient.New(bootstrap.WorkspaceConfig(cfg, clusterID), ctrlruntimeclient.Options{Scheme: scheme})
		if err != nil {
			return nil, fmt.Errorf("building a client for cluster %s: %w", clusterID, err)
		}
		clusterClients[clusterID] = c
		return c, nil
	}

	// Resolved here rather than inside the VW so an unknown strategy fails at
	// boot, next to every other configuration error, instead of on the first
	// Tenant a tenant tries to create.
	strategy, err := operatorCfg.NamingStrategy()
	if err != nil {
		return err
	}

	vw, err := virtualworkspace.New(virtualworkspace.Options{
		Prefix:          layout.VirtualWorkspacePrefix,
		DirectoryClient: directoryClient,
		ClusterClient:   clusterClient,
		Resolver:        resolver,
		Naming:          strategy,
	})
	if err != nil {
		return err
	}

	log.Info().
		Str("prefix", layout.VirtualWorkspacePrefix).
		Str("directory", layout.Directory).
		Str("bindAddress", operatorCfg.VirtualWorkspace.BindAddress).
		Msg("serving the tenancy virtual workspace")

	return virtualworkspace.Serve(c.Context(), vw, virtualworkspace.ServeOptions{
		BindAddress: operatorCfg.VirtualWorkspace.BindAddress,
		TLSCertFile: operatorCfg.VirtualWorkspace.TLSCertFile,
		TLSKeyFile:  operatorCfg.VirtualWorkspace.TLSKeyFile,
		OIDC: virtualworkspace.OIDCOptions{
			IssuerURL:      operatorCfg.OIDC.IssuerURL,
			ClientID:       operatorCfg.OIDC.ClientID,
			CAFile:         operatorCfg.OIDC.CAFile,
			UsernameClaim:  operatorCfg.OIDC.UsernameClaim,
			UsernamePrefix: operatorCfg.OIDC.UsernamePrefix,
			GroupsClaim:    operatorCfg.OIDC.GroupsClaim,
			GroupsPrefix:   operatorCfg.OIDC.GroupsPrefix,
		},
	})
}
