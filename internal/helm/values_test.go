package helm

import (
	"testing"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDecomposeImage(t *testing.T) {
	tests := []struct {
		ref        string
		wantReg    string
		wantRepo   string
		wantTag    string
	}{
		{"quay.io/example/operator:v1.0.0", "quay.io", "example/operator", "v1.0.0"},
		{"gcr.io/project/image:latest", "gcr.io", "project/image", "latest"},
		{"nginx:1.21", "", "nginx", "1.21"},
		{"my-registry.com:5000/repo:tag", "my-registry.com:5000", "repo", "tag"},
		{"ubuntu", "", "ubuntu", "latest"},
		{"quay.io/example/op@sha256:abc123", "quay.io", "example/op", "sha256:abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			cfg := decomposeImage(tt.ref, "IfNotPresent")
			if cfg.Registry != tt.wantReg {
				t.Errorf("registry = %q, want %q", cfg.Registry, tt.wantReg)
			}
			if cfg.Repository != tt.wantRepo {
				t.Errorf("repository = %q, want %q", cfg.Repository, tt.wantRepo)
			}
			if cfg.Tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", cfg.Tag, tt.wantTag)
			}
		})
	}
}

func TestToCamelCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"kube-rbac-proxy", "kubeRbacProxy"},
		{"manager", "manager"},
		{"my_container_name", "myContainerName"},
		{"simple", "simple"},
		{"a-b-c", "aBC"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := toCamelCase(tt.input); got != tt.want {
				t.Errorf("toCamelCase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractImages(t *testing.T) {
	deps := []*unstructured.Unstructured{
		makeDeployment("controller-manager", "quay.io/example/operator:v1.0.0"),
	}

	images := extractImages(deps)
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].Key != "manager" {
		t.Errorf("key = %q, want manager", images[0].Key)
	}
	if images[0].Config.Registry != "quay.io" {
		t.Errorf("registry = %q, want quay.io", images[0].Config.Registry)
	}
	if images[0].Config.Repository != "example/operator" {
		t.Errorf("repository = %q, want example/operator", images[0].Config.Repository)
	}
	if images[0].Config.Tag != "v1.0.0" {
		t.Errorf("tag = %q, want v1.0.0", images[0].Config.Tag)
	}
}

func TestHasMonitoringResources(t *testing.T) {
	noMonitoring := []*unstructured.Unstructured{
		makeObj("v1", "ConfigMap", "config"),
	}
	if hasMonitoringResources(noMonitoring) {
		t.Error("should return false when no monitoring resources")
	}

	withMonitoring := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "monitoring.coreos.com/v1",
				"kind":       "ServiceMonitor",
				"metadata":   map[string]interface{}{"name": "sm"},
			},
		},
	}
	if !hasMonitoringResources(withMonitoring) {
		t.Error("should return true when ServiceMonitor exists")
	}
}

func TestHasWebhookResources(t *testing.T) {
	noWebhooks := []*unstructured.Unstructured{
		makeObj("v1", "ConfigMap", "config"),
	}
	if hasWebhookResources(noWebhooks) {
		t.Error("should return false when no webhook resources")
	}

	withWebhooks := []*unstructured.Unstructured{
		makeObj("admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", "vwc"),
	}
	if !hasWebhookResources(withWebhooks) {
		t.Error("should return true when ValidatingWebhookConfiguration exists")
	}
}

func TestGenerateValuesYAML_CertManagerDefault(t *testing.T) {
	g := &ChartGenerator{
		PackageName:  "test",
		Version:      "1.0.0",
		CertProvider: "cert-manager",
		Manifests:    &bundle.Manifests{},
	}
	content := generateValuesYAML(g)
	if !containsLine(content, "  enabled: true") {
		t.Error("cert-manager should default to enabled when cert provider is cert-manager")
	}

	g2 := &ChartGenerator{
		PackageName:  "test",
		Version:      "1.0.0",
		CertProvider: "self-signed",
		Manifests:    &bundle.Manifests{},
	}
	content2 := generateValuesYAML(g2)
	if !containsLine(content2, "  enabled: false") {
		t.Error("cert-manager should default to disabled when cert provider is self-signed")
	}
}

func containsLine(content, line string) bool {
	for _, l := range splitLines(content) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	return splitByNewline(s)
}

func splitByNewline(s string) []string {
	result := []string{}
	start := 0
	for i, c := range s {
		if c == '\n' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		result = append(result, s[start:])
	}
	return result
}
