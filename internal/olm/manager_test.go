package olm

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func newFakeDynamicClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	gvrToListKind := map[k8sschema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "namespaces"}:                                       "NamespaceList",
		{Group: "", Version: "v1", Resource: "secrets"}:                                          "SecretList",
		{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}:          "SubscriptionList",
		{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "catalogsources"}:         "CatalogSourceList",
		{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions"}: "ClusterServiceVersionList",
		{Group: "operators.coreos.com", Version: "v1", Resource: "operatorgroups"}:               "OperatorGroupList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objects...)

	client.PrependReactor("patch", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patchAction := action.(clienttesting.PatchAction)
		var obj unstructured.Unstructured
		if err := json.Unmarshal(patchAction.GetPatch(), &obj.Object); err != nil {
			return true, nil, err
		}
		if obj.GetNamespace() == "" {
			obj.SetNamespace(patchAction.GetNamespace())
		}
		return true, &obj, nil
	})

	return client
}

func newTestManager(ns string, objects ...runtime.Object) *Manager {
	return &Manager{
		dynamicClient:   newFakeDynamicClient(objects...),
		discoveryClient: fakeDiscoveryWithOLM(),
		namespace:       ns,
		dryRun:          false,
	}
}

func TestInstall_CreatesResources(t *testing.T) {
	m := newTestManager("test-ns")

	err := m.Install(context.Background(), InstallOptions{
		PackageName:  "my-operator",
		CatalogImage: "quay.io/test:v1",
		CatalogType:  "redhat",
		Channel:      "stable",
		InstallMode:  "AllNamespaces",
		NoWait:       true,
	})
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// Verify resources were created by inspecting the fake client's actions
	fakeClient := m.dynamicClient.(*dynamicfake.FakeDynamicClient)
	actions := fakeClient.Actions()

	var patchedResources []string
	for _, action := range actions {
		if pa, ok := action.(clienttesting.PatchAction); ok {
			patchedResources = append(patchedResources, pa.GetResource().Resource+"/"+pa.GetName())
		}
	}

	expectedPatches := map[string]bool{
		"namespaces/test-ns":                    false,
		"operatorgroups/kubectl-catalog-og":     false,
		"catalogsources/kubectl-catalog-redhat": false,
		"subscriptions/my-operator":             false,
	}

	for _, res := range patchedResources {
		if _, ok := expectedPatches[res]; ok {
			expectedPatches[res] = true
		}
	}

	for res, found := range expectedPatches {
		if !found {
			t.Errorf("expected patch for %q but it was not found", res)
		}
	}
}

func TestInstall_SkipsExistingOperatorGroup(t *testing.T) {
	existingOG := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1",
			"kind":       "OperatorGroup",
			"metadata": map[string]interface{}{
				"name":      "existing-og",
				"namespace": "test-ns",
			},
			"spec": map[string]interface{}{},
		},
	}

	m := newTestManager("test-ns", existingOG)

	err := m.Install(context.Background(), InstallOptions{
		PackageName:  "my-operator",
		CatalogImage: "quay.io/test:v1",
		CatalogType:  "community",
		Channel:      "alpha",
		NoWait:       true,
	})
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// Should not have created a second OperatorGroup
	list, _ := m.dynamicClient.Resource(OperatorGroupGVR()).Namespace("test-ns").List(
		context.Background(), metav1.ListOptions{},
	)
	if len(list.Items) != 1 {
		t.Errorf("expected 1 OperatorGroup, got %d", len(list.Items))
	}
	if list.Items[0].GetName() != "existing-og" {
		t.Errorf("expected existing-og to be preserved, got %q", list.Items[0].GetName())
	}
}

