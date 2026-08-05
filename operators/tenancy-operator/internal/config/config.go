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

package config

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"go.platform-mesh.io/tenancy-operator/pkg/identity"
	"go.platform-mesh.io/tenancy-operator/pkg/naming"
	"go.platform-mesh.io/tenancy-operator/pkg/paths"
)

// PathsConfig exposes the workspace layout as flags. Defaults are derived from
// Root by pkg/paths, so a sub-tree install only sets --paths-root.
type PathsConfig struct {
	Root                   string
	TenantFleetRoot        string
	Directory              string
	Exports                string
	Providers              string
	VirtualWorkspacePrefix string
}

// KcpConfig names the APIExportEndpointSlices this operator reads the fleet
// through. There is one per export because a controller cannot watch across
// logical clusters by wishing: kcp gives it exactly one wildcard endpoint, the
// APIExport's virtual workspace, spanning every workspace that binds that export.
//
// The export split therefore shows up here as more than one informer. That is
// the concrete cost of the split, and it is small.
type KcpConfig struct {
	// Server overrides the API server URL from the kubeconfig.
	//
	// In-cluster, kcp's published hostnames are often unroutable: they resolve
	// through a gateway whose advertised address is not a real in-cluster route,
	// so a pod can resolve the name and still fail to connect. Pointing at the
	// front-proxy Service instead is a direct, always-routable path.
	//
	// The path is ignored — the workspace segment is always recomputed from the
	// layout, so this sets host and port only.
	Server string

	// ServerName is the name TLS is verified against when Server dials an address
	// the certificate does not carry. Set it to the hostname kcp's certificate was
	// issued for; without it, addressing the Service by DNS fails verification.
	ServerName string

	// PlatformEndpointSlice is the `tenancy-platform` export — User,
	// Tenant and UserMembershipIndex in the directory workspace. A
	// single-workspace wildcard rather than a fleet-wide one, but going through
	// the export keeps the controller's access uniform: no privileged side
	// channel for the global tier.
	PlatformEndpointSlice string

	// ProvisionerEndpointSlice is the `tenancy-provisioner` export — no schemas,
	// one claim (tenancy.kcp.io/workspaces), bound in the fleet root and in every
	// Tenant. A Workspace object lives in its PARENT's logical cluster, so
	// those are exactly the two tiers where workspaces get created.
	ProvisionerEndpointSlice string

	// TenancyEndpointSlice is the `tenancy` export — every Tenant's
	// Memberships, through one fleet-wide informer.
	TenancyEndpointSlice string

	// AccessEndpointSlice is the `tenancy-access` export — no schemas, claims
	// over namespaces, serviceaccounts and RBAC inside a child workspace. What
	// lets the controller write a member's binding INSIDE a workspace without
	// holding cluster-admin for the whole shard.
	AccessEndpointSlice string
}

// OIDCConfig mirrors kcp's own authenticator settings. It is read twice from ONE
// configuration — here and on kcp — and drift between them is the worst failure
// mode in this operator.
//
// Two things depend on it, and they need different halves:
//
//	the operator  derives User.spec.rbacIdentity from UsernameClaim/UsernamePrefix.
//	              Wrong here and every ClusterRoleBinding names a subject that
//	              never authenticates.
//	the VW        verifies the caller's token itself, which needs the issuer,
//	              audience and CA as well — it resolves a caller to a User before
//	              kcp is ever involved.
type OIDCConfig struct {
	UsernameClaim  string
	UsernamePrefix string

	// IssuerURL is the single trusted issuer. Federation happens in the broker,
	// outside the platform, so there is exactly one.
	IssuerURL string

	// ClientID is the audience tokens must carry.
	ClientID string

	// CAFile is needed for a private-CA or self-signed issuer, which is the
	// normal case for a dev broker.
	CAFile string
}

