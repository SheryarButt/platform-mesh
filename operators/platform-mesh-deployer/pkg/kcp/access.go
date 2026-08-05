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

// Package kcp gives the provisioner controllers a client for the kcp instance
// a PlatformMesh runs, by minting an admin kubeconfig through kcp-operator.
package kcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	pmdeployv1alpha1 "go.platform-mesh.io/apis/deploy/v1alpha1"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/names"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	operatorv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/operator/v1alpha1"
)

// ErrPending signals that the admin kubeconfig has not been minted yet.
var ErrPending = errors.New("kcp admin kubeconfig not minted yet")

// adminValidity is the lifetime of the provisioner's client certificate.
const adminValidity = 30 * 24 * time.Hour

// DialFunc overrides how the kcp endpoint is reached. It exists because the
// e2e runs the deployer outside the cluster, where the front proxy's external
// hostname does not resolve; in a normal deployment it is nil.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Access mints and caches the admin kubeconfig of a PlatformMesh's kcp.
type Access struct {
	client   ctrlruntimeclient.Client
	registry *clusters.Registry
	scheme   *runtime.Scheme
	dial     DialFunc
}

func New(client ctrlruntimeclient.Client, registry *clusters.Registry, scheme *runtime.Scheme, dial DialFunc) *Access {
	return &Access{client: client, registry: registry, scheme: scheme, dial: dial}
}

// KubeconfigName is the admin kubeconfig minted for a PlatformMesh.
func KubeconfigName(platformMesh string) string {
	return platformMesh + "-provisioner"
}

// Config mints the admin kubeconfig if needed and returns a rest config for
// the kcp front proxy. It returns ErrPending until kcp-operator has written
// the secret.
func (a *Access) Config(ctx context.Context, pm *pmdeployv1alpha1.PlatformMesh) (*rest.Config, error) {
	frontProxy, err := a.frontProxyRef(pm)
	if err != nil {
		return nil, err
	}

	name := KubeconfigName(pm.Name)
	kc := &operatorv1alpha1.Kubeconfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pm.Namespace},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, a.client, kc, func() error {
		kc.Spec = operatorv1alpha1.KubeconfigSpec{
			Target:          operatorv1alpha1.KubeconfigTarget{FrontProxyRef: &corev1.LocalObjectReference{Name: frontProxy}},
			TargetWorkspace: "root",
			Username:        "platform-mesh-deployer",
			Groups:          []string{"system:kcp:admin"},
			Validity:        metav1.Duration{Duration: adminValidity},
			SecretRef:       corev1.LocalObjectReference{Name: name},
		}
		return controllerutil.SetControllerReference(pm, kc, a.scheme)
	}); err != nil {
		return nil, fmt.Errorf("reconciling admin Kubeconfig %q: %w", name, err)
	}

	secret := &corev1.Secret{}
	key := ctrlruntimeclient.ObjectKey{Namespace: pm.Namespace, Name: name}
	if err := a.client.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s", ErrPending, name)
		}
		return nil, fmt.Errorf("reading admin kubeconfig %q: %w", name, err)
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(secret.Data["kubeconfig"])
	if err != nil {
		return nil, fmt.Errorf("parsing admin kubeconfig %q: %w", name, err)
	}
	// The minted kubeconfig points at a workspace; the callers append their
	// own path, so keep only the origin.
	cfg.Host, _, _ = strings.Cut(cfg.Host, "/clusters/")
	if a.dial != nil {
		cfg.Dial = a.dial
	}
	return cfg, nil
}

// ClientFor returns a client scoped to a workspace path such as "root:modules".
func (a *Access) ClientFor(base *rest.Config, path string) (ctrlruntimeclient.Client, error) {
	cfg := rest.CopyConfig(base)
	cfg.Host = base.Host + "/clusters/" + path
	return ctrlruntimeclient.New(cfg, ctrlruntimeclient.Options{Scheme: a.scheme})
}

// frontProxyRef is the FrontProxy object the admin kubeconfig is minted for.
func (a *Access) frontProxyRef(pm *pmdeployv1alpha1.PlatformMesh) (string, error) {
	engaged := a.registry.ClustersFor(pm.Name, components.FrontProxy)
	if len(engaged) != 1 {
		return "", fmt.Errorf("expected exactly one front proxy cluster, found %d", len(engaged))
	}
	return names.FrontProxy(pm.Name, pm.Spec.Topology.FrontProxy.Name, engaged[0].ClusterID), nil
}
