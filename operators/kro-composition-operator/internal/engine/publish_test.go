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
	"testing"

	krov1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	kroapis "github.com/kubernetes-sigs/kro/pkg/apis"
	rgdcontroller "github.com/kubernetes-sigs/kro/pkg/controller/resourcegraphdefinition"
	krograph "github.com/kubernetes-sigs/kro/pkg/graph"
	"github.com/stretchr/testify/require"

	"go.platform-mesh.io/kro-composition-operator/internal/composition"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/metadata"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kcpapisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpapisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func TestCompositeExportName(t *testing.T) {
	t.Parallel()
	require.Equal(t, "kro-webpage", compositeExportName("webpage"))
}

func TestSchemaHash(t *testing.T) {
	t.Parallel()
	a := &apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: "a.example.com"}}
	b := &apiextensionsv1.CustomResourceDefinition{Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: "b.example.com"}}

	ha := schemaHash(a)
	require.Len(t, ha, 12)
	require.Equal(t, ha, schemaHash(a), "hash must be stable for the same spec")
	require.NotEqual(t, ha, schemaHash(b), "different specs must hash differently")

	// The APIResourceSchema is immutable, so labels and annotations from spec.schema.metadata
	// only reach a consumer if a change to them mints a new schema object.
	labelled := a.DeepCopy()
	labelled.Labels = map[string]string{"team": "platform"}
	require.NotEqual(t, ha, schemaHash(labelled), "a metadata change must mint a new schema")
}

func TestOwnedByRGD(t *testing.T) {
	t.Parallel()
	rgd := &krov1alpha1.ResourceGraphDefinition{ObjectMeta: metav1.ObjectMeta{Name: "rgd", UID: "rgd-uid"}}
	owned := &metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{UID: "rgd-uid"}}}
	other := &metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{UID: "someone-else"}}}
	none := &metav1.ObjectMeta{}

	require.True(t, ownedByRGD(owned, rgd), "owner ref UID matches the RGD")
	require.False(t, ownedByRGD(other, rgd), "non-matching owner ref")
	require.False(t, ownedByRGD(none, rgd), "no owner refs")
}

// teardownScheme registers the types teardownComposite touches.
func teardownScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		kcpapisv1alpha1.AddToScheme,
		kcpapisv1alpha2.AddToScheme,
		krov1alpha1.AddToScheme,
	} {
		require.NoError(t, add(s))
	}
	return s
}

// TestTeardownCompositeDeletesIdentitySecret covers the secret kcp creates for every
// APIExport: it carries no owner reference, so unless teardown removes it explicitly the
// consumer workspace keeps one per RGD forever.
func TestTeardownCompositeDeletesIdentitySecret(t *testing.T) {
	t.Parallel()

	rgd := &krov1alpha1.ResourceGraphDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "webpage", UID: "rgd-uid"},
	}
	exportName := compositeExportName(rgd.Name) // kro-webpage
	kcpDefault := types.NamespacedName{Namespace: "kcp-system", Name: exportName}

	// kcp writes spec.identity.secretRef itself, pointing at its own default location, so a
	// realistic export always carries the reference.
	t.Run("deletes the secret kcp created, whose ref kcp populates", func(t *testing.T) {
		t.Parallel()

		export := &kcpapisv1alpha2.APIExport{ObjectMeta: metav1.ObjectMeta{Name: exportName}}
		export.Spec.Identity = &kcpapisv1alpha2.Identity{
			SecretRef: &corev1.SecretReference{Namespace: kcpDefault.Namespace, Name: kcpDefault.Name},
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      kcpDefault.Name,
			Namespace: kcpDefault.Namespace,
		}}
		c := fake.NewClientBuilder().
			WithScheme(teardownScheme(t)).
			WithObjects(export, secret).
			Build()

		// No APIBinding present, so teardown runs straight through to completion.
		done, err := teardownComposite(context.Background(), c, rgd, false)
		require.NoError(t, err)
		require.True(t, done, "teardown should complete when no binding remains")

		require.True(t, apierrors.IsNotFound(
			c.Get(context.Background(), kcpDefault, &corev1.Secret{})),
			"identity secret %s must be deleted", kcpDefault)
		require.True(t, apierrors.IsNotFound(
			c.Get(context.Background(), types.NamespacedName{Name: exportName}, &kcpapisv1alpha2.APIExport{})),
			"APIExport must be deleted")
	})

	t.Run("deletes the default secret when no ref is set yet", func(t *testing.T) {
		t.Parallel()

		export := &kcpapisv1alpha2.APIExport{ObjectMeta: metav1.ObjectMeta{Name: exportName}}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      kcpDefault.Name,
			Namespace: kcpDefault.Namespace,
		}}
		c := fake.NewClientBuilder().
			WithScheme(teardownScheme(t)).
			WithObjects(export, secret).
			Build()

		done, err := teardownComposite(context.Background(), c, rgd, false)
		require.NoError(t, err)
		require.True(t, done)
		require.True(t, apierrors.IsNotFound(
			c.Get(context.Background(), kcpDefault, &corev1.Secret{})))
	})

	// A secret pointed somewhere other than kcp's default location was put there by whoever
	// owns it and may be shared with another export, so teardown must leave it alone.
	t.Run("leaves a secret outside kcp's default location alone", func(t *testing.T) {
		t.Parallel()

		shared := types.NamespacedName{Namespace: "custom-ns", Name: "shared-key"}
		export := &kcpapisv1alpha2.APIExport{ObjectMeta: metav1.ObjectMeta{Name: exportName}}
		export.Spec.Identity = &kcpapisv1alpha2.Identity{
			SecretRef: &corev1.SecretReference{Namespace: shared.Namespace, Name: shared.Name},
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      shared.Name,
			Namespace: shared.Namespace,
		}}
		c := fake.NewClientBuilder().
			WithScheme(teardownScheme(t)).
			WithObjects(export, secret).
			Build()

		done, err := teardownComposite(context.Background(), c, rgd, false)
		require.NoError(t, err)
		require.True(t, done)

		require.NoError(t, c.Get(context.Background(), shared, &corev1.Secret{}),
			"a secret the export names must survive teardown")
	})
}

