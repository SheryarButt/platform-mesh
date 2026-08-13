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

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	krov1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpapisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// publishComposite makes the kro-generated composite type a first-class *bound* API in
// the consumer workspace instead of a plain reflected CRD. It snapshots the CRD into an
// APIResourceSchema, exposes it through a per-RGD APIExport, and self-binds that export
// in the same workspace. Being a boundResource is what makes the platform treat the
// type as first-class — the graphql-gateway generates its schema and the
// security-operator generates the OpenFGA authorization model from APIBinding
// boundResources, neither of which sees a plain workspace CRD. Returns true once the
// binding is Bound (type served + boundResource present).
//
// All three objects are owned by the RGD, so deleting the RGD garbage-collects them and
// the platform tears the type back down (authz + schema) on its own.
func (e *Engine) publishComposite(ctx context.Context, c ctrlruntimeclient.Client, crd *apiextensionsv1.CustomResourceDefinition, rgd *krov1alpha1.ResourceGraphDefinition) (bool, error) {
	// APIResourceSchemas are immutable; the content hash in the name means an unchanged
	// schema re-applies to the same object while a changed schema mints a new one.
	ars, err := kcpapisv1alpha1.CRDToAPIResourceSchema(crd, "kroaas-"+schemaHash(crd))
	if err != nil {
		return false, fmt.Errorf("convert CRD to APIResourceSchema: %w", err)
	}
	setRGDOwner(ars, rgd)
	if err := c.Create(ctx, ars); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, fmt.Errorf("apply APIResourceSchema %s: %w", ars.Name, err)
	}

	name := compositeExportName(rgd.Name)

	export := &kcpapisv1alpha1.APIExport{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, export, func() error {
		setRGDOwner(export, rgd)
		export.Spec.LatestResourceSchemas = []string{ars.Name}
		return nil
	}); err != nil {
		return false, fmt.Errorf("apply APIExport %s: %w", name, err)
	}

	// The export now points at the current schema; drop schemas this RGD left behind
	// on earlier edits (APIResourceSchemas are immutable, so a schema change mints a
	// new one). Best-effort cleanup — never blocks serving the current type.
	if err := pruneStaleSchemas(ctx, c, rgd, ars.Name); err != nil {
		logf.FromContext(ctx).V(1).Info("prune stale APIResourceSchemas", "rgd", rgd.Name, "error", err.Error())
	}

	binding := &kcpapisv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, binding, func() error {
		setRGDOwner(binding, rgd)
		// No path → kcp resolves the APIExport in the APIBinding's own logical
		// cluster, i.e. a self-binding within the consumer workspace.
		binding.Spec.Reference = kcpapisv1alpha2.BindingReference{
			Export: &kcpapisv1alpha2.ExportBindingReference{Name: name},
		}
		return nil
	}); err != nil {
		return false, fmt.Errorf("apply APIBinding %s: %w", name, err)
	}

	cur := &kcpapisv1alpha2.APIBinding{}
	if err := c.Get(ctx, types.NamespacedName{Name: name}, cur); err != nil {
		return false, err
	}
	return cur.Status.Phase == kcpapisv1alpha2.APIBindingPhaseBound, nil
}