// VirtualWorkspaceConfig configures the tenancy VW server itself.
type VirtualWorkspaceConfig struct {
	// BindAddress is where the VW serves.
	BindAddress string

	// TLSCertFile/TLSKeyFile serve the VW's own certificate. The front-proxy
	// routes to it, so it terminates its own TLS.
	TLSCertFile string
	TLSKeyFile  string
}

// TenancyConfig holds the model's policy knobs. Quotas are soft caps: a platform
// admin overrides them per User or per Tenant on the object itself.
type TenancyConfig struct {
	// PersonalTenantsEnabled controls whether every User gets a personal
	// Tenant at bootstrap. Disabled means users must be invited into an
	// existing Tenant before they can create anything.
	PersonalTenantsEnabled bool

	// PersonalTenantDisplayNameFormat is a fmt template taking the user's display
	// name (or email, when there is none).
	PersonalTenantDisplayNameFormat string

	// SeedProjectDisplayName is the display name given to the first child
	// Workspace seeded inside a personal Tenant. Not a format: the User's
	// name is already on the Tenant above it, so repeating it here would
	// read as "dex's dex".
	SeedProjectDisplayName string

	// TenantQuota is the platform default cap on Tenants per User. Personal
	// Tenants do not count against it.
	TenantQuota int32

	// ProjectQuota is the platform default cap on child Workspaces per
	// Tenant.
	ProjectQuota int32

	// TenantWorkspaceType is the WorkspaceType Tenant workspaces are
	// created with.
	TenantWorkspaceType string

	// ProjectWorkspaceType is the WorkspaceType child Workspaces are created
	// with. It deliberately does not extend root:universal, so kcp does not
	// create the `default` namespace and the controller must.
	ProjectWorkspaceType string

	// NamingStrategy picks how server-assigned Tenant and Project names are
	// minted. See pkg/naming; the value is a registered strategy name.
	//
	// Applies only to names minted after it is set. Existing objects keep theirs,
	// because a name is also a workspace path and rewriting it would move the
	// workspace — so switching this leaves a cluster with a mix, by design.
	NamingStrategy string
}

// StepConfig gates one step of a reconciler chain.
type StepConfig struct {
	Enabled bool
}

// ReconcilersConfig gates the steps of the bootstrap state machine individually,
// so a step can be disabled while its dependency lands.
type ReconcilersConfig struct {
	// SeedTenant creates the personal Tenant for a new User, once.
	SeedTenant StepConfig

	// SeedWorkspace creates the first child Workspace inside it, once.
	SeedProject StepConfig

	// TenantWorkspace creates the Tenant's kcp workspace under the fleet root.
	TenantWorkspace StepConfig

	// OwnerMembership gives the Tenant's first admin their tenant-scope
	// Membership.
	OwnerMembership StepConfig

	// ProjectWorkspace materializes the kcp Workspace behind a Project.
	ProjectWorkspace StepConfig

	// MembershipRBAC projects a Membership into the role binding that enforces it.
	MembershipRBAC StepConfig

	// Index reconciles the owning User's UserMembershipIndex.
	Index StepConfig
}

// ControllerConfig gates one controller.
type ControllerConfig struct {
	Enabled bool
}

// ControllersConfig gates the controllers this operator runs.
type ControllersConfig struct {
	User       ControllerConfig
	Tenant     ControllerConfig
	Workspace  ControllerConfig
	Membership ControllerConfig
	Project    ControllerConfig
}

// OperatorConfig is the whole configuration surface.
type OperatorConfig struct {
	Paths            PathsConfig
	Kcp              KcpConfig
	OIDC             OIDCConfig
	VirtualWorkspace VirtualWorkspaceConfig
	Tenancy          TenancyConfig
	Reconcilers      ReconcilersConfig
	Controllers      ControllersConfig
}