// TestTeardownCompositeToleratesMissingIdentitySecret guards the common path where the
// secret is already gone (or was never created, e.g. envtest without a real kcp).
func TestTeardownCompositeToleratesMissingIdentitySecret(t *testing.T) {
	t.Parallel()

	rgd := &krov1alpha1.ResourceGraphDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "webpage", UID: "rgd-uid"},
	}
	var c ctrlruntimeclient.Client = fake.NewClientBuilder().WithScheme(teardownScheme(t)).Build()

	done, err := teardownComposite(context.Background(), c, rgd, false)
	require.NoError(t, err, "missing export and secret must not be an error")
	require.True(t, done)
}

// writeRGDStatus reports the type as Active either way. PortalQueryable is what carries
// whether the portal can query it.
func TestWriteRGDStatus_PortalQueryableCondition(t *testing.T) {
	t.Parallel()

	condition := func(t *testing.T, c ctrlruntimeclient.Client) krov1alpha1.Condition {
		t.Helper()
		cur := &krov1alpha1.ResourceGraphDefinition{}
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "webpage"}, cur))
		require.Equal(t, krov1alpha1.ResourceGraphDefinitionStateActive, cur.Status.State,
			"the API is served regardless, so the RGD stays Active")
		for _, cond := range cur.Status.Conditions {
			if cond.Type == conditionTypePortalQueryable {
				return cond
			}
		}
		t.Fatalf("PortalQueryable condition not set")
		return krov1alpha1.Condition{}
	}
	newClient := func() ctrlruntimeclient.Client {
		rgd := &krov1alpha1.ResourceGraphDefinition{ObjectMeta: metav1.ObjectMeta{Name: "webpage"}}
		return fake.NewClientBuilder().
			WithScheme(teardownScheme(t)).
			WithObjects(rgd).
			WithStatusSubresource(rgd).
			Build()
	}

	t.Run("queryable group", func(t *testing.T) {
		t.Parallel()
		c := newClient()
		require.NoError(t, writeRGDStatus(context.Background(), c, "webpage", graphStatus{kind: "WebPage", topologicalOrder: []string{"config"}}, nil))
		cond := condition(t, c)
		require.Equal(t, metav1.ConditionTrue, cond.Status)
		require.Nil(t, cond.Reason)
	})

	t.Run("unqueryable group records the reason", func(t *testing.T) {
		t.Parallel()
		c := newClient()
		portalErr := composition.ValidateAPIGroup("demo.platform-mesh.dev")
		require.Error(t, portalErr)
		require.NoError(t, writeRGDStatus(context.Background(), c, "webpage", graphStatus{kind: "WebPage", topologicalOrder: []string{"config"}}, portalErr))

		cond := condition(t, c)
		require.Equal(t, metav1.ConditionFalse, cond.Status)
		require.NotNil(t, cond.Reason)
		require.Equal(t, "InvalidAPIGroup", *cond.Reason)
		require.NotNil(t, cond.Message)
		require.Contains(t, *cond.Message, "platform-mesh")
	})

	// PortalQueryable is not one of kro's conditions, so it must not drag the aggregate
	// Ready condition down: an unqueryable group still serves a working API.
	t.Run("an unqueryable group leaves Ready true", func(t *testing.T) {
		t.Parallel()
		c := newClient()
		portalErr := composition.ValidateAPIGroup("demo.platform-mesh.dev")
		require.NoError(t, writeRGDStatus(context.Background(), c, "webpage", graphStatus{kind: "WebPage", topologicalOrder: []string{"config"}}, portalErr))
		require.Equal(t, metav1.ConditionTrue, namedCondition(t, c, "webpage", kroapis.ConditionReady).Status)
	})
}

