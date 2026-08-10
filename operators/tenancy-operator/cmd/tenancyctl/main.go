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

// Command tenancyctl is the DEVELOPER CLI for the tenancy model.
//
// A separate binary from the operator on purpose: this runs on a laptop, holds a
// user's token, and has no business inside a server image. It shares the module
// so it can reuse pkg/identity and the API types, and nothing more.
//
// What it is allowed to be: a convenience relay for CLI ergonomics — a localhost
// callback and a kubeconfig — that holds no tenant-tier credentials and
// provisions nothing. Logging in gets you a token. Becoming a provisioned user is
// a separate, explicit call against the virtual workspace.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"
	"go.platform-mesh.io/tenancy-operator/internal/cli"
)

var (
	issuerURL   string
	clientID    string
	caFile      string
	scopes      []string
	cachePath   string
	kubeServer  string
	kubeCA      string
	cluster     string
	outputPath  string
	noBrowser   bool
	redirectURL string
	useExec     bool
	vwServer    string
	vwCA        string
	vwCluster   string
	provision   bool
	tenant      string
	project     string
)

func main() {
	root := &cobra.Command{
		Use:   "tenancyctl",
		Short: "developer CLI for the platform-mesh tenancy model",
		Long: `Developer CLI for the platform-mesh tenancy model.

Signs in against the platform's OIDC issuer with a PKCE public-client flow — no
client secret exists anywhere — and writes a kubeconfig for one workspace.

It provisions nothing. A new identity becomes a User by calling the tenancy
virtual workspace explicitly, which is deliberately not a side effect of logging
in: making authentication provision projects means a monitoring probe can create
them, and hides provisioning failures inside unrelated calls.

Dev tool. Not a supported client.`,
		SilenceUsage: true,
	}

	// The issuer settings must match kcp's and the virtual workspace's. A token
	// minted for another audience is rejected by both, and the resulting 401 says
	// nothing about which of the three is misconfigured.
	root.PersistentFlags().StringVar(&issuerURL, "oidc-issuer-url", envOr("TENANCY_OIDC_ISSUER_URL", ""), "OIDC issuer; MUST match kcp's")
	root.PersistentFlags().StringVar(&clientID, "oidc-client-id", envOr("TENANCY_OIDC_CLIENT_ID", "platform-mesh"), "OIDC public client ID")
	root.PersistentFlags().StringVar(&caFile, "oidc-ca-file", envOr("TENANCY_OIDC_CA_FILE", ""), "CA bundle for a private-CA issuer")
	root.PersistentFlags().StringSliceVar(&scopes, "scopes", []string{"openid", "profile", "email", "groups", "offline_access"}, "scopes to request")
	root.PersistentFlags().StringVar(&cachePath, "token-cache", "", "where the token is cached (default ~/.tenancy/token.json)")

	// Addressing the tenancy virtual workspace. Persistent because provisioning
	// and reading your own User are the same endpoint reached by three commands,
	// and a flag that exists on one of them is a trap.
	root.PersistentFlags().StringVar(&vwServer, "server", envOr("TENANCY_VW_SERVER", ""), "Tenancy virtual workspace URL")
	root.PersistentFlags().StringVar(&vwCA, "vw-ca-file", envOr("TENANCY_VW_CA_FILE", ""), "CA bundle for the virtual workspace's certificate")
	root.PersistentFlags().StringVar(&vwCluster, "vw-cluster", envOr("TENANCY_VW_CLUSTER", ""), "The /clusters/{x} segment to address (default \"*\")")

	// Addressing kcp itself. Persistent rather than local to `kubeconfig`, even
	// though that is the only command which DIALS kcp, because `login` is what
	// remembers the environment — and a value that can only be supplied on the
	// command that consumes it can never be refreshed by the command that caches
	// it. Merge only fills EMPTY fields, so a kcp address remembered against a
	// previous environment then survives every later login and silently outlives
	// the cluster it named. That is exactly how a kubeconfig ends up pointing at an
	// address nothing listens on, with `login` reporting success.
	//
	// NOT --server: that is the tenancy virtual workspace above, and one flag
	// meaning two endpoints depending on the subcommand is a trap.
	root.PersistentFlags().StringVar(&kubeServer, "kcp-server", envOr("TENANCY_KCP_SERVER", ""), "kcp front-proxy base URL")
	root.PersistentFlags().StringVar(&kubeCA, "certificate-authority", envOr("TENANCY_CA_FILE", ""), "CA bundle verifying kcp")

	root.AddCommand(loginCmd(), getTokenCmd(), kubeconfigCmd(), whoamiCmd(), usersCmd(),
		tenantsCmd(), projectsCmd(), membershipsCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loginCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "login",
		Short: "sign in against the OIDC issuer and cache the token",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config()
			if err != nil {
				return err
			}
			token, err := cli.Login(cmd.Context(), cfg, !noBrowser)
			if err != nil {
				return err
			}
			path, err := cli.CachePath(cachePath)
			if err != nil {
				return err
			}
			if err := cli.SaveToken(path, token); err != nil {
				return err
			}

			if _, err := fmt.Fprintf(os.Stderr, "Signed in. Token cached at %s\n", path); err != nil {
				return err
			}

			// Remember where this login was performed, so later commands need only
			// `tenancyctl projects` rather than the whole address again. Best-effort:
			// the token is what was hard to obtain and it is already saved, so a
			// failure to cache addressing must not fail the login.
			remembered, _ := cli.Context{
				Server:        vwServer,
				CAFile:        vwCA,
				Cluster:       vwCluster,
				KCPServer:     kubeServer,
				KCPCAFile:     kubeCA,
				OIDCIssuerURL: issuerURL,
				OIDCClientID:  clientID,
				OIDCCAFile:    caFile,
			}.Merge(cli.LoadContext(cli.ContextPath(path)))
			_ = cli.SaveContext(cli.ContextPath(path), remembered)

			// Provisioning stays an explicit call — it is simply made here, on your
			// behalf, rather than left as homework. The distinction the RFC draws is
			// that authenticating must not IMPLY provisioning: a monitoring probe or
			// a token refresh must not create projects. A person running `login` is
			// not that, and the call is still attributable and rate-limitable.
			if !provision {
				_, err := fmt.Fprintf(os.Stderr, "\nNot provisioned: --provision=false.\n"+
					"Run `tenancyctl users create` when you want a project.\n")
				return err
			}
			if vwServer == "" {
				_, err := fmt.Fprintf(os.Stderr, "\nYou are authenticated but not yet provisioned — that is a separate,\n"+
					"explicit call against the tenancy virtual workspace. Pass --server to make it\n"+
					"here, or run `tenancyctl users create --server=…` later.\n")
				return err
			}

			idToken, err := cli.IDToken(token)
			if err != nil {
				return err
			}
			user, err := cli.CreateUserSelf(cmd.Context(), cli.VWOptions{
				Server:  vwServer,
				Cluster: vwCluster,
				CAFile:  vwCA,
				Token:   idToken,
			})
			if err != nil {
				// The token IS cached and valid; only provisioning failed. Say so, so
				// nobody re-runs the browser flow to fix an unrelated problem.
				return fmt.Errorf("signed in, but provisioning failed (the token is cached; retry with `tenancyctl users create`): %w", err)
			}
			return cli.PrintUser(os.Stdout, user, cli.TokenGroups(idToken))
		},
	}
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "print the URL instead of opening a browser")
	c.Flags().BoolVar(&provision, "provision", true, "after signing in, create the caller's User (needs --server)")
	// Must match a redirectURI registered on the IdP client. The dev dex config
	// registers http://127.0.0.1:8000 (and localhost:8000) with no path, so that
	// is the default; ephemeral ports would be refused.
	c.Flags().StringVar(&redirectURL, "redirect-url", envOr("TENANCY_REDIRECT_URL", "http://127.0.0.1:8000"),
		"loopback callback; must exactly match one registered with the IdP")
	return c
}

