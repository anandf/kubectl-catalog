package applier

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/restmapper"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/anandf/kubectl-catalog/internal/state"
)

// newTestApplier creates an Applier backed by a fake dynamic client.
func newTestApplier(ns string, objects ...runtime.Object) *Applier {
	scheme := runtime.NewScheme()

	resources := []*restmapper.APIGroupResources{
		{
			Group: metav1.APIGroup{Name: "", Versions: []metav1.GroupVersionForDiscovery{{Version: "v1"}}},
			VersionedResources: map[string][]metav1.APIResource{
				"v1": {
					{Name: "namespaces", Kind: "Namespace", Namespaced: false},
					{Name: "secrets", Kind: "Secret", Namespaced: true},
					{Name: "serviceaccounts", Kind: "ServiceAccount", Namespaced: true},
					{Name: "services", Kind: "Service", Namespaced: true},
					{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
				},
			},
		},
		{
			Group: metav1.APIGroup{Name: "apps", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "apps/v1", Version: "v1"}}},
			VersionedResources: map[string][]metav1.APIResource{
				"v1": {
					{Name: "deployments", Kind: "Deployment", Namespaced: true},
				},
			},
		},
		{
			Group: metav1.APIGroup{Name: "rbac.authorization.k8s.io", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "rbac.authorization.k8s.io/v1", Version: "v1"}}},
			VersionedResources: map[string][]metav1.APIResource{
				"v1": {
					{Name: "clusterroles", Kind: "ClusterRole", Namespaced: false},
					{Name: "clusterrolebindings", Kind: "ClusterRoleBinding", Namespaced: false},
					{Name: "roles", Kind: "Role", Namespaced: true},
					{Name: "rolebindings", Kind: "RoleBinding", Namespaced: true},
				},
			},
		},
		{
			Group: metav1.APIGroup{Name: "apiextensions.k8s.io", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "apiextensions.k8s.io/v1", Version: "v1"}}},
			VersionedResources: map[string][]metav1.APIResource{
				"v1": {
					{Name: "customresourcedefinitions", Kind: "CustomResourceDefinition", Namespaced: false},
				},
			},
		},
		{
			Group: metav1.APIGroup{Name: "admissionregistration.k8s.io", Versions: []metav1.GroupVersionForDiscovery{{GroupVersion: "admissionregistration.k8s.io/v1", Version: "v1"}}},
			VersionedResources: map[string][]metav1.APIResource{
				"v1": {
					{Name: "validatingwebhookconfigurations", Kind: "ValidatingWebhookConfiguration", Namespaced: false},
					{Name: "mutatingwebhookconfigurations", Kind: "MutatingWebhookConfiguration", Namespaced: false},
				},
			},
		},
	}

	mapper := restmapper.NewDiscoveryRESTMapper(resources)

	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "namespaces"}:                                                                                      "NamespaceList",
		{Group: "", Version: "v1", Resource: "secrets"}:                                                                                         "SecretList",
		{Group: "", Version: "v1", Resource: "serviceaccounts"}:                                                                                 "ServiceAccountList",
		{Group: "", Version: "v1", Resource: "services"}:                                                                                        "ServiceList",
		{Group: "", Version: "v1", Resource: "configmaps"}:                                                                                      "ConfigMapList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:                                                                                 "DeploymentList",
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}:                                                           "ClusterRoleList",
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}:                                                    "ClusterRoleBindingList",
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}:                                                                  "RoleList",
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}:                                                           "RoleBindingList",
		{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}:                                                   "CustomResourceDefinitionList",
		{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}:                                     "ValidatingWebhookConfigurationList",
		{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}:                                       "MutatingWebhookConfigurationList",
	}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objects...)

	// Add a reactor so that Patch (server-side apply) works as a create-or-update
	// on the fake client, which doesn't natively support ApplyPatchType.
	dynClient.PrependReactor("patch", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patchAction := action.(clienttesting.PatchAction)
		var obj unstructured.Unstructured
		if err := json.Unmarshal(patchAction.GetPatch(), &obj.Object); err != nil {
			return true, nil, err
		}
		gvr := patchAction.GetResource()
		// Use the tracker to create-or-update without going through reactors again
		existing, err := dynClient.Tracker().Get(gvr, patchAction.GetNamespace(), obj.GetName())
		if err != nil {
			// Object doesn't exist — create it
			if obj.GetNamespace() == "" && patchAction.GetNamespace() != "" {
				obj.SetNamespace(patchAction.GetNamespace())
			}
			createErr := dynClient.Tracker().Create(gvr, &obj, patchAction.GetNamespace())
			return true, &obj, createErr
		}
		// Object exists — update it
		existingU := existing.(*unstructured.Unstructured)
		existingU.SetLabels(obj.GetLabels())
		existingU.SetAnnotations(obj.GetAnnotations())
		updateErr := dynClient.Tracker().Update(gvr, existingU, patchAction.GetNamespace())
		return true, existingU, updateErr
	})

	return &Applier{
		dynamicClient:          dynClient,
		mapper:                 mapper,
		namespace:              ns,
		crdEstablishTimeout:    defaultCRDEstablishTimeout,
		deploymentReadyTimeout: defaultDeploymentReadyTimeout,
	}
}