func TestInstall_SubscriptionAlreadyExists(t *testing.T) {
	existingSub := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      "my-operator",
				"namespace": "test-ns",
			},
			"spec": map[string]interface{}{
				"channel": "stable",
			},
		},
	}

	m := newTestManager("test-ns", existingSub)

	err := m.Install(context.Background(), InstallOptions{
		PackageName:  "my-operator",
		CatalogImage: "quay.io/test:v1",
		CatalogType:  "redhat",
		Channel:      "stable",
		Force:        false,
		NoWait:       true,
	})
	if err == nil {
		t.Fatal("expected error when Subscription exists and force=false")
	}
}

func TestInstall_ForceRecreateSub(t *testing.T) {
	existingSub := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      "my-operator",
				"namespace": "test-ns",
			},
			"spec": map[string]interface{}{
				"channel": "stable",
			},
		},
	}

	m := newTestManager("test-ns", existingSub)

	err := m.Install(context.Background(), InstallOptions{
		PackageName:  "my-operator",
		CatalogImage: "quay.io/test:v1",
		CatalogType:  "redhat",
		Channel:      "preview",
		Force:        true,
		NoWait:       true,
	})
	if err != nil {
		t.Fatalf("Install with --force failed: %v", err)
	}
}

func TestInstall_DryRun(t *testing.T) {
	m := newTestManager("test-ns")
	m.dryRun = true

	err := m.Install(context.Background(), InstallOptions{
		PackageName:  "my-operator",
		CatalogImage: "quay.io/test:v1",
		CatalogType:  "redhat",
		Channel:      "stable",
		NoWait:       true,
	})
	if err != nil {
		t.Fatalf("dry-run Install failed: %v", err)
	}
}

func TestInstall_OLMNotInstalled(t *testing.T) {
	m := &Manager{
		dynamicClient:   newFakeDynamicClient(),
		discoveryClient: fakeDiscoveryWithoutOLM(),
		namespace:       "test-ns",
	}

	err := m.Install(context.Background(), InstallOptions{
		PackageName: "my-operator",
		Channel:     "stable",
		NoWait:      true,
	})
	if err == nil {
		t.Fatal("expected error when OLM is not installed")
	}
}

func TestUninstall_DeletesResources(t *testing.T) {
	sub := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      "my-operator",
				"namespace": "test-ns",
				"labels": map[string]interface{}{
					"kubectl-catalog.io/package":   "my-operator",
					"app.kubernetes.io/managed-by": "kubectl-catalog",
				},
			},
			"spec": map[string]interface{}{
				"channel": "stable",
			},
			"status": map[string]interface{}{
				"installedCSV": "my-operator.v1.0.0",
			},
		},
	}

	csv := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "ClusterServiceVersion",
			"metadata": map[string]interface{}{
				"name":      "my-operator.v1.0.0",
				"namespace": "test-ns",
			},
		},
	}

	cs := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "CatalogSource",
			"metadata": map[string]interface{}{
				"name":      "kubectl-catalog-redhat",
				"namespace": "test-ns",
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
				},
			},
		},
	}

	og := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1",
			"kind":       "OperatorGroup",
			"metadata": map[string]interface{}{
				"name":      "kubectl-catalog-og",
				"namespace": "test-ns",
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
				},
			},
		},
	}

	m := newTestManager("test-ns", sub, csv, cs, og)

	err := m.Uninstall(context.Background(), "my-operator")
	if err != nil {
		t.Fatalf("Uninstall failed: %v", err)
	}
}

func TestFindSubscription_ByName(t *testing.T) {
	sub := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      "my-operator",
				"namespace": "test-ns",
			},
		},
	}

	m := newTestManager("test-ns", sub)

	found, err := m.findSubscription(context.Background(), "my-operator")
	if err != nil {
		t.Fatalf("findSubscription failed: %v", err)
	}
	if found.GetName() != "my-operator" {
		t.Errorf("expected 'my-operator', got %q", found.GetName())
	}
}

func TestFindSubscription_NotFound(t *testing.T) {
	m := newTestManager("test-ns")

	_, err := m.findSubscription(context.Background(), "missing-operator")
	if err == nil {
		t.Fatal("expected error for missing subscription")
	}
}