// getTokenCmd implements the client-go credential plugin protocol, so a
// kubeconfig can refresh transparently instead of going stale.
func getTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get-token",
		Short: "print an ExecCredential (used by the kubeconfig, not by humans)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config()
			if err != nil {
				return err
			}
			path, err := cli.CachePath(cachePath)
			if err != nil {
				return err
			}
			token, err := cli.LoadToken(path)
			if err != nil {
				return err
			}

			refreshed, err := cli.Refresh(cmd.Context(), cfg, token)
			if err != nil {
				return err
			}
			// Persist a renewed token so the next invocation starts from it.
			if refreshed.AccessToken != token.AccessToken {
				if err := cli.SaveToken(path, refreshed); err != nil {
					return err
				}
			}

			idToken, err := cli.IDToken(refreshed)
			if err != nil {
				return err
			}
			return cli.PrintExecCredential(os.Stdout, idToken, refreshed.Expiry)
		},
	}
}

func kubeconfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "kubeconfig",
		Short: "write a kubeconfig for one workspace",
		Long: `Write a kubeconfig pointing at one project's workspace.

Name the target one of two ways:

  --project <uuid|name> [--tenant <uuid|name>]   resolved through the API
  --cluster <id>                                       if you already know it

There is NO default. A "default workspace" pointer turns a set-once field into a
request-scoping default and funnels every request into one workspace. Resolving
--project is a lookup you asked for, not a default chosen for you.

--tenant is optional but recommended: project display names are unique
nowhere, and an ambiguous one is refused rather than guessed.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target := cluster
			if target == "" && project != "" {
				opts, err := vwOptions()
				if err != nil {
					return err
				}

				var tenantUUID string
				if tenant != "" {
					tnt, err := cli.ResolveTenant(cmd.Context(), opts, tenant)
					if err != nil {
						return err
					}
					tenantUUID = tnt.Name
				}

				proj, err := cli.ResolveProject(cmd.Context(), opts, project, tenantUUID)
				if err != nil {
					return err
				}
				if proj.Status.ClusterID == "" {
					return fmt.Errorf("project %s has no workspace yet; wait for it to become Ready (`tenancyctl projects`)", proj.Name)
				}
				target = proj.Status.ClusterID
			}
			if target == "" {
				return fmt.Errorf("name a workspace: --project <uuid|name> (optionally with --tenant), or --cluster <id> if you know it")
			}

			// Same inheritance as the virtual-workspace commands: name kcp once and
			// it is remembered, rather than repeated on every kubeconfig.
			tokenPath, err := cli.CachePath(cachePath)
			if err != nil {
				return err
			}
			ctxPath := cli.ContextPath(tokenPath)
			resolved, _ := cli.Context{
				KCPServer:     kubeServer,
				KCPCAFile:     kubeCA,
				OIDCIssuerURL: issuerURL,
				OIDCClientID:  clientID,
				OIDCCAFile:    caFile,
			}.Merge(cli.LoadContext(ctxPath))
			kubeServer, kubeCA = resolved.KCPServer, resolved.KCPCAFile
			issuerURL, clientID, caFile = resolved.OIDCIssuerURL, resolved.OIDCClientID, resolved.OIDCCAFile

			if kubeServer == "" {
				return fmt.Errorf("no kcp server: pass --kcp-server (or set TENANCY_KCP_SERVER) once and it will be remembered")
			}
			if useExec && issuerURL == "" {
				// Writing it anyway produces a kubeconfig that looks fine and fails
				// the first time kubectl runs the plugin, with an error about a flag
				// the user never typed. Refusing here fails at the point of the
				// mistake instead.
				return fmt.Errorf("no OIDC issuer for the credential plugin: sign in with `tenancyctl login` " +
					"(which remembers it), pass --oidc-issuer-url, or use --exec=false to embed the current token instead")
			}

			var caData []byte
			if kubeCA != "" {
				var err error
				caData, err = os.ReadFile(kubeCA)
				if err != nil {
					return fmt.Errorf("reading the kcp CA from %s: %w", kubeCA, err)
				}
			}

			opts := cli.KubeconfigOptions{
				Server:      kubeServer,
				Cluster:     target,
				CAData:      caData,
				ContextName: "tenancy",
			}

			if useExec {
				self, err := os.Executable()
				if err != nil {
					return fmt.Errorf("locating this binary for the credential plugin: %w", err)
				}
				if isEphemeralBinary(self) {
					// `go run` builds into a temp directory and deletes it on exit, so
					// the path written here stops existing the moment this command
					// returns. The kubeconfig looks correct and fails on first use with
					// an ENOENT naming a path the user never chose — refuse at the point
					// of the mistake instead, for the same reason a missing issuer is
					// refused above.
					return fmt.Errorf("this binary is a temporary build at %s, so a kubeconfig naming it as a "+
						"credential plugin would break as soon as this command exits.\n"+
						"Build it first (`go build -o bin/tenancyctl ./cmd/tenancyctl`, or `task dev-kubeconfig`), "+
						"or pass --exec=false to embed the current token instead", self)
				}
				opts.ExecCommand = self
				opts.ExecArgs = []string{"get-token", "--oidc-issuer-url=" + issuerURL, "--oidc-client-id=" + clientID}
				if caFile != "" {
					opts.ExecArgs = append(opts.ExecArgs, "--oidc-ca-file="+caFile)
				}
			} else {
				path, err := cli.CachePath(cachePath)
				if err != nil {
					return err
				}
				token, err := cli.LoadToken(path)
				if err != nil {
					return err
				}
				idToken, err := cli.IDToken(token)
				if err != nil {
					return err
				}
				opts.Token = idToken
			}

			// Remembered only after the kubeconfig actually rendered, so a rejected
			// address is not cached as if it worked.
			defer func() {
				merged, _ := cli.Context{
					KCPServer:     kubeServer,
					KCPCAFile:     kubeCA,
					OIDCIssuerURL: issuerURL,
					OIDCClientID:  clientID,
					OIDCCAFile:    caFile,
				}.Merge(cli.LoadContext(ctxPath))
				_ = cli.SaveContext(ctxPath, merged)
			}()

			data, err := cli.Kubeconfig(opts)
			if err != nil {
				return err
			}
			return cli.WriteKubeconfig(outputPath, data)
		},
	}
	// --kcp-server and --certificate-authority are PERSISTENT flags on the root
	// command, so `login` can record the address it signed in against instead of
	// leaving a previous environment's cached and unreachable.
	c.Flags().StringVar(&cluster, "cluster", "", "logical cluster to point at, if you already know it")
	c.Flags().StringVar(&project, "project", "", "project to point at (UUID or display name)")
	c.Flags().StringVar(&tenant, "tenant", "", "tenant the project is in (UUID or display name)")
	c.Flags().StringVarP(&outputPath, "output", "o", "-", "where to write, or - for stdout")
	c.Flags().BoolVar(&useExec, "exec", true, "use this binary as a credential plugin so the token refreshes")
	return c
}

// whoamiCmd shows what the cached token says, without contacting anything.
// Useful precisely because the identity keys are not obvious: the User name is a
// digest, and rbacIdentity is a mirror of kcp's username convention.
func whoamiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "show the identity in the cached token, and the User the platform holds for it",
		Long: `Show who the cached token says you are.