func TestPullSecretName(t *testing.T) {
	if got := PullSecretName("my-operator"); got != "my-operator-pull-secret" {
		t.Errorf("PullSecretName = %q, want %q", got, "my-operator-pull-secret")
	}
}

func TestNamespaceAndSetNamespace(t *testing.T) {
	a := newTestApplier("test-ns")
	if a.Namespace() != "test-ns" {
		t.Fatalf("Namespace() = %q, want %q", a.Namespace(), "test-ns")
	}
	a.SetNamespace("other-ns")
	if a.Namespace() != "other-ns" {
		t.Fatalf("SetNamespace did not update: got %q", a.Namespace())
	}
}

func TestDryRunFlag(t *testing.T) {
	a := newTestApplier("ns")
	if a.DryRun() {
		t.Fatal("expected DryRun() == false for default applier")
	}
	a.dryRun = true
	if !a.DryRun() {
		t.Fatal("expected DryRun() == true")
	}
}

func TestEnsureNamespace(t *testing.T) {
	a := newTestApplier("default")
	ctx := context.Background()

	// Creating a new namespace should succeed
	if err := a.EnsureNamespace(ctx, "my-ns"); err != nil {
		t.Fatalf("EnsureNamespace failed: %v", err)
	}

	// Calling again should be idempotent (namespace already exists in the fake)
	if err := a.EnsureNamespace(ctx, "my-ns"); err != nil {
		t.Fatalf("EnsureNamespace idempotent call failed: %v", err)
	}
}

func TestEnsureNamespaceDryRun(t *testing.T) {
	a := newTestApplier("default")
	a.dryRun = true
	ctx := context.Background()

	if err := a.EnsureNamespace(ctx, "dry-ns"); err != nil {
		t.Fatalf("EnsureNamespace dry-run failed: %v", err)
	}
}

func TestStampMetadata(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name": "test",
				"labels": map[string]interface{}{
					"existing": "label",
				},
			},
		},
	}

	labels := map[string]string{"app": "test", "new": "label"}
	annotations := map[string]string{"note": "hello"}

	stampMetadata(obj, labels, annotations)

	gotLabels := obj.GetLabels()
	if gotLabels["existing"] != "label" {
		t.Error("stampMetadata removed existing label")
	}
	if gotLabels["app"] != "test" || gotLabels["new"] != "label" {
		t.Error("stampMetadata did not add new labels")
	}

	gotAnn := obj.GetAnnotations()
	if gotAnn["note"] != "hello" {
		t.Error("stampMetadata did not add annotations")
	}
}

func TestStampMetadataNilMaps(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name": "bare",
			},
		},
	}

	stampMetadata(obj, map[string]string{"k": "v"}, map[string]string{"a": "b"})

	if obj.GetLabels()["k"] != "v" {
		t.Error("stampMetadata failed on nil labels map")
	}
	if obj.GetAnnotations()["a"] != "b" {
		t.Error("stampMetadata failed on nil annotations map")
	}
}