// The conditions come from kro's marker, so they carry kro's reasons and messages and roll up
// into the Ready condition kro derives. None of that happens if the conditions are set by hand.
func TestWriteRGDStatus_KroConditions(t *testing.T) {
	t.Parallel()

	rgd := &krov1alpha1.ResourceGraphDefinition{ObjectMeta: metav1.ObjectMeta{Name: "webpage"}}
	c := fake.NewClientBuilder().
		WithScheme(teardownScheme(t)).
		WithObjects(rgd).
		WithStatusSubresource(rgd).
		Build()

	require.NoError(t, writeRGDStatus(context.Background(), c, "webpage", graphStatus{kind: "WebPage", topologicalOrder: []string{"config"}}, nil))

	for _, tc := range []struct{ condition, reason string }{
		{rgdcontroller.GraphAccepted, "Valid"},
		{rgdcontroller.KindReady, "Ready"},
		{rgdcontroller.ControllerReady, "Running"},
		{rgdcontroller.GraphRevisionsResolved, "Resolved"},
	} {
		cond := namedCondition(t, c, "webpage", tc.condition)
		require.Equal(t, metav1.ConditionTrue, cond.Status, tc.condition)
		require.NotNil(t, cond.Reason, tc.condition)
		require.Equal(t, tc.reason, *cond.Reason, tc.condition)
	}

	ready := namedCondition(t, c, "webpage", kroapis.ConditionReady)
	require.Equal(t, metav1.ConditionTrue, ready.Status,
		"all four dependents are true, so kro's aggregate must be true")

	// A refusal flips KindReady and the aggregate with it, without touching the others.
	require.NoError(t, writeRGDKindNotReady(context.Background(), c, "webpage", "breaking schema changes: spec.replicas removed"))

	kind := namedCondition(t, c, "webpage", rgdcontroller.KindReady)
	require.Equal(t, metav1.ConditionFalse, kind.Status)
	require.NotNil(t, kind.Message)
	require.Contains(t, *kind.Message, "spec.replicas")
	require.Equal(t, metav1.ConditionTrue, namedCondition(t, c, "webpage", rgdcontroller.GraphAccepted).Status,
		"the graph still compiled, so only KindReady moves")
	require.Equal(t, metav1.ConditionFalse, namedCondition(t, c, "webpage", kroapis.ConditionReady).Status)

	cur := &krov1alpha1.ResourceGraphDefinition{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "webpage"}, cur))
	require.Equal(t, krov1alpha1.ResourceGraphDefinitionStateInactive, cur.Status.State)
}