With --server, also fetch your own User from the tenancy virtual workspace
(GET users/~). Without it the command stays entirely local and only decodes the
token, which is useful when the virtual workspace is not reachable.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := cli.CachePath(cachePath)
			if err != nil {
				return err
			}
			token, err := cli.LoadToken(path)
			if err != nil {
				return err
			}
			idToken, err := cli.IDToken(token)
			if err != nil {
				return err
			}

			if err := cli.PrintIdentity(os.Stdout, idToken, token.Expiry); err != nil {
				return err
			}
			opts, optErr := vwOptions()
			if optErr != nil {
				// Staying local is a valid outcome here, unlike for the listings:
				// the token alone answers "who am I".
				_, err := fmt.Fprintf(os.Stdout, "\nNot checked against the platform: %v\n", optErr)
				return err
			}
			opts.Token = idToken

			user, err := cli.UserSelf(cmd.Context(), opts)
			if err != nil {
				var notProvisioned *cli.NotProvisionedError
				if errors.As(err, &notProvisioned) {
					// Not a failure: it is the state every identity starts in, and the
					// server's own message names the call that changes it.
					_, err := fmt.Fprintf(os.Stdout, "\nNo User yet.\n  %s\n", notProvisioned)
					return err
				}
				return err
			}

			return cli.PrintUser(os.Stdout, user, cli.TokenGroups(idToken))
		},
	}
	return cmd
}

