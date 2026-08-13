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

package virtualworkspace

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	pmtenancyv1alpha1 "go.platform-mesh.io/apis/tenancy/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"

	virtualrootapiserver "github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"
)

// OIDCOptions is the verifier half of "one configuration, read twice".
//
// kcp authenticates the same tokens with the same settings; this is the second
// reader. If the two drift, a caller kcp accepts is rejected here (or worse,
// resolved to a different identity than the one kcp will enforce RBAC against).
type OIDCOptions struct {
	IssuerURL      string
	ClientID       string
	CAFile         string
	UsernameClaim  string
	UsernamePrefix string
	GroupsClaim    string
	GroupsPrefix   string
}

// ServeOptions configures the HTTP surface.
type ServeOptions struct {
	BindAddress string
	TLSCertFile string
	TLSKeyFile  string
	OIDC        OIDCOptions
}

// Serve runs the virtual workspace until ctx is cancelled.
//
// The shape follows the framework's standard root API server: a generic
// apiserver hosting the VW as a delegated API server, so every kube client works
// against it unmodified and errors are Status-shaped by construction rather than
// by convention.
func Serve(ctx context.Context, vw virtualrootapiserver.NamedVirtualWorkspace, opts ServeOptions) error {
	scheme := runtime.NewScheme()
	utilruntime.Must(pmtenancyv1alpha1.AddToScheme(scheme))
	codecs := serializer.NewCodecFactory(scheme)

	recommended := genericapiserver.NewRecommendedConfig(codecs)

	// NewRecommendedConfig leaves the OpenAPI configs nil and the server refuses to
	// build without them. They also cannot be empty: the server resolves a model
	// for every served type, so the definitions are generated from the API package
	// (apis/tenancy/v1alpha1/zz_generated.openapi.go, via `task generate-apis`).
	namer := openapinamer.NewDefinitionNamer(scheme)
	defs := pmtenancyv1alpha1.GetOpenAPIDefinitions
	recommended.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(defs, namer)
	recommended.OpenAPIConfig.Info.Title = "tenancy"
	recommended.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(defs, namer)
	recommended.OpenAPIV3Config.Info.Title = "tenancy"

	secure, err := secureServingOptions(opts)
	if err != nil {
		return err
	}
	if err := secure.ApplyTo(&recommended.SecureServing, &recommended.LoopbackClientConfig); err != nil {
		return fmt.Errorf("configuring secure serving: %w", err)
	}

	// The VW verifies the caller's token itself: it resolves a caller to a User
	// before kcp is involved at all.
	authn, err := newAuthenticator(opts.OIDC)
	if err != nil {
		return err
	}

	rootConfig, err := virtualrootapiserver.NewConfig(recommended)
	if err != nil {
		return fmt.Errorf("building the root API server config: %w", err)
	}

	rootConfig.Extra.VirtualWorkspaces = []virtualrootapiserver.NamedVirtualWorkspace{vw}
	rootConfig.Generic.Authentication.Authenticator = authn

	// The virtual workspace's own authorizer, failing closed. Delegating upward
	// would authorize against kcp's RBAC, which says nothing about the tenant tier.
	rootConfig.Generic.Authorization.Authorizer = vw.VirtualWorkspace

	completed := rootConfig.Complete()
	server, err := virtualrootapiserver.NewServer(completed, genericapiserver.NewEmptyDelegate())
	if err != nil {
		return fmt.Errorf("building the root API server: %w", err)
	}

	prepared := server.GenericAPIServer.PrepareRun()
	if err := completed.WithOpenAPIAggregationController(prepared.GenericAPIServer); err != nil {
		return fmt.Errorf("wiring OpenAPI aggregation: %w", err)
	}

	return prepared.RunWithContext(ctx)
}

// secureServingOptions turns the bind address and certificate paths into the
// generic apiserver's serving options.
//
// A certificate is required rather than self-signed on the fly: the front-proxy
// routes to this server, so an ephemeral cert would fail verification on every
// restart.
func secureServingOptions(opts ServeOptions) (*genericoptions.SecureServingOptionsWithLoopback, error) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(opts.BindAddress))
	if err != nil {
		return nil, fmt.Errorf("bind address %q: %w", opts.BindAddress, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("bind address %q: port must be numeric: %w", opts.BindAddress, err)
	}

	secure := genericoptions.NewSecureServingOptions()
	secure.BindPort = port
	if host != "" {
		secure.BindAddress = net.ParseIP(host)
	}
	if opts.TLSCertFile == "" || opts.TLSKeyFile == "" {
		return nil, fmt.Errorf("a TLS certificate and key are required (--virtual-workspace-tls-cert-file / --virtual-workspace-tls-private-key-file)")
	}
	secure.ServerCert.CertKey.CertFile = opts.TLSCertFile
	secure.ServerCert.CertKey.KeyFile = opts.TLSKeyFile

	return secure.WithLoopback(), nil
}