// A graph that will not build has to say so on the RGD. Otherwise a reference to an API the
// workspace never serves is indistinguishable from one that is about to resolve.
func TestWriteRGDGraphInvalid(t *testing.T) {
	t.Parallel()

	rgd := &krov1alpha1.ResourceGraphDefinition{ObjectMeta: metav1.ObjectMeta{Name: "webpage"}}
	c := fake.NewClientBuilder().
		WithScheme(teardownScheme(t)).
		WithObjects(rgd).
		WithStatusSubresource(rgd).
		Build()

	const msg = `RGD not compilable yet: no matches for kind "Deployment"`
	require.NoError(t, writeRGDGraphInvalid(context.Background(), c, "webpage", msg))

	accepted := namedCondition(t, c, "webpage", rgdcontroller.GraphAccepted)
	require.Equal(t, metav1.ConditionFalse, accepted.Status)
	require.NotNil(t, accepted.Reason)
	require.Equal(t, "InvalidResourceGraph", *accepted.Reason, "kro's reason for a graph that will not build")
	require.NotNil(t, accepted.Message)
	require.Contains(t, *accepted.Message, "Deployment")
	require.Equal(t, metav1.ConditionFalse, namedCondition(t, c, "webpage", kroapis.ConditionReady).Status)

	cur := &krov1alpha1.ResourceGraphDefinition{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "webpage"}, cur))
	require.Equal(t, krov1alpha1.ResourceGraphDefinitionStateInactive, cur.Status.State)

	// The compile path retries on a timer, so the same failure must not rewrite the status.
	t.Run("repeating the same failure writes nothing", func(t *testing.T) {
		before := cur.Status.Conditions
		require.NoError(t, writeRGDGraphInvalid(context.Background(), c, "webpage", msg))
		after := &krov1alpha1.ResourceGraphDefinition{}
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "webpage"}, after))
		require.Equal(t, before, after.Status.Conditions)
		require.Equal(t, cur.ResourceVersion, after.ResourceVersion, "no update should have been issued")
	})

	t.Run("a different failure is written", func(t *testing.T) {
		require.NoError(t, writeRGDGraphInvalid(context.Background(), c, "webpage", "something else"))
		require.Contains(t, *namedCondition(t, c, "webpage", rgdcontroller.GraphAccepted).Message, "something else")
	})

	// Fixing the RGD takes the normal success path, which must clear the condition.
	t.Run("a graph that compiles clears it", func(t *testing.T) {
		require.NoError(t, writeRGDStatus(context.Background(), c, "webpage",
			graphStatus{kind: "WebPage", topologicalOrder: []string{"config"}}, nil))
		require.Equal(t, metav1.ConditionTrue, namedCondition(t, c, "webpage", rgdcontroller.GraphAccepted).Status)
		require.Equal(t, metav1.ConditionTrue, namedCondition(t, c, "webpage", kroapis.ConditionReady).Status)

		after := &krov1alpha1.ResourceGraphDefinition{}
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "webpage"}, after))
		require.Equal(t, krov1alpha1.ResourceGraphDefinitionStateActive, after.Status.State)
	})
}

// status.resources is what shows, on the RGD itself, how a graph resolved its wiring.
func TestResourceDependencies(t *testing.T) {
	t.Parallel()

	node := func(deps ...string) *krograph.Node {
		return &krograph.Node{Meta: krograph.NodeMeta{Dependencies: deps}}
	}
	g := &krograph.Graph{
		TopologicalOrder: []string{"ns", "config", "deployment"},
		Nodes: map[string]*krograph.Node{
			"ns":         node(),
			"config":     node("ns"),
			"deployment": node("ns", "config"),
		},
	}

	require.Equal(t, []krov1alpha1.ResourceInformation{
		{ID: "config", Dependencies: []krov1alpha1.Dependency{{ID: "ns"}}},
		{ID: "deployment", Dependencies: []krov1alpha1.Dependency{{ID: "ns"}, {ID: "config"}}},
	}, resourceDependencies(g), "reported in topological order, roots omitted as in kro")

	t.Run("a graph with no dependencies reports nothing", func(t *testing.T) {
		t.Parallel()
		flat := &krograph.Graph{
			TopologicalOrder: []string{"a", "b"},
			Nodes:            map[string]*krograph.Node{"a": node(), "b": node()},
		}
		require.Empty(t, resourceDependencies(flat))
	})

	// The order comes from TopologicalOrder rather than the map, so a node named there but
	// absent from Nodes must be skipped rather than panic.
	t.Run("an order naming an unknown node is skipped", func(t *testing.T) {
		t.Parallel()
		partial := &krograph.Graph{
			TopologicalOrder: []string{"ghost", "config"},
			Nodes:            map[string]*krograph.Node{"config": node("ns")},
		}
		require.Equal(t, []krov1alpha1.ResourceInformation{
			{ID: "config", Dependencies: []krov1alpha1.Dependency{{ID: "ns"}}},
		}, resourceDependencies(partial))
	})
}

func namedCondition(t *testing.T, c ctrlruntimeclient.Client, rgdName, conditionType string) krov1alpha1.Condition {
	t.Helper()
	cur := &krov1alpha1.ResourceGraphDefinition{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: rgdName}, cur))
	for _, cond := range cur.Status.Conditions {
		if string(cond.Type) == conditionType {
			return cond
		}
	}
	t.Fatalf("condition %s not set on RGD %s", conditionType, rgdName)
	return krov1alpha1.Condition{}
}