// tenantsCmd lists the Tenants the caller belongs to.
func tenantsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tenants",
		Aliases: []string{"tenant"},
		Short:   "list the tenants you belong to",
		Long: `List the Tenants you belong to.

Filtered by your memberships, not by permission: a Tenant you are not a
member of is invisible rather than forbidden, so this never reveals that one
exists.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := vwOptions()
			if err != nil {
				return err
			}
			tenants, err := cli.ListTenants(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return cli.PrintTenants(os.Stdout, tenants)
		},
	}
	cmd.AddCommand(tenantsCreateCmd())
	return cmd
}

// tenantsCreateCmd creates a Tenant owned by the caller.
func tenantsCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <display-name>",
		Short: "create a tenant you will own",
		Long: `Create a Tenant.

The argument is a DISPLAY NAME, not an identifier: the Tenant's real name is
a server-assigned UUID, because that name is also its workspace path and letting a
client choose it would let them choose a path. Display names are not unique.

You become its first admin, which the controller turns into a Membership and then
into real role bindings — so the Tenant is briefly not usable while that
settles. Watch it with "tenancyctl tenants".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := vwOptions()
			if err != nil {
				return err
			}
			tenant, err := cli.CreateTenant(cmd.Context(), opts, args[0])
			if err != nil {
				return err
			}
			return cli.PrintTenants(os.Stdout, []pmtenancyv1alpha1.Tenant{*tenant})
		},
	}
}

