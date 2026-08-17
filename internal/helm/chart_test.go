package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGenerate_DirectoryStructure(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName:  "test-operator",
		Version:      "1.0.0",
		Channel:      "stable",
		CatalogRef:   "registry.example.com/catalog:v4.20",
		CertProvider: "self-signed",
		InstallMode:  "AllNamespaces",
		Namespace:    "operators",
		Manifests: &bundle.Manifests{
			CRDs: []*unstructured.Unstructured{
				makeObj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "tests.example.com"),
			},
			Deployments: []*unstructured.Unstructured{
				makeDeployment("controller-manager", "quay.io/example/operator:v1.0.0"),
			},
			RBAC: []*unstructured.Unstructured{
				makeObj("v1", "ServiceAccount", "controller-manager"),
				makeObj("rbac.authorization.k8s.io/v1", "ClusterRole", "manager-role"),
			},
			Services: []*unstructured.Unstructured{
				makeObj("v1", "Service", "controller-manager-metrics"),
			},
			CSVMetadata: &bundle.CSVMetadata{
				DisplayName: "Test Operator",
				Description: "A test operator for unit testing",
				Version:     "1.0.0",
				Keywords:    []string{"test", "operator"},
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	expectedFiles := []string{
		"Chart.yaml",
		"values.yaml",
		"crds/tests.example.com.yaml",
		"templates/_helpers.tpl",
		"templates/NOTES.txt",
		"templates/serviceaccount.yaml",
		"templates/rbac.yaml",
		"templates/deployment.yaml",
		"templates/service.yaml",
	}

	for _, f := range expectedFiles {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", f)
		}
	}
}

func TestGenerate_ChartYAMLContent(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName:  "my-operator",
		Version:      "2.1.0",
		Channel:      "alpha",
		CatalogRef:   "registry.example.com/catalog:v4.20",
		CertProvider: "self-signed",
		Namespace:    "default",
		Manifests: &bundle.Manifests{
			CSVMetadata: &bundle.CSVMetadata{
				Description: "My great operator",
				Keywords:    []string{"database", "sql"},
				Maintainers: []bundle.CSVMaintainer{
					{Name: "Alice", Email: "alice@example.com"},
				},
				Provider: bundle.CSVProvider{URL: "https://example.com"},
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		t.Fatalf("reading Chart.yaml: %v", err)
	}
	content := string(data)

	checks := []struct {
		desc     string
		contains string
	}{
		{"apiVersion", "apiVersion: v2"},
		{"name", "name: my-operator"},
		{"version", "version: 2.1.0"},
		{"appVersion", "appVersion: 2.1.0"},
		{"type", "type: application"},
		{"description", "description: My great operator"},
		{"keyword", "database"},
		{"maintainer name", "Alice"},
		{"maintainer email", "alice@example.com"},
		{"home", "home: https://example.com"},
	}

	for _, tc := range checks {
		if !strings.Contains(content, tc.contains) {
			t.Errorf("Chart.yaml missing %s (%q)", tc.desc, tc.contains)
		}
	}
}

func TestGenerate_ValuesYAMLContent(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName:  "my-operator",
		Version:      "1.0.0",
		CertProvider: "cert-manager",
		InstallMode:  "SingleNamespace",
		Namespace:    "ops",
		Manifests: &bundle.Manifests{
			Deployments: []*unstructured.Unstructured{
				makeDeployment("controller-manager", "quay.io/example/operator:v1.0.0"),
			},
			Other: []*unstructured.Unstructured{
				makeObj("admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", "my-webhook"),
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatalf("reading values.yaml: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "certManager:\n  enabled: true") {
		t.Error("values.yaml should have certManager.enabled: true when cert-provider is cert-manager")
	}
	if !strings.Contains(content, "webhooks:\n  enabled: true") {
		t.Error("values.yaml should have webhooks.enabled: true when webhook configs exist")
	}
	if !strings.Contains(content, `installMode: "SingleNamespace"`) {
		t.Error("values.yaml should reflect the install mode")
	}
}

func TestGenerate_CRDsAreRaw(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName: "crd-test",
		Version:     "1.0.0",
		Namespace:   "default",
		Manifests: &bundle.Manifests{
			CRDs: []*unstructured.Unstructured{
				makeObj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "widgets.example.com"),
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "crds", "widgets.example.com.yaml"))
	if err != nil {
		t.Fatalf("reading CRD file: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "{{") {
		t.Error("CRD files should not contain template expressions")
	}
	if !strings.Contains(content, "widgets.example.com") {
		t.Error("CRD file should contain the CRD name")
	}
}

func TestGenerate_WebhookTemplates(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName: "webhook-test",
		Version:     "1.0.0",
		Namespace:   "default",
		Manifests: &bundle.Manifests{
			Services: []*unstructured.Unstructured{
				makeObj("v1", "Service", "webhook-service"),
			},
			Other: []*unstructured.Unstructured{
				makeObj("admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", "my-vwc"),
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	// Should have webhook config, cert-manager, and self-signed cert templates
	for _, name := range []string{"webhook-configs.yaml", "cert-manager.yaml", "self-signed-certs.yaml"} {
		path := filepath.Join(dir, "templates", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected template %s to exist", name)
		}
	}

	// cert-manager template should have conditional
	data, err := os.ReadFile(filepath.Join(dir, "templates", "cert-manager.yaml"))
	if err != nil {
		t.Fatalf("reading cert-manager.yaml: %v", err)
	}
	if !strings.Contains(string(data), ".Values.certManager.enabled") {
		t.Error("cert-manager.yaml should be conditional on .Values.certManager.enabled")
	}

	// self-signed template should use genSignedCert
	data, err = os.ReadFile(filepath.Join(dir, "templates", "self-signed-certs.yaml"))
	if err != nil {
		t.Fatalf("reading self-signed-certs.yaml: %v", err)
	}
	if !strings.Contains(string(data), "genSignedCert") {
		t.Error("self-signed-certs.yaml should use Helm's genSignedCert")
	}
	if !strings.Contains(string(data), "lookup") {
		t.Error("self-signed-certs.yaml should use lookup to preserve certs across upgrades")
	}
}

// --- Test helpers ---

func makeObj(apiVersion, kind, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata":   map[string]interface{}{"name": name},
		},
	}
}

func makeDeployment(name, image string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": name},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"serviceAccountName": name,
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "manager",
								"image": image,
							},
						},
					},
				},
			},
		},
	}
}