// teardownComposite removes the published objects in an order that lets the platform's
// APIBinding finalizer (account-operator, which strips the type's OpenFGA authz) run
// while the APIExport + schema still exist: delete the APIBinding and wait until it is
// fully gone, then delete the APIExport and the RGD's APIResourceSchemas. Returns
// done=true once all are gone. The objects are also owner-referenced by the RGD as a GC
// backstop, but the RGD is held Terminating until this completes so nothing is collected
// out from under the finalizer (which was the cause of the leaked-authz/zombie-binding).
//
// force is set when the whole workspace is terminating (account/workspace deletion). The
// ordered authz-strip is then moot — the workspace's authz store is being torn down too
// — and the binding's platform apibinding-finalizer can't be processed mid-teardown, so
// waiting on it would deadlock workspace (hence account) deletion. In that case we drop
// the binding's finalizers and proceed without waiting.
func teardownComposite(ctx context.Context, c ctrlruntimeclient.Client, rgd *krov1alpha1.ResourceGraphDefinition, force bool) (bool, error) {
	name := compositeExportName(rgd.Name)

	// 1. Delete the APIBinding; wait until it is fully gone before touching the export,
	//    so its finalizer can strip authz against the still-present export/schema.
	binding := &kcpapisv1alpha2.APIBinding{}
	switch err := c.Get(ctx, types.NamespacedName{Name: name}, binding); {
	case err == nil:
		if binding.DeletionTimestamp.IsZero() {
			if delErr := c.Delete(ctx, binding); delErr != nil && !apierrors.IsNotFound(delErr) {
				return false, fmt.Errorf("delete APIBinding %s: %w", name, delErr)
			}
		}
		if !force {
			return false, nil // ordered path: wait until the binding is fully gone
		}
		// Workspace terminating: release the stuck finalizer so the binding can go and
		// stop waiting (race-free merge patch; tolerate the binding already being gone).
		if len(binding.Finalizers) > 0 {
			patch := ctrlruntimeclient.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`))
			if err := c.Patch(ctx, binding, patch); err != nil && !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("clear APIBinding %s finalizers: %w", name, err)
			}
		}
	case !apierrors.IsNotFound(err):
		return false, fmt.Errorf("get APIBinding %s: %w", name, err)
	}

	// 2. Binding gone -> delete the APIExport.
	export := &kcpapisv1alpha1.APIExport{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := c.Delete(ctx, export); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("delete APIExport %s: %w", name, err)
	}

	// 3. Delete this RGD's APIResourceSchemas.
	list := &kcpapisv1alpha1.APIResourceSchemaList{}
	if err := c.List(ctx, list); err != nil {
		return false, err
	}
	for i := range list.Items {
		ars := &list.Items[i]
		if !ownedByRGD(ars, rgd) {
			continue
		}
		if err := c.Delete(ctx, ars); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete APIResourceSchema %s: %w", ars.Name, err)
		}
	}
	return true, nil
}

// compositeExportName is the per-RGD APIExport/APIBinding name in the consumer workspace.
func compositeExportName(rgdName string) string { return "kro-" + rgdName }

// schemaHash is a short stable digest of the CRD spec, used in the (immutable)
// APIResourceSchema name so a schema change produces a new schema object.
func schemaHash(crd *apiextensionsv1.CustomResourceDefinition) string {
	b, _ := json.Marshal(crd.Spec)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// pruneStaleSchemas deletes APIResourceSchemas owned by this RGD other than keep —
// the orphans left when the RGD's (immutable) schema is re-minted after an edit.
func pruneStaleSchemas(ctx context.Context, c ctrlruntimeclient.Client, rgd *krov1alpha1.ResourceGraphDefinition, keep string) error {
	list := &kcpapisv1alpha1.APIResourceSchemaList{}
	if err := c.List(ctx, list); err != nil {
		return err
	}
	for i := range list.Items {
		ars := &list.Items[i]
		if ars.Name == keep || !ownedByRGD(ars, rgd) {
			continue
		}
		if err := c.Delete(ctx, ars); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func ownedByRGD(o metav1.Object, rgd *krov1alpha1.ResourceGraphDefinition) bool {
	for _, ref := range o.GetOwnerReferences() {
		if ref.UID == rgd.UID {
			return true
		}
	}
	return false
}

func setRGDOwner(o metav1.Object, rgd *krov1alpha1.ResourceGraphDefinition) {
	o.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: krov1alpha1.GroupVersion.String(),
		Kind:       "ResourceGraphDefinition",
		Name:       rgd.Name,
		UID:        rgd.UID,
	}})
}