// projectsCmd lists the Projects the caller may work in.
func projectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project"},
		Short:   "list the projects you can work in",
		Long: `List the Projects you can work in, across every Tenant you belong to.

A Project is where work happens; the kcp workspace behind it is never named. The
CLUSTER column is what "tenancyctl kubeconfig --cluster" takes.

--tenant narrows the list to one Tenant, by UUID or display name.
Display names are not unique, so an ambiguous one is an error listing the
candidates rather than a silent guess.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := vwOptions()
			if err != nil {
				return err
			}

			projects, err := cli.ListProjects(cmd.Context(), opts)
			if err != nil {
				return err
			}

			if tenant != "" {
				tnt, err := cli.ResolveTenant(cmd.Context(), opts, tenant)
				if err != nil {
					return err
				}
				// Filtered here rather than server-side: the list is already
				// bounded by the caller's memberships, so this is a display
				// choice, not a security one.
				kept := projects[:0]
				for _, a := range projects {
					if a.Labels[pmtenancyv1alpha1.LabelTenant] == tnt.Name {
						kept = append(kept, a)
					}
				}
				projects = kept
			}

			return cli.PrintProjects(os.Stdout, projects)
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "only projects in this tenant (UUID or display name)")
	cmd.AddCommand(projectsCreateCmd())
	return cmd
}

// projectsCreateCmd creates a Project inside one Tenant.
func projectsCreateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "create <display-name> --tenant <uuid|name>",
		Short: "create a project in a tenant",
		Long: `Create a Project: a place to work, inside a Tenant you belong to.

The argument is a DISPLAY NAME; the Project's real name is a server-assigned UUID,
for the same reason Tenants work that way.

--tenant is required. There is no default: this model has no "current"
tenant, and picking one for you is how a client ends up creating things in
the wrong tenant.

Whether you may create depends on the Tenant's own policy
(spec.projectCreation), not on a platform-wide rule.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tenant == "" {
				return fmt.Errorf("--tenant is required: name the tenant to create this project in " +
					"(`tenancyctl tenants` lists the ones you belong to)")
			}
			opts, err := vwOptions()
			if err != nil {
				return err
			}
			tnt, err := cli.ResolveTenant(cmd.Context(), opts, tenant)
			if err != nil {
				return err
			}
			proj, err := cli.CreateProject(cmd.Context(), opts, args[0], tnt.Name)
			if err != nil {
				return err
			}
			return cli.PrintProjects(os.Stdout, []pmtenancyv1alpha1.Project{*proj})
		},
	}
	c.Flags().StringVar(&tenant, "tenant", "", "the tenant to create the project in (UUID or display name)")
	return c
}

