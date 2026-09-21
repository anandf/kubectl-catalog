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
		PackageName:  "test.json-operator",
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
				Description: "A test.json operator for unit testing",
				Version:     "1.0.0",
				Keywords:    []string{"test.json", "operator"},
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

	saData, err := os.ReadFile(filepath.Join(dir, "templates", "serviceaccount.yaml"))
	if err != nil {
		t.Fatalf("reading serviceaccount.yaml: %v", err)
	}
	saContent := string(saData)
	if !strings.Contains(saContent, "imagePullSecrets") {
		t.Error("serviceaccount.yaml should contain imagePullSecrets for pull secret")
	}
	if !strings.Contains(saContent, ".Values.pullSecret.create") {
		t.Error("serviceaccount.yaml imagePullSecrets should be conditional on .Values.pullSecret.create")
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
		PackageName: "crd-test.json",
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
		PackageName: "webhook-test.json",
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

func TestGenerate_DeploymentTemplate(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName:  "my-operator",
		Version:      "1.0.0",
		CertProvider: "self-signed",
		InstallMode:  "AllNamespaces",
		Namespace:    "operators",
		Manifests: &bundle.Manifests{
			Deployments: []*unstructured.Unstructured{
				makeDeploymentFull("controller-manager", "quay.io/example/operator:v1.0.0"),
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "templates", "deployment.yaml"))
	if err != nil {
		t.Fatalf("reading deployment.yaml: %v", err)
	}
	content := string(data)

	t.Run("no status field", func(t *testing.T) {
		if strings.Contains(content, "status:") {
			t.Error("deployment template should not contain status field")
		}
	})

	t.Run("no duplicate annotations", func(t *testing.T) {
		count := strings.Count(content, "annotations:")
		// One for the existing pod template annotation, one from the Helm
		// conditional — the word "annotations" appears in the conditional
		// too, but there should be exactly one YAML annotations: key.
		lines := strings.Split(content, "\n")
		yamlAnnotations := 0
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "annotations:" {
				yamlAnnotations++
			}
		}
		if yamlAnnotations > 1 {
			t.Errorf("expected at most 1 YAML annotations: key in pod template, found %d (total 'annotations:' occurrences: %d)", yamlAnnotations, count)
		}
	})

	t.Run("no duplicate env keys", func(t *testing.T) {
		lines := strings.Split(content, "\n")
		envKeys := 0
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "env:" {
				envKeys++
			}
		}
		if envKeys > 1 {
			t.Errorf("expected exactly 1 env: key per container, found %d", envKeys)
		}
	})

	t.Run("args not first in container", func(t *testing.T) {
		lines := strings.Split(content, "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "- args:" || strings.HasPrefix(trimmed, "- args: ") {
				t.Error("args should not be the first field (with list marker) in the container")
			}
		}
	})

	t.Run("containers is a list", func(t *testing.T) {
		idx := strings.Index(content, "containers:")
		if idx < 0 {
			t.Fatal("no containers: found")
		}
		after := content[idx:]
		lines := strings.Split(after, "\n")
		for _, line := range lines[1:] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "{{") {
				continue
			}
			if !strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "#") {
				t.Errorf("first content line after containers: should be a list item (- ...); got %q", trimmed)
			}
			break
		}
	})

	t.Run("WATCH_NAMESPACE in env list", func(t *testing.T) {
		if !strings.Contains(content, "WATCH_NAMESPACE") {
			t.Error("deployment template should contain WATCH_NAMESPACE")
		}
	})

	t.Run("env var overrides from values.yaml", func(t *testing.T) {
		if !strings.Contains(content, `hasKey .Values.env "ENABLE_CONVERSION_WEBHOOK"`) {
			t.Error("deployment template should check .Values.env for ENABLE_CONVERSION_WEBHOOK override")
		}
		if !strings.Contains(content, `hasKey .Values.env "OPERATOR_NAME"`) {
			t.Error("deployment template should check .Values.env for OPERATOR_NAME override")
		}
	})

	t.Run("env range excludes bundle keys", func(t *testing.T) {
		if !strings.Contains(content, `has $key`) {
			t.Error("deployment template should filter bundle env keys from .Values.env range")
		}
	})
}