// NewOperatorConfig returns the defaults: the RFC's default layout, personal
// tenants on, and the quotas and grace period it specifies.
func NewOperatorConfig() OperatorConfig {
	return OperatorConfig{
		Paths: PathsConfig{
			Root: paths.DefaultRoot,
		},
		Kcp: KcpConfig{
			PlatformEndpointSlice:    "tenancy-platform",
			ProvisionerEndpointSlice: "tenancy-provisioner",
			TenancyEndpointSlice:     "tenancy",
			AccessEndpointSlice:      "tenancy-access",
		},
		OIDC: OIDCConfig{
			UsernameClaim:  string(identity.ClaimEmail),
			UsernamePrefix: "pm:",
		},
		VirtualWorkspace: VirtualWorkspaceConfig{
			BindAddress: ":6443",
		},
		Tenancy: TenancyConfig{
			PersonalTenantsEnabled:          true,
			PersonalTenantDisplayNameFormat: "%s's personal",
			SeedProjectDisplayName:          "default",
			TenantQuota:                     10,
			ProjectQuota:                    50,
			TenantWorkspaceType:             "tenant",
			ProjectWorkspaceType:            "project",
			NamingStrategy:                  naming.StrategyUUID,
		},
		Reconcilers: ReconcilersConfig{
			SeedTenant:       StepConfig{Enabled: true},
			SeedProject:      StepConfig{Enabled: true},
			TenantWorkspace:  StepConfig{Enabled: true},
			OwnerMembership:  StepConfig{Enabled: true},
			MembershipRBAC:   StepConfig{Enabled: true},
			ProjectWorkspace: StepConfig{Enabled: true},
			Index:            StepConfig{Enabled: true},
		},
		Controllers: ControllersConfig{
			User:       ControllerConfig{Enabled: true},
			Tenant:     ControllerConfig{Enabled: true},
			Workspace:  ControllerConfig{Enabled: true},
			Membership: ControllerConfig{Enabled: true},
			Project:    ControllerConfig{Enabled: true},
		},
	}
}

// Layout resolves the configured roots into the shared workspace layout.
func (c *OperatorConfig) Layout() (paths.Layout, error) {
	return paths.New(paths.Options{
		Root:                   c.Paths.Root,
		TenantFleetRoot:        c.Paths.TenantFleetRoot,
		Directory:              c.Paths.Directory,
		Exports:                c.Paths.Exports,
		Providers:              c.Paths.Providers,
		VirtualWorkspacePrefix: c.Paths.VirtualWorkspacePrefix,
	})
}

// IdentityResolver builds the rbacIdentity mirror from the OIDC settings.
func (c *OperatorConfig) IdentityResolver() (*identity.Resolver, error) {
	return identity.NewResolver(identity.Config{
		UsernameClaim:  identity.UsernameClaim(c.OIDC.UsernameClaim),
		UsernamePrefix: c.OIDC.UsernamePrefix,
	})
}

// Validate checks the configuration is self-consistent before anything starts, so
// a bad root or claim fails at boot rather than on the first reconcile.
func (c *OperatorConfig) Validate() error {
	if _, err := c.Layout(); err != nil {
		return fmt.Errorf("workspace layout: %w", err)
	}
	if _, err := c.IdentityResolver(); err != nil {
		return fmt.Errorf("oidc: %w", err)
	}
	if c.Kcp.PlatformEndpointSlice == "" {
		return fmt.Errorf("kcp: platform APIExportEndpointSlice name must be set")
	}
	if c.Tenancy.TenantQuota < 0 || c.Tenancy.ProjectQuota < 0 {
		return fmt.Errorf("tenancy: quotas must not be negative")
	}
	// Resolved at boot, not on the first create: an unknown strategy is a typo in
	// a flag, and finding out about it when a tenant tries to make a Tenant
	// is finding out in the worst place.
	if _, err := c.NamingStrategy(); err != nil {
		return fmt.Errorf("tenancy: %w", err)
	}
	return nil
}

// NamingStrategy resolves the configured strategy for server-assigned names.
func (c *OperatorConfig) NamingStrategy() (naming.Strategy, error) {
	return naming.Get(c.Tenancy.NamingStrategy)
}