// membershipsCmd lists and manages who belongs to a Tenant.
func membershipsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "memberships",
		Aliases: []string{"membership", "members"},
		Short:   "who belongs to your tenants, and as what",
		Long: `List the Memberships of every Tenant you belong to.

READING IS OPEN TO EVERY ROLE, including viewer: knowing who else is in your
Tenant is part of being in it, and a member who cannot read the roster
cannot tell whether an admin still exists.

WRITING IS ADMIN-ONLY. "add", "set-role" and "remove" all require the admin role
in that Tenant — except removing your own grant, which is leaving and is
always yours to do.

This is the ONLY way to manage access. Memberships live in the Tenant's kcp
workspace, and no tenant identity has any permission there, so there is no
kubectl path to fall back on.

--tenant narrows the list to one Tenant, by UUID or display name.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := vwOptions()
			if err != nil {
				return err
			}

			memberships, err := cli.ListMemberships(cmd.Context(), opts)
			if err != nil {
				return err
			}

			if tenant != "" {
				tnt, err := cli.ResolveTenant(cmd.Context(), opts, tenant)
				if err != nil {
					return err
				}
				kept := memberships[:0]
				for _, m := range memberships {
					if m.Labels[pmtenancyv1alpha1.LabelTenant] == tnt.Name {
						kept = append(kept, m)
					}
				}
				memberships = kept
			}

			return cli.PrintMemberships(os.Stdout, memberships)
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "only memberships in this tenant (UUID or display name)")
	cmd.AddCommand(membershipsAddCmd(), membershipsSetRoleCmd(), membershipsRemoveCmd())
	return cmd
}

// membershipsAddCmd grants a user access.
func membershipsAddCmd() *cobra.Command {
	var role, project, group string
	c := &cobra.Command{
		Use:   "add <user>|--group <group> --tenant <uuid|name>",
		Short: "grant a user or a group access to a tenant or one project (admin only)",
		Long: `Grant access. Requires the admin role in that Tenant.

<user> is a User NAME — the sha256(issuer + "\n" + sub) digest that "tenancyctl
whoami" prints — NOT an email address. There is no lookup by email: this API is
never a directory of the platform's people, so you have to be told the name by
the person you are granting to.

They must have signed in at least once. A User exists only after its owner
provisions it, so granting to someone who never has is refused rather than left
pending — an invitation is a different thing and this is deliberately not one.

--group grants to EVERYONE whose token carries that group, including people who
have never signed in — which is the one thing a user grant cannot do. Give the
group as your identity provider emits it, without the prefix kcp adds.

Nothing checks that the group exists, because nothing can: the platform holds no
object for a group and cannot enumerate one. A typo is accepted and grants
nobody. Revoking is at your identity provider for one person, or here for all of
them.

--project grants access to ONE project instead of the whole Tenant. Without
it the grant is tenant-scope, which reaches every project in the Tenant,
including ones created later.

--tenant is required only for a tenant-wide grant. With --project it is
optional, because a project already knows which tenant it is in; pass it
anyway to disambiguate a project DISPLAY NAME that two of your tenants share.
If you pass both and they disagree, the command refuses rather than picking.`,
		// A user is positional and a group is a flag, so exactly one of the two
		// forms is expressible: `add <user>` or `add --group <g>`. Both, or
		// neither, is refused below rather than resolved in either direction.
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subject := cli.Subject{Group: group}
			if len(args) == 1 {
				subject.User = args[0]
			}
			switch {
			case subject.User == "" && subject.Group == "":
				return fmt.Errorf("name a subject: a <user> as the argument, or --group for everyone in a group")
			case subject.User != "" && subject.Group != "":
				return fmt.Errorf("a grant has one subject: pass a <user> or --group, not both")
			}

			opts, err := vwOptions()
			if err != nil {
				return err
			}
			// --tenant is OPTIONAL when --project names one, because a Project
			// already knows which Tenant it belongs to. Making the caller restate it
			// would be asking for something the server can derive, and giving them a
			// second chance to get it wrong.
			tenantUUID := ""
			if tenant != "" {
				tnt, err := cli.ResolveTenant(cmd.Context(), opts, tenant)
				if err != nil {
					return err
				}
				tenantUUID = tnt.Name
			}

			scope := pmtenancyv1alpha1.MembershipScopeTenant
			projectUUID := ""
			if project != "" {
				scope = pmtenancyv1alpha1.MembershipScopeProject
				// Passing tenantUUID here NARROWS the search when it was given, and
				// searches every Tenant the caller belongs to when it was not. A
				// display name that is ambiguous across Tenants comes back as an error
				// listing the candidates, never as a guess.
				proj, err := cli.ResolveProject(cmd.Context(), opts, project, tenantUUID)
				if err != nil {
					return err
				}
				projectUUID = proj.Name

				owner := proj.Labels[pmtenancyv1alpha1.LabelTenant]
				switch {
				case tenantUUID == "":
					tenantUUID = owner
				case tenantUUID != owner:
					// Both given and disagreeing. Refused rather than resolved in
					// either direction: one of the two is a mistake, and picking a
					// winner grants access somewhere the caller did not mean.
					return fmt.Errorf("project %s belongs to tenant %s, not %s",
						proj.Name, owner, tenantUUID)
				}
			}

			if tenantUUID == "" {
				return fmt.Errorf("--tenant is required for a tenant-wide grant: name the tenant to grant access in " +
					"(`tenancyctl tenants` lists the ones you belong to), or pass --project to grant access to one project")
			}

			m, err := cli.CreateMembership(cmd.Context(), opts, tenantUUID, subject, scope, projectUUID, role)
			if err != nil {
				return err
			}
			return cli.PrintMemberships(os.Stdout, []pmtenancyv1alpha1.Membership{*m})
		},
	}
	c.Flags().StringVar(&tenant, "tenant", "", "the tenant to grant access in (UUID or display name)")
	c.Flags().StringVar(&role, "role", pmtenancyv1alpha1.MembershipRoleMember, "admin, member or viewer")
	c.Flags().StringVar(&project, "project", "", "grant access to one project only (UUID or display name)")
	c.Flags().StringVar(&group, "group", "", "grant to everyone in this identity-provider group instead of one user")
	return c
}

// membershipsSetRoleCmd changes an existing grant's role.
func membershipsSetRoleCmd() *cobra.Command {
	var role string
	c := &cobra.Command{
		Use:   "set-role <name|user> --role <admin|member|viewer>",
		Short: "change what an existing grant allows (admin only)",
		Long: `Change a Membership's role. Requires the admin role in that Tenant.