func TestIsCRDEstablished(t *testing.T) {
	tests := []struct {
		name       string
		conditions []interface{}
		want       bool
	}{
		{
			name: "established",
			conditions: []interface{}{
				map[string]interface{}{"type": "Established", "status": "True"},
			},
			want: true,
		},
		{
			name: "not established",
			conditions: []interface{}{
				map[string]interface{}{"type": "Established", "status": "False"},
			},
			want: false,
		},
		{
			name:       "no conditions",
			conditions: nil,
			want:       false,
		},
		{
			name: "other conditions only",
			conditions: []interface{}{
				map[string]interface{}{"type": "NamesAccepted", "status": "True"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "apiextensions.k8s.io/v1",
				"kind":       "CustomResourceDefinition",
				"metadata":   map[string]interface{}{"name": "test.example.com"},
			}}
			if tt.conditions != nil {
				unstructured.SetNestedSlice(obj.Object, tt.conditions, "status", "conditions")
			}
			if got := isCRDEstablished(obj); got != tt.want {
				t.Errorf("isCRDEstablished() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsDeploymentReady(t *testing.T) {
	tests := []struct {
		name       string
		obj        map[string]interface{}
		wantReady  bool
		wantReason string
	}{
		{
			name: "ready with all replicas",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": int64(2)},
				"status": map[string]interface{}{
					"replicas":        int64(2),
					"readyReplicas":   int64(2),
					"updatedReplicas": int64(2),
					"conditions": []interface{}{
						map[string]interface{}{"type": "Available", "status": "True"},
					},
				},
			},
			wantReady: true,
		},
		{
			name: "not ready - replicas pending",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": int64(3)},
				"status": map[string]interface{}{
					"replicas":        int64(3),
					"readyReplicas":   int64(1),
					"updatedReplicas": int64(3),
					"conditions": []interface{}{
						map[string]interface{}{"type": "Available", "status": "True"},
					},
				},
			},
			wantReady: false,
		},
		{
			name: "failed rollout",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": int64(1)},
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{
							"type":    "Progressing",
							"status":  "False",
							"message": "deadline exceeded",
						},
					},
				},
			},
			wantReady:  false,
			wantReason: "rollout failed: deadline exceeded",
		},
		{
			name: "no status conditions",
			obj: map[string]interface{}{
				"spec":   map[string]interface{}{"replicas": int64(1)},
				"status": map[string]interface{}{},
			},
			wantReady:  false,
			wantReason: "no status conditions",
		},
		{
			name: "default replicas when spec.replicas omitted",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{},
				"status": map[string]interface{}{
					"replicas":        int64(1),
					"readyReplicas":   int64(1),
					"updatedReplicas": int64(1),
					"conditions": []interface{}{
						map[string]interface{}{"type": "Available", "status": "True"},
					},
				},
			},
			wantReady: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata":   map[string]interface{}{"name": "test"},
			}}
			for k, v := range tt.obj {
				obj.Object[k] = v
			}

			ready, reason := isDeploymentReady(obj)
			if ready != tt.wantReady {
				t.Errorf("isDeploymentReady() ready = %v, want %v", ready, tt.wantReady)
			}
			if tt.wantReason != "" && reason != tt.wantReason {
				t.Errorf("isDeploymentReady() reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestSetDefaultSubjectNamespaces(t *testing.T) {
	a := newTestApplier("operator-ns")

	// ClusterRoleBinding with SA missing namespace
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRoleBinding",
			"metadata":   map[string]interface{}{"name": "test-binding"},
			"subjects": []interface{}{
				map[string]interface{}{"kind": "ServiceAccount", "name": "my-sa"},
				map[string]interface{}{"kind": "ServiceAccount", "name": "other-sa", "namespace": "kept-ns"},
				map[string]interface{}{"kind": "User", "name": "admin"},
			},
		},
	}

	a.setDefaultSubjectNamespaces(obj)

	subjects, _, _ := unstructured.NestedSlice(obj.Object, "subjects")
	sa0 := subjects[0].(map[string]interface{})
	if sa0["namespace"] != "operator-ns" {
		t.Errorf("expected namespace to be injected, got %v", sa0["namespace"])
	}

	sa1 := subjects[1].(map[string]interface{})
	if sa1["namespace"] != "kept-ns" {
		t.Errorf("existing namespace was overwritten: got %v", sa1["namespace"])
	}

	user := subjects[2].(map[string]interface{})
	if _, ok := user["namespace"]; ok {
		t.Error("non-SA subject should not get namespace injected")
	}
}