// AddFlags registers every knob above.
func (c *OperatorConfig) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.Paths.Root, "paths-root", c.Paths.Root, "Install root workspace; every other path defaults below it")
	fs.StringVar(&c.Paths.TenantFleetRoot, "paths-tenant-fleet-root", c.Paths.TenantFleetRoot, "Workspace whose children are Tenants (default <root>:tenants)")
	fs.StringVar(&c.Paths.Directory, "paths-directory", c.Paths.Directory, "Workspace holding User/Tenant/UserMembershipIndex (default <root>:system:directory)")
	fs.StringVar(&c.Paths.Exports, "paths-exports", c.Paths.Exports, "Workspace defining the tenancy APIExports (default <root>:system:controllers)")
	fs.StringVar(&c.Paths.Providers, "paths-providers", c.Paths.Providers, "Workspace holding Provider objects (default <root>:system:providers)")
	fs.StringVar(&c.Paths.VirtualWorkspacePrefix, "paths-virtual-workspace-prefix", c.Paths.VirtualWorkspacePrefix, "Mount point of the tenancy virtual workspace singleton")

	fs.StringVar(&c.Kcp.Server, "kcp-server", c.Kcp.Server, "Override the kcp API server URL from the kubeconfig (host and port; the workspace path is always recomputed)")
	fs.StringVar(&c.Kcp.ServerName, "kcp-server-name", c.Kcp.ServerName, "Hostname to verify kcp's certificate against when --kcp-server dials a different address")
	fs.StringVar(&c.Kcp.PlatformEndpointSlice, "kcp-platform-endpoint-slice", c.Kcp.PlatformEndpointSlice, "APIExportEndpointSlice for the tenancy-platform export")
	fs.StringVar(&c.Kcp.ProvisionerEndpointSlice, "kcp-provisioner-endpoint-slice", c.Kcp.ProvisionerEndpointSlice, "APIExportEndpointSlice for the tenancy-provisioner export")
	fs.StringVar(&c.Kcp.TenancyEndpointSlice, "kcp-tenancy-endpoint-slice", c.Kcp.TenancyEndpointSlice, "APIExportEndpointSlice for the tenancy export")
	fs.StringVar(&c.Kcp.AccessEndpointSlice, "kcp-access-endpoint-slice", c.Kcp.AccessEndpointSlice, "APIExportEndpointSlice for the tenancy-access export")

	fs.StringVar(&c.OIDC.UsernameClaim, "oidc-username-claim", c.OIDC.UsernameClaim, "Claim kcp turns into a username; MUST match kcp's own setting (email|sub)")
	fs.StringVar(&c.OIDC.UsernamePrefix, "oidc-username-prefix", c.OIDC.UsernamePrefix, "Prefix kcp prepends to the username claim; MUST match kcp's own setting")
	fs.StringVar(&c.OIDC.IssuerURL, "oidc-issuer-url", c.OIDC.IssuerURL, "The single trusted OIDC issuer; MUST match kcp's own setting (virtual-workspace only)")
	fs.StringVar(&c.OIDC.ClientID, "oidc-client-id", c.OIDC.ClientID, "Audience tokens must carry (virtual-workspace only)")
	fs.StringVar(&c.OIDC.CAFile, "oidc-ca-file", c.OIDC.CAFile, "CA bundle for a private-CA issuer (virtual-workspace only)")

	fs.StringVar(&c.VirtualWorkspace.BindAddress, "virtual-workspace-bind-address", c.VirtualWorkspace.BindAddress, "Address the tenancy virtual workspace serves on")
	fs.StringVar(&c.VirtualWorkspace.TLSCertFile, "virtual-workspace-tls-cert-file", c.VirtualWorkspace.TLSCertFile, "Certificate the tenancy virtual workspace serves")
	fs.StringVar(&c.VirtualWorkspace.TLSKeyFile, "virtual-workspace-tls-private-key-file", c.VirtualWorkspace.TLSKeyFile, "Private key for the tenancy virtual workspace certificate")

	fs.BoolVar(&c.Tenancy.PersonalTenantsEnabled, "tenancy-personal-tenants-enabled", c.Tenancy.PersonalTenantsEnabled, "Give every User a personal Tenant at bootstrap")
	fs.StringVar(&c.Tenancy.PersonalTenantDisplayNameFormat, "tenancy-personal-tenant-display-name-format", c.Tenancy.PersonalTenantDisplayNameFormat, "fmt template for a personal Tenant's display name")
	fs.StringVar(&c.Tenancy.SeedProjectDisplayName, "tenancy-seed-project-display-name", c.Tenancy.SeedProjectDisplayName, "Display name for the Workspace seeded in a personal Tenant")
	fs.Int32Var(&c.Tenancy.TenantQuota, "tenancy-tenant-quota", c.Tenancy.TenantQuota, "Default cap on Tenants per User (personal ones excluded)")
	fs.Int32Var(&c.Tenancy.ProjectQuota, "tenancy-project-quota", c.Tenancy.ProjectQuota, "Default cap on Projects per Tenant")
	fs.StringVar(&c.Tenancy.TenantWorkspaceType, "tenancy-tenant-workspace-type", c.Tenancy.TenantWorkspaceType, "WorkspaceType used for Tenant workspaces")
	fs.StringVar(&c.Tenancy.ProjectWorkspaceType, "tenancy-project-workspace-type", c.Tenancy.ProjectWorkspaceType, "WorkspaceType used for Project workspaces")
	fs.StringVar(&c.Tenancy.NamingStrategy, "tenancy-naming-strategy", c.Tenancy.NamingStrategy,
		"How server-assigned Tenant/Project names are minted; one of "+strings.Join(naming.Registered(), ", "))

	fs.BoolVar(&c.Reconcilers.SeedTenant.Enabled, "reconcilers-seed-tenant-enabled", c.Reconcilers.SeedTenant.Enabled, "Enable the seed Tenant step")
	fs.BoolVar(&c.Reconcilers.TenantWorkspace.Enabled, "reconcilers-tenant-workspace-enabled", c.Reconcilers.TenantWorkspace.Enabled, "Enable the Tenant workspace step")
	fs.BoolVar(&c.Reconcilers.SeedProject.Enabled, "reconcilers-seed-project-enabled", c.Reconcilers.SeedProject.Enabled, "Enable the seed Project step")
	fs.BoolVar(&c.Reconcilers.OwnerMembership.Enabled, "reconcilers-owner-membership-enabled", c.Reconcilers.OwnerMembership.Enabled, "Enable the owner Membership step")
	fs.BoolVar(&c.Reconcilers.ProjectWorkspace.Enabled, "reconcilers-project-workspace-enabled", c.Reconcilers.ProjectWorkspace.Enabled, "Enable the Project workspace step")
	fs.BoolVar(&c.Reconcilers.MembershipRBAC.Enabled, "reconcilers-membership-rbac-enabled", c.Reconcilers.MembershipRBAC.Enabled, "Enable the Membership RBAC step")
	fs.BoolVar(&c.Reconcilers.Index.Enabled, "reconcilers-index-enabled", c.Reconcilers.Index.Enabled, "Enable the membership index step")

	fs.BoolVar(&c.Controllers.User.Enabled, "controllers-user-enabled", c.Controllers.User.Enabled, "Enable the User controller")
	fs.BoolVar(&c.Controllers.Tenant.Enabled, "controllers-tenant-enabled", c.Controllers.Tenant.Enabled, "Enable the Tenant controller")
	fs.BoolVar(&c.Controllers.Project.Enabled, "controllers-project-enabled", c.Controllers.Project.Enabled, "Enable the Project controller")
	fs.BoolVar(&c.Controllers.Membership.Enabled, "controllers-membership-enabled", c.Controllers.Membership.Enabled, "Enable the Membership controller (projects Memberships into RBAC)")
	fs.BoolVar(&c.Controllers.Workspace.Enabled, "controllers-workspace-enabled", c.Controllers.Workspace.Enabled, "Enable the Workspace controller (creates `default` in tenant workspaces)")
}