The role is the ONLY editable field. Who the grant is for, its scope and its
project identify it — the Membership's name is derived from them — so changing
those means a different grant. Remove this one and add the one you want.

There is no self-service arm here, unlike "remove": leaving is yours to decide,
but editing your own grant is self-promotion with extra steps.

Demoting the last admin is refused for the same reason removing them is: an
Tenant with no admin can never be administered again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if role == "" {
				return fmt.Errorf("--role is required: admin, member or viewer")
			}
			opts, err := vwOptions()
			if err != nil {
				return err
			}

			tenantUUID := ""
			if tenant != "" {
				tnt, err := cli.ResolveTenant(cmd.Context(), opts, tenant)
				if err != nil {
					return err
				}
				tenantUUID = tnt.Name
			}

			current, err := cli.ResolveMembership(cmd.Context(), opts, args[0], tenantUUID)
			if err != nil {
				return err
			}
			m, err := cli.SetMembershipRole(cmd.Context(), opts, current.Name, role)
			if err != nil {
				return err
			}
			return cli.PrintMemberships(os.Stdout, []pmtenancyv1alpha1.Membership{*m})
		},
	}
	c.Flags().StringVar(&tenant, "tenant", "", "disambiguate by tenant (UUID or display name)")
	c.Flags().StringVar(&role, "role", "", "admin, member or viewer")
	return c
}