func TestSetDefaultSubjectNamespacesNonBinding(t *testing.T) {
	a := newTestApplier("ns")

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]interface{}{"name": "cm"},
		},
	}

	// Should be a no-op for non-binding kinds
	a.setDefaultSubjectNamespaces(obj)
}

func TestEnsurePullSecret(t *testing.T) {
	a := newTestApplier("test-ns")
	ctx := context.Background()

	pullSecretData := []byte(`{"auths":{"registry.example.com":{"auth":"dGVzdDp0ZXN0"}}}`)

	if err := a.EnsurePullSecret(ctx, pullSecretData, "my-pkg"); err != nil {
		t.Fatalf("EnsurePullSecret failed: %v", err)
	}
}

func TestEnsurePullSecretDryRun(t *testing.T) {
	a := newTestApplier("test-ns")
	a.dryRun = true
	ctx := context.Background()

	if err := a.EnsurePullSecret(ctx, []byte(`{}`), "my-pkg"); err != nil {
		t.Fatalf("EnsurePullSecret dry-run failed: %v", err)
	}
}

func TestDeletePullSecret(t *testing.T) {
	a := newTestApplier("test-ns")
	ctx := context.Background()

	// Deleting a non-existent secret should succeed (not found is OK)
	if err := a.DeletePullSecret(ctx, "nonexistent-pkg"); err != nil {
		t.Fatalf("DeletePullSecret failed for non-existent: %v", err)
	}
}

func TestDeletePullSecretDryRun(t *testing.T) {
	a := newTestApplier("test-ns")
	a.dryRun = true
	ctx := context.Background()

	if err := a.DeletePullSecret(ctx, "pkg"); err != nil {
		t.Fatalf("DeletePullSecret dry-run failed: %v", err)
	}
}

func TestDeleteNamespaceProtected(t *testing.T) {
	a := newTestApplier("test-ns")
	ctx := context.Background()

	for _, ns := range []string{"default", "kube-system", "kube-public", "kube-node-lease"} {
		if err := a.DeleteNamespace(ctx, ns); err != nil {
			t.Errorf("DeleteNamespace(%q) should silently skip protected ns, got: %v", ns, err)
		}
	}
}

func TestDeleteNamespaceNotFound(t *testing.T) {
	a := newTestApplier("test-ns")
	ctx := context.Background()

	// Non-existent, non-protected namespace should not error
	if err := a.DeleteNamespace(ctx, "gone-ns"); err != nil {
		t.Fatalf("DeleteNamespace for missing ns failed: %v", err)
	}
}

func TestApplyDryRun(t *testing.T) {
	a := newTestApplier("test-ns")
	a.dryRun = true
	ctx := context.Background()

	manifests := &bundle.Manifests{
		RBAC: []*unstructured.Unstructured{
			makeUnstructured("rbac.authorization.k8s.io/v1", "ClusterRole", "test-role", ""),
		},
		Deployments: []*unstructured.Unstructured{
			makeUnstructured("apps/v1", "Deployment", "test-deploy", "test-ns"),
		},
		Services: []*unstructured.Unstructured{
			makeUnstructured("v1", "Service", "test-svc", "test-ns"),
		},
		Other: []*unstructured.Unstructured{
			makeUnstructured("v1", "ConfigMap", "test-cm", "test-ns"),
		},
	}

	ic := &InstallContext{
		PackageName: "test-pkg",
		Version:     "1.0.0",
		Channel:     "stable",
		BundleName:  "test-bundle",
		BundleImage: "example.com/test:v1",
		CatalogRef:  "example.com/catalog:latest",
	}

	if err := a.Apply(ctx, manifests, ic); err != nil {
		t.Fatalf("Apply dry-run failed: %v", err)
	}
}

