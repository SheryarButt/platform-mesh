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

package ocmmodule

import (
	"context"
	"fmt"
	"strings"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/celtemplate"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"
	pmocmmodule "go.platform-mesh.io/platform-mesh-deployer/pkg/ocmmodule"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// certificateGVK is handled unstructured: the deployer only ever writes
// Certificates, so it does not need the cert-manager Go types.
var certificateGVK = schema.GroupVersionKind{
	Group:   "cert-manager.io",
	Version: "v1",
	Kind:    "Certificate",
}

// errServingCertPending signals that a mapped component's certificate has not
// been issued yet.
var errServingCertPending = fmt.Errorf("serving certificate not issued yet")

// errRequestHeaderCAPending signals that kcp-operator has not created the root
// shard's requestheader CA yet.
var errRequestHeaderCAPending = fmt.Errorf("requestheader CA not created yet")

// ensureServingCert issues a TLS certificate for a mapped component's backend
// Service and copies it to the component's cluster.
//
// The front proxy validates a mapped backend against the CA it already mounts,
// which is the root shard's root CA. Issuing from the root shard's server CA
// therefore produces a chain the front proxy trusts without kcp-operator having
// to mount anything extra.
func (r *reconciler) ensureServingCert(ctx context.Context, inst pmocmmodule.Instance, celCtx celtemplate.Context) error {
	mod := r.mod
	mapping := inst.Component.Mapping

	// The name the front proxy will dial, from the same resolver the mapping
	// itself uses. The proxy verifies the certificate against exactly that name,
	// so deriving it twice is how a backend ends up unreachable with both objects
	// looking correct.
	authority, err := mappingAuthority(inst, celCtx)
	if err != nil {
		return err
	}

	issuer, err := r.rootShardIssuer()
	if err != nil {
		return err
	}

	certName := pmocmmodule.ServingCertName(mod.Name, inst.Component.Name, inst.Cluster.ClusterID)
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName(certName)
	cert.SetNamespace(mod.Namespace)
	if err := r.opts.Apply(ctx, mod, cert, func() error {
		cert.SetLabels(pmocmmodule.OCMModuleSelector(mod, inst.Cluster.ClusterID))
		spec := map[string]any{
			"secretName": certName,
			"dnsNames":   toAnySlice(backendDNSNames(mapping, authority)),
			"usages":     []any{"server auth"},
			"issuerRef": map[string]any{
				"name":  issuer,
				"kind":  "Issuer",
				"group": certificateGVK.Group,
			},
		}
		if err := unstructured.SetNestedMap(cert.Object, spec, "spec"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return fmt.Errorf("reconciling Certificate %q: %w", certName, err)
	}

	key := ctrlruntimeclient.ObjectKey{Namespace: mod.Namespace, Name: certName}
	src, err := r.opts.GetSecret(ctx, key)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: %s", errServingCertPending, certName)
		}
		return fmt.Errorf("reading serving certificate %q: %w", certName, err)
	}

	cl := inst.Cluster.Cluster.GetClient()
	if err := sync.EnsureNamespace(ctx, cl, inst.Component.Namespace); err != nil {
		return err
	}
	dst := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      pmocmmodule.ServingCertSecretName(mod.Name, inst.Component.Name),
		Namespace: inst.Component.Namespace,
	}}
	if _, err := controllerutil.CreateOrUpdate(ctx, cl, dst, func() error {
		dst.Labels = pmocmmodule.OCMModuleSelector(mod, inst.Cluster.ClusterID)
		dst.Type = src.Type
		dst.Data = src.Data
		return nil
	}); err != nil {
		return fmt.Errorf("copying serving certificate %q: %w", dst.Name, err)
	}
	return nil
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// backendDNSNames are the names the certificate must be valid for.
//
// For a Service backend that is the four in-cluster spellings, because the
// authority the mapping resolves to is only one of them and a caller inside the
// cluster may legitimately use any. For a `host` backend it is that name alone:
// it is routed from outside, so the in-cluster spellings mean nothing there and
// including them would only widen what the certificate vouches for.
func backendDNSNames(mapping *pmdeployv1alpha1.Mapping, authority string) []string {
	if mapping.Host != "" {
		return []string{authority}
	}

	service, namespace, _ := strings.Cut(authority, ".")
	namespace = strings.TrimSuffix(namespace, ".svc")
	return []string{
		service,
		service + "." + namespace,
		service + "." + namespace + ".svc",
		service + "." + namespace + ".svc.cluster.local",
	}
}

// rootShardIssuer is the name of the cert-manager Issuer for the root shard's
// server CA, which kcp-operator creates alongside the root shard.
func (r *reconciler) rootShardIssuer() (string, error) {
	name, err := r.rootShardName()
	if err != nil {
		return "", err
	}
	return name + "-server-ca", nil
}

// rootShardName is the root shard admin CR name, which kcp-operator derives its
// own secret names from.
func (r *reconciler) rootShardName() (string, error) {
	pm := r.pm
	engaged := r.opts.ClustersFor(pm.Name, components.RootShard)
	if len(engaged) != 1 {
		return "", fmt.Errorf("expected exactly one root shard cluster, found %d", len(engaged))
	}
	return names.RootShard(pm.Name, pm.Spec.Topology.RootShard.Name, engaged[0].ClusterID), nil
}

// ensureRequestHeaderCA copies the front proxy's requestheader CA to a mapped
// component's cluster.
//
// A mapped component is reached through the front proxy, which forwards the
// caller's identity in headers signed by this CA. A component that authorises
// on that identity has to verify it, and the CA is a kcp-operator secret named
// after the root shard, so the deployer copies it rather than have every module
// reconstruct that name.
func (r *reconciler) ensureRequestHeaderCA(ctx context.Context, inst pmocmmodule.Instance) error {
	mod := r.mod

	rootShard, err := r.rootShardName()
	if err != nil {
		return err
	}
	caName := rootShard + "-requestheader-client-ca"

	key := ctrlruntimeclient.ObjectKey{Namespace: mod.Namespace, Name: caName}
	src, err := r.opts.GetSecret(ctx, key)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: %s", errRequestHeaderCAPending, caName)
		}
		return fmt.Errorf("reading requestheader CA %q: %w", caName, err)
	}

	cl := inst.Cluster.Cluster.GetClient()
	if err := sync.EnsureNamespace(ctx, cl, inst.Component.Namespace); err != nil {
		return err
	}
	dst := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      pmocmmodule.RequestHeaderCASecretName(mod.Name, inst.Component.Name),
		Namespace: inst.Component.Namespace,
	}}
	if _, err := controllerutil.CreateOrUpdate(ctx, cl, dst, func() error {
		dst.Labels = pmocmmodule.OCMModuleSelector(mod, inst.Cluster.ClusterID)
		dst.Type = src.Type
		dst.Data = src.Data
		return nil
	}); err != nil {
		return fmt.Errorf("copying requestheader CA %q: %w", dst.Name, err)
	}
	return nil
}