// membershipsRemoveCmd revokes a grant.
func membershipsRemoveCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "remove <name|user>",
		Aliases: []string{"revoke", "delete"},
		Short:   "revoke a grant, or leave a tenant",
		Long: `Revoke a Membership.

An admin may revoke anyone's. Anyone may revoke their OWN, which is what leaving
a Tenant is — there is no separate "leave" command, because a verb that is
really a delete is better as a delete.

Removing the last admin of a Tenant is refused: nobody could grant a new
one afterwards, since there is no way into the Tenant's workspace except
through this API and this API requires an admin.

The grant is not gone when this returns. A finalizer removes the role bindings
first, and only then does the object disappear — which is why access can outlive
the command by a moment, and why it is reported rather than hidden.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := vwOptions()
			if err != nil {
				return err
			}

			tenantUUID := ""
			if tenant != "" {
				tnt, err := cli.ResolveTenant(cmd.Context(), opts, tenant)
				if err != nil {
					return err
				}
				tenantUUID = tnt.Name
			}

			m, err := cli.ResolveMembership(cmd.Context(), opts, args[0], tenantUUID)
			if err != nil {
				return err
			}
			if err := cli.DeleteMembership(cmd.Context(), opts, m.Name); err != nil {
				return err
			}
			_, err = fmt.Fprintf(os.Stdout,
				"Revoking %s (%s, %s in %s).\nThe role bindings are removed first; the grant disappears when that finishes.\n",
				m.Name, m.Spec.Scope, m.Spec.Role, m.Labels[pmtenancyv1alpha1.LabelTenant])
			return err
		},
	}
	c.Flags().StringVar(&tenant, "tenant", "", "disambiguate by tenant (UUID or display name)")
	return c
}

// vwOptions gathers what every virtual-workspace call needs.
//
// Flags win, then the environment remembered at login. Naming the virtual
// workspace once, when signing in, is enough — repeating it on every read is the
// kind of friction that gets worked around with a shell alias.
func vwOptions() (cli.VWOptions, error) {
	path, err := cli.CachePath(cachePath)
	if err != nil {
		return cli.VWOptions{}, err
	}
	idToken, err := cachedIDToken()
	if err != nil {
		return cli.VWOptions{}, err
	}

	resolved, _ := cli.Context{
		Server:  vwServer,
		Cluster: vwCluster,
		CAFile:  vwCA,
	}.Merge(cli.LoadContext(cli.ContextPath(path)))

	opts := cli.VWOptions{
		Server:  resolved.Server,
		Cluster: resolved.Cluster,
		CAFile:  resolved.CAFile,
		Token:   idToken,
	}
	if opts.Server == "" {
		return cli.VWOptions{}, fmt.Errorf(
			"no tenancy virtual workspace to read from: pass --server, set TENANCY_VW_SERVER, " +
				"or sign in with `tenancyctl login --server=...` once and it will be remembered")
	}
	return opts, nil
}

// usersCmd groups the calls that act on the caller's own User.
func usersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "the caller's own User",
	}
	cmd.AddCommand(usersCreateCmd())
	return cmd
}

// usersCreateCmd is the self-provision call.
func usersCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create",
		Short: "provision the caller's own User",
		Long: `Create your own User against the tenancy virtual workspace.

This is the one call a new identity makes. The request body is empty on purpose:
the server ignores any submitted spec and fills every field from the verified
token, which is also what makes the call idempotent — running it twice returns
the same object rather than minting a second project.

Provisioning is deliberately NOT a side effect of authenticating or of reading:
an implicit trigger would make GET a write, would hide provisioning failures
inside unrelated calls, and would leave nothing to rate-limit.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := vwOptions()
			if err != nil {
				return err
			}

			user, err := cli.CreateUserSelf(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return cli.PrintUser(os.Stdout, user, cli.TokenGroups(opts.Token))
		},
	}
}

// cachedIDToken loads the id_token every virtual-workspace call authenticates with.
func cachedIDToken() (string, error) {
	path, err := cli.CachePath(cachePath)
	if err != nil {
		return "", err
	}
	token, err := cli.LoadToken(path)
	if err != nil {
		return "", err
	}
	return cli.IDToken(token)
}

func config() (cli.Config, error) {
	if issuerURL == "" {
		return cli.Config{}, fmt.Errorf("--oidc-issuer-url is required (or set TENANCY_OIDC_ISSUER_URL); it must match what kcp is configured with")
	}
	return cli.Config{
		IssuerURL:   issuerURL,
		ClientID:    clientID,
		CAFile:      caFile,
		Scopes:      scopes,
		RedirectURL: redirectURL,
	}, nil
}

// isEphemeralBinary reports whether path looks like a `go run` build — a binary
// under the temp directory in a `go-build*` cache directory.
//
// Matched on the path rather than on anything authoritative because there is
// nothing authoritative to ask: a process cannot tell it was started by `go run`.
// A false positive costs a real binary in /tmp its exec plugin (with an error
// naming --exec=false); a false negative is only the pre-existing behaviour.
func isEphemeralBinary(path string) bool {
	dir := filepath.Dir(path)
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		tmp = os.TempDir()
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	if !strings.HasPrefix(resolved+string(filepath.Separator), tmp+string(filepath.Separator)) {
		return false
	}
	// go-build<digits>/b001/exe/<name> — the cache directory is the part that says
	// "the toolchain made this", so look for it anywhere below the temp root.
	for _, part := range strings.Split(resolved, string(filepath.Separator)) {
		if strings.HasPrefix(part, "go-build") {
			return true
		}
	}
	return false
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
