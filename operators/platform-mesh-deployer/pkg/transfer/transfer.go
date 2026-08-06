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

// Package transfer copies the compiled kcp-operator CRs from the config plane
// to the workload cluster they were compiled for, along with the Secrets and
// ConfigMaps kcp-operator generated for them, and reflects their status back.
package transfer

import (
	"context"
	"strings"
	"time"

	"go.platform-mesh.io/platform-mesh-deployer/pkg/clusters"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/components"
	"go.platform-mesh.io/platform-mesh-deployer/pkg/sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	deployv1alpha1 "github.com/kcp-dev/kcp-operator/sdk/apis/deploy/v1alpha1"
)

// compiledKinds are the deploy.operator.kcp.io kinds copied to workload clusters.
var compiledKinds = []string{
	"CompiledRootShard",
	"CompiledShard",
	"CompiledFrontProxy",
	"CompiledCacheServer",
	"CompiledVirtualWorkspace",
}

// Controller registers one reconciler per compiled kind.
type Controller struct {
	registry *clusters.Registry
}

func New(registry *clusters.Registry) *Controller {
	return &Controller{registry: registry}
}

func (r *Controller) SetupWithManager(mgr mcmanager.Manager) error {
	local := mgr.GetLocalManager()
	for _, kind := range compiledKinds {
		gvk := deployv1alpha1.SchemeGroupVersion.WithKind(kind)
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		rec := &compiled{gvk: gvk, local: local.GetClient(), registry: r.registry}
		if err := ctrl.NewControllerManagedBy(local).
			For(obj).
			Named("copy-" + kind).
			WithOptions(controller.Options{SkipNameValidation: ptr.To(true)}).
			Complete(rec); err != nil {
			return err
		}
	}
	return nil
}

type compiled struct {
	gvk      schema.GroupVersionKind
	local    ctrlruntimeclient.Client
	registry *clusters.Registry
}

func (r *compiled) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(r.gvk)
	if err := r.local.Get(ctx, req.NamespacedName, src); err != nil {
		return reconcile.Result{}, ctrlruntimeclient.IgnoreNotFound(err)
	}

	target := r.targetCluster(ctx, src.GetLabels())
	if target == nil {
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}
	workload := target.GetClient()

	if err := sync.EnsureNamespace(ctx, workload, req.Namespace); err != nil {
		return reconcile.Result{}, err
	}
	if err := sync.CopySpec(ctx, workload, r.gvk, req.NamespacedName, src); err != nil {
		return reconcile.Result{}, err
	}
	if err := r.copyRelated(ctx, workload, src.GetNamespace(), src.GetLabels()); err != nil {
		return reconcile.Result{}, err
	}
	if err := sync.ReflectStatus(ctx, workload, r.local, r.gvk, req.NamespacedName); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

// targetCluster resolves the workload cluster of a compiled CR from its deployer labels.
func (r *compiled) targetCluster(ctx context.Context, labels map[string]string) cluster.Cluster {
	pm, component, clusterID := labels[components.LabelPlatformMesh], labels[components.LabelComponent], labels[components.LabelCluster]
	if pm == "" || component == "" || clusterID == "" {
		return nil
	}
	for _, c := range r.registry.ClustersFor(pm, component) {
		if c.ClusterID == clusterID {
			return c.Cluster
		}
	}
	return nil
}

// kcpOperatorLabelPrefix marks the labels kcp-operator shares between a compiled CR and its generated Secrets/ConfigMaps.
const kcpOperatorLabelPrefix = "operator.kcp.io/"

// copyRelated copies kcp-operator's generated Secrets/ConfigMaps for the compiled CR to the workload.
func (r *compiled) copyRelated(ctx context.Context, workload ctrlruntimeclient.Client, namespace string, crLabels map[string]string) error {
	seenSecrets := map[string]struct{}{}
	seenConfigMaps := map[string]struct{}{}
	for k, v := range crLabels {
		if !strings.HasPrefix(k, kcpOperatorLabelPrefix) {
			continue
		}
		selector := ctrlruntimeclient.MatchingLabels{k: v}

		secrets := &corev1.SecretList{}
		if err := r.local.List(ctx, secrets, ctrlruntimeclient.InNamespace(namespace), selector); err != nil {
			return err
		}
		for i := range secrets.Items {
			src := &secrets.Items[i]
			if _, ok := seenSecrets[src.Name]; ok {
				continue
			}
			seenSecrets[src.Name] = struct{}{}
			dst := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: src.Namespace}}
			if _, err := controllerutil.CreateOrUpdate(ctx, workload, dst, func() error {
				dst.Labels = src.Labels
				dst.Type = src.Type
				dst.Data = src.Data
				return nil
			}); err != nil {
				return err
			}
		}

		configMaps := &corev1.ConfigMapList{}
		if err := r.local.List(ctx, configMaps, ctrlruntimeclient.InNamespace(namespace), selector); err != nil {
			return err
		}
		for i := range configMaps.Items {
			src := &configMaps.Items[i]
			if _, ok := seenConfigMaps[src.Name]; ok {
				continue
			}
			seenConfigMaps[src.Name] = struct{}{}
			dst := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: src.Namespace}}
			if _, err := controllerutil.CreateOrUpdate(ctx, workload, dst, func() error {
				dst.Labels = src.Labels
				dst.Data = src.Data
				dst.BinaryData = src.BinaryData
				return nil
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