// compositeCRD builds a generated composite CRD with the given spec properties, in the shape
// kro produces: one served storage version with a status subresource.
func compositeCRD(specProps map[string]apiextensionsv1.JSONSchemaProps, required ...string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.apps.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "apps.example.com",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind: "Widget", ListKind: "WidgetList", Plural: "widgets", Singular: "widget",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1alpha1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"spec": {Type: "object", Properties: specProps, Required: required},
						},
					},
				},
				Subresources: &apiextensionsv1.CustomResourceSubresources{
					Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
				},
			}},
		},
	}
}

func stringProps(names ...string) map[string]apiextensionsv1.JSONSchemaProps {
	props := make(map[string]apiextensionsv1.JSONSchemaProps, len(names))
	for _, n := range names {
		props[n] = apiextensionsv1.JSONSchemaProps{Type: "string"}
	}
	return props
}

// publishedClient returns a client holding the state publishComposite leaves behind for crd:
// an APIResourceSchema and an APIExport pointing at it. The APIBinding never reaches Bound
// against a fake client, which is not what these tests are about.
func publishedClient(t *testing.T, rgd *krov1alpha1.ResourceGraphDefinition, crd *apiextensionsv1.CustomResourceDefinition) ctrlruntimeclient.Client {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(teardownScheme(t)).WithObjects(rgd).Build()
	_, err := (&Engine{}).publishComposite(context.Background(), c, crd, rgd)
	require.NoError(t, err)

	// Assert the state the compatibility check reads, so a test that expects no breaking
	// change cannot pass merely because there was nothing to compare against.
	export := &kcpapisv1alpha2.APIExport{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: compositeExportName(rgd.Name)}, export))
	require.Len(t, export.Spec.Resources, 1)
	// The entry is keyed by name+group, so a wrong one publishes the schema under a type
	// nobody asked for rather than failing.
	require.Equal(t, crd.Spec.Names.Plural, export.Spec.Resources[0].Name)
	require.Equal(t, crd.Spec.Group, export.Spec.Resources[0].Group)
	require.NotNil(t, export.Spec.Resources[0].Storage.CRD, "the type is served as a CRD")
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: export.Spec.Resources[0].Schema}, &kcpapisv1alpha1.APIResourceSchema{}))
	return c
}

// checkSchemaCompatibility is what stands between an RGD edit and the composite type's schema
// being swapped under live instances. kcp validates each APIResourceSchema on its own and
// knows nothing of the one it replaces, so nothing else would catch this.
func TestCheckSchemaCompatibility(t *testing.T) {
	t.Parallel()

	rgd := &krov1alpha1.ResourceGraphDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widget", UID: "rgd-uid"},
	}
	served := compositeCRD(stringProps("image", "replicas"))

	t.Run("nothing published yet", func(t *testing.T) {
		t.Parallel()
		c := fake.NewClientBuilder().WithScheme(teardownScheme(t)).WithObjects(rgd).Build()
		require.NoError(t, checkSchemaCompatibility(context.Background(), c, rgd, served))
	})

	t.Run("unchanged schema", func(t *testing.T) {
		t.Parallel()
		c := publishedClient(t, rgd, served)
		require.NoError(t, checkSchemaCompatibility(context.Background(), c, rgd, served))
	})

	t.Run("added optional field", func(t *testing.T) {
		t.Parallel()
		c := publishedClient(t, rgd, served)
		next := compositeCRD(stringProps("image", "replicas", "tier"))
		require.NoError(t, checkSchemaCompatibility(context.Background(), c, rgd, next),
			"widening the schema keeps existing instances valid")
	})

	t.Run("removed field is refused", func(t *testing.T) {
		t.Parallel()
		c := publishedClient(t, rgd, served)
		next := compositeCRD(stringProps("image"))

		err := checkSchemaCompatibility(context.Background(), c, rgd, next)
		require.Error(t, err)
		require.True(t, isBreakingSchemaChange(err))
		require.Contains(t, err.Error(), "replicas")
		require.Contains(t, err.Error(), krov1alpha1.AllowBreakingChangesAnnotation,
			"the message must name the way out")
	})

	t.Run("newly required field is refused", func(t *testing.T) {
		t.Parallel()
		c := publishedClient(t, rgd, served)
		next := compositeCRD(stringProps("image", "replicas"), "image")

		err := checkSchemaCompatibility(context.Background(), c, rgd, next)
		require.Error(t, err)
		require.True(t, isBreakingSchemaChange(err))
	})

	t.Run("annotation opts out", func(t *testing.T) {
		t.Parallel()
		c := publishedClient(t, rgd, served)
		allowed := rgd.DeepCopy()
		allowed.Annotations = map[string]string{krov1alpha1.AllowBreakingChangesAnnotation: "true"}
		next := compositeCRD(stringProps("image"))

		require.NoError(t, checkSchemaCompatibility(context.Background(), c, allowed, next))
	})
}

