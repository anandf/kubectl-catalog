package helm

import (
	"strings"
	"testing"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDecomposeImage(t *testing.T) {
	tests := []struct {
		ref      string
		wantReg  string
		wantRepo string
		wantTag  string
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
		PackageName:  "test.json",
		Version:      "1.0.0",
		CertProvider: "cert-manager",
		Manifests:    &bundle.Manifests{},
	}
	content := generateValuesYAML(g)
	if !containsLine(content, "  enabled: true") {
		t.Error("cert-manager should default to enabled when cert provider is cert-manager")
	}

	g2 := &ChartGenerator{
		PackageName:  "test.json",
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

func TestParseMirrorPrefix(t *testing.T) {
	tests := []struct {
		prefix   string
		wantReg  string
		wantRepo string
	}{
		{"quay.io/anjoseph", "quay.io", "anjoseph"},
		{"mirror.example.com:5000/myorg", "mirror.example.com:5000", "myorg"},
		{"localhost:5000/test", "localhost:5000", "test"},
		{"myorg", "", "myorg"},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			reg, repo := parseMirrorPrefix(tt.prefix)
			if reg != tt.wantReg {
				t.Errorf("registry = %q, want %q", reg, tt.wantReg)
			}
			if repo != tt.wantRepo {
				t.Errorf("repoPrefix = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

func TestMirrorImageRef(t *testing.T) {
	tests := []struct {
		name   string
		ref    string
		mirror string
		want   string
	}{
		{
			"digest image with registry",
			"registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abc123",
			"quay.io/anjoseph",
			"quay.io/anjoseph/argocd-rhel9@sha256:abc123",
		},
		{
			"tagged image with registry",
			"registry.redhat.io/openshift-gitops-1/operator:v1.0.0",
			"quay.io/anjoseph",
			"quay.io/anjoseph/operator:v1.0.0",
		},
		{
			"nested path",
			"registry.redhat.io/a/b/c/myimage:latest",
			"mirror.example.com/org",
			"mirror.example.com/org/myimage:latest",
		},
		{
			"no tag",
			"registry.redhat.io/openshift-gitops-1/argocd-rhel9",
			"quay.io/anjoseph",
			"quay.io/anjoseph/argocd-rhel9",
		},
		{
			"port in mirror registry",
			"registry.redhat.io/org/image:v1",
			"localhost:5000/test",
			"localhost:5000/test/image:v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mirrorImageRef(tt.ref, tt.mirror)
			if got != tt.want {
				t.Errorf("mirrorImageRef(%q, %q) = %q, want %q", tt.ref, tt.mirror, got, tt.want)
			}
		})
	}
}

func TestMirrorImageConfig(t *testing.T) {
	tests := []struct {
		name   string
		img    imageConfig
		mirror string
		wantR  string
		wantP  string
	}{
		{
			"standard registry image",
			imageConfig{Registry: "registry.redhat.io", Repository: "openshift-gitops-1/operator", Tag: "v1.0.0"},
			"quay.io/anjoseph",
			"quay.io",
			"anjoseph/operator",
		},
		{
			"no registry in original",
			imageConfig{Registry: "", Repository: "myorg/myimage", Tag: "latest"},
			"quay.io/anjoseph",
			"quay.io",
			"anjoseph/myimage",
		},
		{
			"single segment repository",
			imageConfig{Registry: "docker.io", Repository: "nginx", Tag: "1.21"},
			"mirror.example.com/org",
			"mirror.example.com",
			"org/nginx",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mirrorImageConfig(tt.img, tt.mirror)
			if got.Registry != tt.wantR {
				t.Errorf("registry = %q, want %q", got.Registry, tt.wantR)
			}
			if got.Repository != tt.wantP {
				t.Errorf("repository = %q, want %q", got.Repository, tt.wantP)
			}
			if got.Tag != tt.img.Tag {
				t.Errorf("tag changed: %q -> %q", tt.img.Tag, got.Tag)
			}
		})
	}
}

func TestReconstructImageRef(t *testing.T) {
	tests := []struct {
		name string
		img  imageConfig
		want string
	}{
		{
			"tagged with registry",
			imageConfig{Registry: "quay.io", Repository: "example/operator", Tag: "v1.0.0"},
			"quay.io/example/operator:v1.0.0",
		},
		{
			"digest with registry",
			imageConfig{Registry: "registry.redhat.io", Repository: "org/image", Tag: "sha256:abc123"},
			"registry.redhat.io/org/image@sha256:abc123",
		},
		{
			"no registry",
			imageConfig{Repository: "nginx", Tag: "1.21"},
			"nginx:1.21",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reconstructImageRef(tt.img)
			if got != tt.want {
				t.Errorf("reconstructImageRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateValuesYAML_Mirror(t *testing.T) {
	g := &ChartGenerator{
		PackageName:  "my-operator",
		Version:      "1.0.0",
		CertProvider: "self-signed",
		InstallMode:  "AllNamespaces",
		Namespace:    "operators",
		MirrorPrefix: "quay.io/anjoseph",
		CheckImage: func(imageRef string) bool {
			return false
		},
		Manifests: &bundle.Manifests{
			Deployments: []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata":   map[string]interface{}{"name": "controller-manager"},
						"spec": map[string]interface{}{
							"replicas": int64(1),
							"template": map[string]interface{}{
								"spec": map[string]interface{}{
									"containers": []interface{}{
										map[string]interface{}{
											"name":  "manager",
											"image": "registry.redhat.io/openshift-gitops-1/operator:v1.0.0",
											"env": []interface{}{
												map[string]interface{}{
													"name":  "RELATED_IMAGE_ARGOCD",
													"value": "registry.redhat.io/openshift-gitops-1/argocd-rhel9@sha256:abc123",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	content := generateValuesYAML(g)

	if !strings.Contains(content, "registry: quay.io") {
		t.Error("mirrored image should have registry: quay.io")
	}
	if !strings.Contains(content, "repository: anjoseph/operator") {
		t.Error("mirrored image should have repository: anjoseph/operator")
	}
	if !strings.Contains(content, "quay.io/anjoseph/argocd-rhel9@sha256:abc123") {
		t.Error("mirrored operatorImageEnv should use mirror prefix")
	}
	if strings.Contains(content, "registry.redhat.io") {
		t.Error("original registry should not appear in mirrored values")
	}
}

func TestGenerateValuesYAML_MirrorPartial(t *testing.T) {
	g := &ChartGenerator{
		PackageName:  "my-operator",
		Version:      "1.0.0",
		CertProvider: "self-signed",
		Namespace:    "default",
		MirrorPrefix: "quay.io/anjoseph",
		CheckImage: func(imageRef string) bool {
			return strings.Contains(imageRef, "available-image")
		},
		Manifests: &bundle.Manifests{
			Deployments: []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata":   map[string]interface{}{"name": "controller-manager"},
						"spec": map[string]interface{}{
							"replicas": int64(1),
							"template": map[string]interface{}{
								"spec": map[string]interface{}{
									"containers": []interface{}{
										map[string]interface{}{
											"name":  "manager",
											"image": "registry.redhat.io/org/available-image:v1",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	content := generateValuesYAML(g)

	if !strings.Contains(content, "registry: registry.redhat.io") {
		t.Error("available image should keep original registry")
	}
	if !strings.Contains(content, "repository: org/available-image") {
		t.Error("available image should keep original repository")
	}
}