func TestGenerate_MonitoringTemplate(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName: "monitoring-test.json",
		Version:     "1.0.0",
		Namespace:   "default",
		Manifests: &bundle.Manifests{
			Other: []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"apiVersion": "monitoring.coreos.com/v1",
						"kind":       "ServiceMonitor",
						"metadata":   map[string]interface{}{"name": "my-service-monitor"},
						"spec": map[string]interface{}{
							"endpoints": []interface{}{
								map[string]interface{}{"port": "metrics"},
							},
						},
					},
				},
				{
					Object: map[string]interface{}{
						"apiVersion": "monitoring.coreos.com/v1",
						"kind":       "PrometheusRule",
						"metadata":   map[string]interface{}{"name": "my-prometheus-rule"},
						"spec":       map[string]interface{}{},
					},
				},
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "templates", "monitoring.yaml"))
	if err != nil {
		t.Fatalf("reading monitoring.yaml: %v", err)
	}
	content := string(data)

	t.Run("capabilities check for ServiceMonitor", func(t *testing.T) {
		if !strings.Contains(content, `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1/ServiceMonitor"`) {
			t.Error("monitoring template should check .Capabilities.APIVersions for ServiceMonitor")
		}
	})

	t.Run("capabilities check for PrometheusRule", func(t *testing.T) {
		if !strings.Contains(content, `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1/PrometheusRule"`) {
			t.Error("monitoring template should check .Capabilities.APIVersions for PrometheusRule")
		}
	})

	t.Run("monitoring enabled flag", func(t *testing.T) {
		if !strings.Contains(content, ".Values.monitoring.enabled") {
			t.Error("monitoring template should check .Values.monitoring.enabled")
		}
	})

	t.Run("uses and conditional", func(t *testing.T) {
		if !strings.Contains(content, "{{- if and .Values.monitoring.enabled (.Capabilities.APIVersions.Has") {
			t.Error("monitoring template should combine both conditions with 'and'")
		}
	})
}

func TestGenerate_ChartNameOverride(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName: "some-operator",
		ChartName:   "my-custom-chart",
		Version:     "1.0.0",
		Namespace:   "default",
		Manifests: &bundle.Manifests{
			Deployments: []*unstructured.Unstructured{
				makeDeployment("controller-manager", "quay.io/example/operator:v1.0.0"),
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	chartData, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		t.Fatalf("reading Chart.yaml: %v", err)
	}
	if !strings.Contains(string(chartData), "name: my-custom-chart") {
		t.Errorf("Chart.yaml should use overridden name 'my-custom-chart', got:\n%s", string(chartData))
	}

	valuesData, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatalf("reading values.yaml: %v", err)
	}
	if !strings.Contains(string(valuesData), "# Default values for my-custom-chart.") {
		t.Error("values.yaml should reference the overridden chart name")
	}

	helpersData, err := os.ReadFile(filepath.Join(dir, "templates", "_helpers.tpl"))
	if err != nil {
		t.Fatalf("reading _helpers.tpl: %v", err)
	}
	if !strings.Contains(string(helpersData), "my-custom-chart") {
		t.Error("_helpers.tpl should reference the overridden chart name")
	}

	deployData, err := os.ReadFile(filepath.Join(dir, "templates", "deployment.yaml"))
	if err != nil {
		t.Fatalf("reading deployment.yaml: %v", err)
	}
	if !strings.Contains(string(deployData), "my-custom-chart") {
		t.Error("deployment template should reference the overridden chart name in labels/helpers")
	}
}