// TestPublishCompositeRefusesBreakingChange covers what the refusal protects: the schema the
// consumer workspace serves must not move.
func TestPublishCompositeRefusesBreakingChange(t *testing.T) {
	t.Parallel()

	rgd := &krov1alpha1.ResourceGraphDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widget", UID: "rgd-uid"},
	}
	served := compositeCRD(stringProps("image", "replicas"))
	c := publishedClient(t, rgd, served)

	export := &kcpapisv1alpha2.APIExport{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: compositeExportName(rgd.Name)}, export))
	before := export.Spec.Resources

	_, err := (&Engine{}).publishComposite(context.Background(), c, compositeCRD(stringProps("image")), rgd)
	require.Error(t, err)
	require.True(t, isBreakingSchemaChange(err))

	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: compositeExportName(rgd.Name)}, export))
	require.Equal(t, before, export.Spec.Resources,
		"the export must still point at the schema it was serving")

	list := &kcpapisv1alpha1.APIResourceSchemaList{}
	require.NoError(t, c.List(context.Background(), list))
	require.Len(t, list.Items, 1, "no schema may be minted for a refused change")
}

// TestPublishCompositeRefusesForeignType covers two RGDs racing for one composite type.
//
// An APIResourceSchema is named after a hash of its content, so identical schemas resolve to
// one object. Without an ownership check the second RGD's Create returns AlreadyExists, both
// APIExports point at a schema owned by the first RGD, and deleting that RGD collects the
// schema out from under the second.
func TestPublishCompositeRefusesForeignType(t *testing.T) {
	t.Parallel()

	alpha := &krov1alpha1.ResourceGraphDefinition{ObjectMeta: metav1.ObjectMeta{Name: "alpha", UID: "uid-a"}}
	beta := &krov1alpha1.ResourceGraphDefinition{ObjectMeta: metav1.ObjectMeta{Name: "beta", UID: "uid-b"}}
	crd := compositeCRD(stringProps("image"))

	newClient := func() ctrlruntimeclient.Client {
		return fake.NewClientBuilder().WithScheme(teardownScheme(t)).WithObjects(alpha, beta).Build()
	}
	schemaCount := func(t *testing.T, c ctrlruntimeclient.Client) int {
		t.Helper()
		list := &kcpapisv1alpha1.APIResourceSchemaList{}
		require.NoError(t, c.List(context.Background(), list))
		return len(list.Items)
	}

	t.Run("an identical schema is refused, not shared", func(t *testing.T) {
		t.Parallel()
		c := newClient()
		_, err := (&Engine{}).publishComposite(context.Background(), c, crd, alpha)
		require.NoError(t, err)

		_, err = (&Engine{}).publishComposite(context.Background(), c, crd, beta)
		require.Error(t, err)
		require.True(t, isTypeConflict(err))
		require.Contains(t, err.Error(), "alpha", "the message must name the RGD holding the type")

		require.Equal(t, 1, schemaCount(t, c))
		export := &kcpapisv1alpha2.APIExport{}
		require.True(t, apierrors.IsNotFound(
			c.Get(context.Background(), types.NamespacedName{Name: compositeExportName("beta")}, export)),
			"the refused RGD must not get an APIExport pointing at someone else's schema")
	})

	// The same group and kind with a different schema hashes differently, so the collision is
	// two APIExports serving one type into one workspace rather than a shared schema.
	t.Run("the same kind with a different schema is refused", func(t *testing.T) {
		t.Parallel()
		c := newClient()
		_, err := (&Engine{}).publishComposite(context.Background(), c, crd, alpha)
		require.NoError(t, err)

		_, err = (&Engine{}).publishComposite(context.Background(), c, compositeCRD(stringProps("image", "tier")), beta)
		require.Error(t, err)
		require.True(t, isTypeConflict(err))
		require.Equal(t, 1, schemaCount(t, c))
	})

	t.Run("a different kind is not a conflict", func(t *testing.T) {
		t.Parallel()
		c := newClient()
		_, err := (&Engine{}).publishComposite(context.Background(), c, crd, alpha)
		require.NoError(t, err)

		other := compositeCRD(stringProps("image"))
		other.Spec.Names.Plural = "gadgets"
		other.Spec.Names.Kind = "Gadget"
		other.Name = "gadgets.apps.example.com"
		_, err = (&Engine{}).publishComposite(context.Background(), c, other, beta)
		require.NoError(t, err)
		require.Equal(t, 2, schemaCount(t, c))
	})

	// Ownership is compared by RGD name, so an RGD deleted and recreated reclaims its type.
	// The stale owner UID has to be restamped too: pruning and teardown key on the UID, so a
	// schema left carrying the old one would never be collected.
	t.Run("an RGD recreated under the same name reclaims and restamps its type", func(t *testing.T) {
		t.Parallel()
		c := newClient()
		_, err := (&Engine{}).publishComposite(context.Background(), c, crd, alpha)
		require.NoError(t, err)

		reborn := &krov1alpha1.ResourceGraphDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha", UID: "uid-a-again"},
		}
		_, err = (&Engine{}).publishComposite(context.Background(), c, crd, reborn)
		require.NoError(t, err, "same name, so it owns the type")
		require.Equal(t, 1, schemaCount(t, c))

		list := &kcpapisv1alpha1.APIResourceSchemaList{}
		require.NoError(t, c.List(context.Background(), list))
		require.True(t, ownedByRGD(&list.Items[0], reborn),
			"the owner UID must be restamped or the schema is never collected")
	})
}