func TestDeleteResourcesDryRun(t *testing.T) {
	a := newTestApplier("test-ns")
	a.dryRun = true
	ctx := context.Background()

	resources := []unstructured.Unstructured{
		*makeUnstructured("v1", "ConfigMap", "cm1", "test-ns"),
		*makeUnstructured("v1", "Service", "svc1", "test-ns"),
	}

	if err := a.DeleteResources(ctx, resources); err != nil {
		t.Fatalf("DeleteResources dry-run failed: %v", err)
	}
}

func TestDeleteResourcesNotFound(t *testing.T) {
	a := newTestApplier("test-ns")
	ctx := context.Background()

	resources := []unstructured.Unstructured{
		*makeUnstructured("v1", "ConfigMap", "nonexistent", "test-ns"),
	}

	// Deleting non-existent resources should succeed (not found is ignored)
	if err := a.DeleteResources(ctx, resources); err != nil {
		t.Fatalf("DeleteResources failed for not-found resources: %v", err)
	}
}

func TestCleanupWebhookConfigurationsEmpty(t *testing.T) {
	a := newTestApplier("test-ns")
	ctx := context.Background()

	cleaned, err := a.CleanupWebhookConfigurations(ctx, "test-ns")
	if err != nil {
		t.Fatalf("CleanupWebhookConfigurations failed: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned, got %d", cleaned)
	}
}

func TestApplyStampsTrackingMetadata(t *testing.T) {
	a := newTestApplier("test-ns")
	a.dryRun = true
	ctx := context.Background()

	cm := makeUnstructured("v1", "ConfigMap", "tracked-cm", "test-ns")
	manifests := &bundle.Manifests{
		Other: []*unstructured.Unstructured{cm},
	}

	ic := &InstallContext{
		PackageName: "my-pkg",
		Version:     "2.0.0",
		Channel:     "alpha",
		BundleName:  "my-bundle",
		BundleImage: "img:v2",
		CatalogRef:  "cat:latest",
	}

	if err := a.Apply(ctx, manifests, ic); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Verify tracking labels were stamped
	labels := cm.GetLabels()
	if labels[state.LabelManagedBy] != state.ManagedByValue {
		t.Errorf("missing managed-by label")
	}
	if labels[state.LabelPackage] != "my-pkg" {
		t.Errorf("missing package label, got: %v", labels[state.LabelPackage])
	}
}

func TestCrdGroupVersionResource(t *testing.T) {
	gvr := crdGroupVersionResource()
	if gvr.Group != "apiextensions.k8s.io" || gvr.Version != "v1" || gvr.Resource != "customresourcedefinitions" {
		t.Errorf("unexpected CRD GVR: %v", gvr)
	}
}

func TestDeploymentGroupVersionResource(t *testing.T) {
	gvr := deploymentGroupVersionResource()
	if gvr.Group != "apps" || gvr.Version != "v1" || gvr.Resource != "deployments" {
		t.Errorf("unexpected Deployment GVR: %v", gvr)
	}
}

func TestOptionsApplied(t *testing.T) {
	a := newTestApplier("ns")
	a.dryRun = true
	a.noWait = true
	a.crdEstablishTimeout = 10
	a.deploymentReadyTimeout = 20

	if !a.dryRun || !a.noWait {
		t.Error("options not applied")
	}
	if a.crdEstablishTimeout != 10 || a.deploymentReadyTimeout != 20 {
		t.Error("timeout options not applied")
	}
}

// makeUnstructured creates a minimal unstructured object for testing.
func makeUnstructured(apiVersion, kind, name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata": map[string]interface{}{
				"name": name,
			},
		},
	}
	if namespace != "" {
		obj.Object["metadata"].(map[string]interface{})["namespace"] = namespace
	}
	return obj
}