func TestGenerate_CRDsOnly(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName:   "my-operator",
		Version:       "1.0.0",
		Namespace:     "default",
		SkipTemplates: true,
		Manifests: &bundle.Manifests{
			CRDs: []*unstructured.Unstructured{
				makeObj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "widgets.example.com"),
				makeObj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "gadgets.example.com"),
			},
			Deployments: []*unstructured.Unstructured{
				makeDeployment("controller-manager", "quay.io/example/operator:v1.0.0"),
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	// CRDs directory and files should exist
	for _, crdName := range []string{"widgets.example.com.yaml", "gadgets.example.com.yaml"} {
		path := filepath.Join(dir, "crds", crdName)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected CRD file %s to exist", crdName)
		}
	}

	// Templates directory should NOT exist
	if _, err := os.Stat(filepath.Join(dir, "templates")); !os.IsNotExist(err) {
		t.Error("templates/ directory should not exist when SkipTemplates is true")
	}

	// Chart.yaml and .helmignore should still exist
	for _, f := range []string{"Chart.yaml", "values.yaml", ".helmignore"} {
		if _, err := os.Stat(filepath.Join(dir, f)); os.IsNotExist(err) {
			t.Errorf("expected %s to exist", f)
		}
	}

	// values.yaml should be minimal (CRDs-only comment)
	data, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatalf("reading values.yaml: %v", err)
	}
	if !strings.Contains(string(data), "only CRDs") {
		t.Error("values.yaml should indicate this chart contains only CRDs")
	}
}

func TestGenerate_TemplatesOnly(t *testing.T) {
	dir := t.TempDir()

	g := &ChartGenerator{
		PackageName:  "my-operator",
		Version:      "1.0.0",
		CertProvider: "self-signed",
		InstallMode:  "AllNamespaces",
		Namespace:    "operators",
		SkipCRDs:     true,
		Manifests: &bundle.Manifests{
			CRDs: []*unstructured.Unstructured{
				makeObj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "widgets.example.com"),
			},
			Deployments: []*unstructured.Unstructured{
				makeDeployment("controller-manager", "quay.io/example/operator:v1.0.0"),
			},
			RBAC: []*unstructured.Unstructured{
				makeObj("v1", "ServiceAccount", "controller-manager"),
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate() error: %v", err)
	}

	// Templates directory and files should exist
	for _, f := range []string{"_helpers.tpl", "NOTES.txt", "deployment.yaml"} {
		path := filepath.Join(dir, "templates", f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected template file %s to exist", f)
		}
	}

	// CRDs directory should NOT exist
	if _, err := os.Stat(filepath.Join(dir, "crds")); !os.IsNotExist(err) {
		t.Error("crds/ directory should not exist when SkipCRDs is true")
	}

	// NOTES.txt should not mention CRD update instructions
	data, err := os.ReadFile(filepath.Join(dir, "templates", "NOTES.txt"))
	if err != nil {
		t.Fatalf("reading NOTES.txt: %v", err)
	}
	if strings.Contains(string(data), "kubectl apply -f <chart-dir>/crds/") {
		t.Error("NOTES.txt should not contain CRD update instructions when CRDs are excluded")
	}

	// values.yaml should have full content (not the minimal CRD-only version)
	valData, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatalf("reading values.yaml: %v", err)
	}
	if !strings.Contains(string(valData), "replicaCount:") {
		t.Error("values.yaml should have full template values when SkipTemplates is false")
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

func makeDeploymentFull(name, image string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": name},
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"annotations": map[string]interface{}{
							"kubectl.kubernetes.io/default-container": "manager",
						},
					},
					"spec": map[string]interface{}{
						"serviceAccountName": name,
						"containers": []interface{}{
							map[string]interface{}{
								"name":    "manager",
								"image":   image,
								"args":    []interface{}{"--leader-elect"},
								"command": []interface{}{"/usr/local/bin/manager"},
								"env": []interface{}{
									map[string]interface{}{
										"name":  "OPERATOR_NAME",
										"value": "my-operator",
									},
									map[string]interface{}{
										"name":  "ENABLE_CONVERSION_WEBHOOK",
										"value": "true",
									},
									map[string]interface{}{
										"name":  "WATCH_NAMESPACE",
										"value": "operators",
									},
								},
							},
						},
					},
				},
			},
			"status": map[string]interface{}{},
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