// The RGD's spec.schema.metadata is metadata for the generated type, which here is the
// APIResourceSchema. kcp's CRD conversion keeps only the name, so it has to be carried over.
func TestPublishCompositeCarriesSchemaMetadata(t *testing.T) {
	t.Parallel()

	rgd := &krov1alpha1.ResourceGraphDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widget", UID: "rgd-uid"},
	}
	crd := compositeCRD(stringProps("image"))
	crd.Labels = map[string]string{"team": "platform"}
	crd.Annotations = map[string]string{"docs.example.com/url": "https://example.com/widget"}

	c := publishedClient(t, rgd, crd)

	list := &kcpapisv1alpha1.APIResourceSchemaList{}
	require.NoError(t, c.List(context.Background(), list))
	require.Len(t, list.Items, 1)
	require.Equal(t, "platform", list.Items[0].Labels["team"])
	require.Equal(t, "https://example.com/widget", list.Items[0].Annotations["docs.example.com/url"])
}

func TestMergeMissing(t *testing.T) {
	t.Parallel()

	require.Nil(t, mergeMissing(nil, nil))
	require.Equal(t, map[string]string{"a": "1"}, mergeMissing(nil, map[string]string{"a": "1"}))
	require.Equal(t,
		map[string]string{"ours": "keep", "theirs": "add"},
		mergeMissing(map[string]string{"ours": "keep"}, map[string]string{"theirs": "add", "ours": "clobber"}),
		"keys the operator set must not be overwritten by the RGD")

	// These are live label maps off an object, and the same map can back more than one
	// object, so neither input may be modified.
	into := map[string]string{"ours": "keep"}
	from := map[string]string{"theirs": "add"}
	out := mergeMissing(into, from)
	require.Equal(t, map[string]string{"ours": "keep"}, into, "into must not be modified")
	require.Equal(t, map[string]string{"theirs": "add"}, from, "from must not be modified")
	out["mutated"] = "yes"
	require.NotContains(t, into, "mutated", "the result must not alias into")
	require.NotContains(t, from, "mutated", "the result must not alias from")

	// The common call shape: an RGD with no spec.schema.metadata leaves from empty while
	// into already carries the ownership labels.
	require.Equal(t, map[string]string{"ours": "keep"}, mergeMissing(map[string]string{"ours": "keep"}, nil))
}

