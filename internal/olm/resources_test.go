package olm

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCatalogSourceName_KnownType(t *testing.T) {
	for _, ct := range []string{"redhat", "community", "certified", "operatorhub"} {
		name := CatalogSourceName(ct, "")
		expected := "kubectl-catalog-" + ct
		if name != expected {
			t.Errorf("CatalogSourceName(%q) = %q, want %q", ct, name, expected)
		}
	}
}

func TestCatalogSourceName_CustomImage(t *testing.T) {
	name := CatalogSourceName("", "my-registry.io/custom-catalog:v1")
	if !strings.HasPrefix(name, "kubectl-catalog-") {
		t.Errorf("expected prefix 'kubectl-catalog-', got %q", name)
	}
	if len(name) != len("kubectl-catalog-")+8 {
		t.Errorf("expected 8-char hash suffix, got name %q (len %d)", name, len(name))
	}
}

func TestCatalogSourceName_Deterministic(t *testing.T) {
	a := CatalogSourceName("", "quay.io/test:v1")
	b := CatalogSourceName("", "quay.io/test:v1")
	if a != b {
		t.Errorf("CatalogSourceName should be deterministic: %q != %q", a, b)
	}
}

func TestNewCatalogSource(t *testing.T) {
	cs := NewCatalogSource("test-cs", "default", "quay.io/test:v1", "Test Catalog", "")

	if cs.GetKind() != "CatalogSource" {
		t.Errorf("expected kind CatalogSource, got %q", cs.GetKind())
	}
	if cs.GetAPIVersion() != "operators.coreos.com/v1alpha1" {
		t.Errorf("unexpected apiVersion: %q", cs.GetAPIVersion())
	}
	if cs.GetName() != "test-cs" {
		t.Errorf("unexpected name: %q", cs.GetName())
	}

	image, _, _ := unstructured.NestedString(cs.Object, "spec", "image")
	if image != "quay.io/test:v1" {
		t.Errorf("unexpected spec.image: %q", image)
	}

	labels := cs.GetLabels()
	if labels["app.kubernetes.io/managed-by"] != "kubectl-catalog" {
		t.Error("missing managed-by label")
	}

	// No secrets when pullSecretName is empty
	_, found, _ := unstructured.NestedSlice(cs.Object, "spec", "secrets")
	if found {
		t.Error("expected no spec.secrets when pullSecretName is empty")
	}
}

func TestNewCatalogSource_WithPullSecret(t *testing.T) {
	cs := NewCatalogSource("test-cs", "default", "quay.io/test:v1", "Test", "my-secret")

	secrets, found, _ := unstructured.NestedSlice(cs.Object, "spec", "secrets")
	if !found || len(secrets) != 1 {
		t.Fatalf("expected 1 secret, got %v", secrets)
	}
	if secrets[0] != "my-secret" {
		t.Errorf("expected secret 'my-secret', got %v", secrets[0])
	}
}

func TestNewOperatorGroup_AllNamespaces(t *testing.T) {
	og := NewOperatorGroup("test-og", "default", nil)

	if og.GetKind() != "OperatorGroup" {
		t.Errorf("expected kind OperatorGroup, got %q", og.GetKind())
	}
	if og.GetAPIVersion() != "operators.coreos.com/v1" {
		t.Errorf("unexpected apiVersion: %q", og.GetAPIVersion())
	}

	targets, found, _ := unstructured.NestedSlice(og.Object, "spec", "targetNamespaces")
	if found && len(targets) > 0 {
		t.Error("AllNamespaces OperatorGroup should not have targetNamespaces")
	}
}

func TestNewOperatorGroup_SingleNamespace(t *testing.T) {
	og := NewOperatorGroup("test-og", "my-ns", []string{"my-ns"})

	targets, found, _ := unstructured.NestedSlice(og.Object, "spec", "targetNamespaces")
	if !found || len(targets) != 1 {
		t.Fatalf("expected 1 targetNamespace, got %v", targets)
	}
	if targets[0] != "my-ns" {
		t.Errorf("expected targetNamespace 'my-ns', got %v", targets[0])
	}
}

func TestNewSubscription(t *testing.T) {
	sub := NewSubscription("my-operator", "operators", "stable", "my-catalog", "operators", "")

	if sub.GetKind() != "Subscription" {
		t.Errorf("expected kind Subscription, got %q", sub.GetKind())
	}

	channel, _, _ := unstructured.NestedString(sub.Object, "spec", "channel")
	if channel != "stable" {
		t.Errorf("expected channel 'stable', got %q", channel)
	}

	source, _, _ := unstructured.NestedString(sub.Object, "spec", "source")
	if source != "my-catalog" {
		t.Errorf("expected source 'my-catalog', got %q", source)
	}

	approval, _, _ := unstructured.NestedString(sub.Object, "spec", "installPlanApproval")
	if approval != "Automatic" {
		t.Errorf("expected installPlanApproval 'Automatic', got %q", approval)
	}

	labels := sub.GetLabels()
	if labels["kubectl-catalog.io/package"] != "my-operator" {
		t.Error("missing package label")
	}

	_, found, _ := unstructured.NestedString(sub.Object, "spec", "startingCSV")
	if found {
		t.Error("startingCSV should not be set when empty")
	}
}

func TestNewSubscription_WithStartingCSV(t *testing.T) {
	sub := NewSubscription("my-operator", "operators", "stable", "my-catalog", "operators", "my-operator.v1.2.3")

	csv, found, _ := unstructured.NestedString(sub.Object, "spec", "startingCSV")
	if !found || csv != "my-operator.v1.2.3" {
		t.Errorf("expected startingCSV 'my-operator.v1.2.3', got %q (found=%v)", csv, found)
	}
}

func TestGVRHelpers(t *testing.T) {
	tests := []struct {
		name     string
		group    string
		resource string
	}{
		{"SubscriptionGVR", "operators.coreos.com", "subscriptions"},
		{"CatalogSourceGVR", "operators.coreos.com", "catalogsources"},
		{"OperatorGroupGVR", "operators.coreos.com", "operatorgroups"},
		{"CSVGVR", "operators.coreos.com", "clusterserviceversions"},
	}

	gvrs := map[string]struct{ Group, Resource string }{
		"SubscriptionGVR":  {SubscriptionGVR().Group, SubscriptionGVR().Resource},
		"CatalogSourceGVR": {CatalogSourceGVR().Group, CatalogSourceGVR().Resource},
		"OperatorGroupGVR": {OperatorGroupGVR().Group, OperatorGroupGVR().Resource},
		"CSVGVR":           {CSVGVR().Group, CSVGVR().Resource},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gvr := gvrs[tt.name]
			if gvr.Group != tt.group {
				t.Errorf("expected group %q, got %q", tt.group, gvr.Group)
			}
			if gvr.Resource != tt.resource {
				t.Errorf("expected resource %q, got %q", tt.resource, gvr.Resource)
			}
		})
	}
}