// drainInstances runs before the composite API is unserved. Its contract is what keeps an
// instance from being stranded with a finalizer no controller can reach.
func TestDrainInstances(t *testing.T) {
	t.Parallel()

	gvr := schema.GroupVersionResource{Group: "apps.example.com", Version: "v1alpha1", Resource: "widgets"}
	instance := func(name string, finalizers ...string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("apps.example.com/v1alpha1")
		u.SetKind("Widget")
		u.SetName(name)
		u.SetNamespace("default")
		if len(finalizers) > 0 {
			u.SetFinalizers(finalizers)
		}
		return u
	}
	newDyn := func(objs ...*unstructured.Unstructured) dynamic.Interface {
		rt := make([]runtime.Object, 0, len(objs))
		for _, o := range objs {
			rt = append(rt, o)
		}
		return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			runtime.NewScheme(),
			map[schema.GroupVersionResource]string{gvr: "WidgetList"},
			rt...,
		)
	}
	// drainInstances lists metadata and mutates through the dynamic client, so both fakes are
	// seeded from the same instances.
	newMeta := func(objs ...*unstructured.Unstructured) metadata.Interface {
		scheme := runtime.NewScheme()
		metav1.AddMetaToScheme(scheme) //nolint:errcheck // test scheme
		rt := make([]runtime.Object, 0, len(objs))
		for _, o := range objs {
			rt = append(rt, &metav1.PartialObjectMetadata{
				TypeMeta: metav1.TypeMeta{APIVersion: "apps.example.com/v1alpha1", Kind: "Widget"},
				ObjectMeta: metav1.ObjectMeta{
					Name:       o.GetName(),
					Namespace:  o.GetNamespace(),
					Finalizers: o.GetFinalizers(),
				},
			})
		}
		return metadatafake.NewSimpleMetadataClient(scheme, rt...)
	}

	t.Run("no instances is already drained", func(t *testing.T) {
		t.Parallel()
		dyn := newDyn()
		drained, err := drainInstances(context.Background(), newMeta(), dyn, gvr, false)
		require.NoError(t, err)
		require.True(t, drained)
	})

	// The fake dynamic client does not honour finalizers, so the object cannot be observed
	// sitting in Terminating here. What matters for the RGD teardown is the return value:
	// the ordered path must report not-drained so the caller requeues instead of unserving
	// the type while the instance handler still has cleanup to do.
	t.Run("issues deletes and reports not drained so the caller waits", func(t *testing.T) {
		t.Parallel()
		objs := []*unstructured.Unstructured{instance("w1", kroInstanceFinalizer), instance("w2")}
		dyn := newDyn(objs...)

		drained, err := drainInstances(context.Background(), newMeta(objs...), dyn, gvr, false)
		require.NoError(t, err)
		require.False(t, drained, "must not report drained on the same pass it issues deletes")

		cur, err := dyn.Resource(gvr).Namespace("default").Get(context.Background(), "w1", metav1.GetOptions{})
		if err == nil {
			require.False(t, cur.GetDeletionTimestamp().IsZero(), "delete should have been issued")
			require.Contains(t, cur.GetFinalizers(), kroInstanceFinalizer,
				"the ordered path leaves cleanup to kro's instance reconciler")
		} else {
			require.True(t, apierrors.IsNotFound(err))
		}
	})

	t.Run("a second pass over already-drained instances reports drained", func(t *testing.T) {
		t.Parallel()
		dyn := newDyn()
		drained, err := drainInstances(context.Background(), newMeta(), dyn, gvr, false)
		require.NoError(t, err)
		require.True(t, drained)
	})

	// The listed metadata says who has a finalizer, so instances without one must not cost a
	// read-modify-write. Verified through the fake's recorded actions.
	t.Run("force skips the read for instances with no finalizer", func(t *testing.T) {
		t.Parallel()
		objs := []*unstructured.Unstructured{instance("w1", kroInstanceFinalizer), instance("w2")}
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			runtime.NewScheme(),
			map[schema.GroupVersionResource]string{gvr: "WidgetList"},
			objs[0], objs[1],
		)

		drained, err := drainInstances(context.Background(), newMeta(objs...), dyn, gvr, true)
		require.NoError(t, err)
		require.True(t, drained)

		var gets []string
		for _, a := range dyn.Actions() {
			if a.GetVerb() == "get" {
				gets = append(gets, a.(k8stesting.GetAction).GetName())
			}
		}
		require.Equal(t, []string{"w1"}, gets,
			"only the instance carrying the finalizer should be read back")
	})

	t.Run("force releases our finalizer and does not wait", func(t *testing.T) {
		t.Parallel()
		objs := []*unstructured.Unstructured{instance("w1", kroInstanceFinalizer, "other/keep")}
		dyn := newDyn(objs...)

		drained, err := drainInstances(context.Background(), newMeta(objs...), dyn, gvr, true)
		require.NoError(t, err)
		require.True(t, drained, "a terminating workspace must not be held up")

		// Dropping the last blocking finalizer lets the fake client complete the delete; if
		// something else still holds it, ours is gone regardless.
		cur, err := dyn.Resource(gvr).Namespace("default").Get(context.Background(), "w1", metav1.GetOptions{})
		if err == nil {
			require.NotContains(t, cur.GetFinalizers(), kroInstanceFinalizer)
			require.Contains(t, cur.GetFinalizers(), "other/keep", "other finalizers are not ours to remove")
		} else {
			require.True(t, apierrors.IsNotFound(err))
		}
	})
}
