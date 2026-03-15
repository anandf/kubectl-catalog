package bundle

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSupportsInstallMode(t *testing.T) {
	m := &Manifests{
		InstallModes: []InstallMode{
			{Type: "AllNamespaces", Supported: true},
			{Type: "SingleNamespace", Supported: true},
			{Type: "OwnNamespace", Supported: false},
			{Type: "MultiNamespace", Supported: false},
		},
	}

	tests := []struct {
		mode string
		want bool
	}{
		{"AllNamespaces", true},
		{"SingleNamespace", true},
		{"OwnNamespace", false},
		{"MultiNamespace", false},
		{"Unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got := m.SupportsInstallMode(tt.mode)
			if got != tt.want {
				t.Errorf("SupportsInstallMode(%q) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func TestSupportsInstallMode_Empty(t *testing.T) {
	m := &Manifests{}
	if m.SupportsInstallMode("AllNamespaces") {
		t.Error("empty install modes should not support any mode")
	}
}

func TestDefaultInstallMode(t *testing.T) {
	tests := []struct {
		name  string
		modes []InstallMode
		want  string
	}{
		{
			name:  "empty modes defaults to AllNamespaces",
			modes: nil,
			want:  "AllNamespaces",
		},
		{
			name: "prefers AllNamespaces",
			modes: []InstallMode{
				{Type: "AllNamespaces", Supported: true},
				{Type: "SingleNamespace", Supported: true},
			},
			want: "AllNamespaces",
		},
		{
			name: "falls back to SingleNamespace",
			modes: []InstallMode{
				{Type: "AllNamespaces", Supported: false},
				{Type: "SingleNamespace", Supported: true},
				{Type: "OwnNamespace", Supported: true},
			},
			want: "SingleNamespace",
		},
		{
			name: "falls back to OwnNamespace",
			modes: []InstallMode{
				{Type: "AllNamespaces", Supported: false},
				{Type: "SingleNamespace", Supported: false},
				{Type: "OwnNamespace", Supported: true},
			},
			want: "OwnNamespace",
		},
		{
			name: "all unsupported falls back to AllNamespaces",
			modes: []InstallMode{
				{Type: "AllNamespaces", Supported: false},
				{Type: "SingleNamespace", Supported: false},
				{Type: "OwnNamespace", Supported: false},
			},
			want: "AllNamespaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manifests{InstallModes: tt.modes}
			got := m.DefaultInstallMode()
			if got != tt.want {
				t.Errorf("DefaultInstallMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetWatchNamespace(t *testing.T) {
	dep := makeDeploymentWithContainers("my-deploy", []map[string]interface{}{
		{
			"name":  "operator",
			"image": "quay.io/operator:v1",
			"env": []interface{}{
				map[string]interface{}{"name": "FOO", "value": "bar"},
				map[string]interface{}{"name": "WATCH_NAMESPACE", "value": "old-ns"},
			},
		},
	})

	m := &Manifests{Deployments: []*unstructured.Unstructured{dep}}

	// Set to a specific namespace
	m.SetWatchNamespace("my-namespace")
	env := getContainerEnv(t, m.Deployments[0], 0)

	// Should have FOO and WATCH_NAMESPACE (old one removed, new one added)
	found := false
	for _, e := range env {
		eMap := e.(map[string]interface{})
		if eMap["name"] == "WATCH_NAMESPACE" {
			if eMap["value"] != "my-namespace" {
				t.Errorf("WATCH_NAMESPACE = %q, want %q", eMap["value"], "my-namespace")
			}
			found = true
		}
	}
	if !found {
		t.Error("WATCH_NAMESPACE not found in env")
	}

	// Set to empty (AllNamespaces)
	m.SetWatchNamespace("")
	env = getContainerEnv(t, m.Deployments[0], 0)
	for _, e := range env {
		eMap := e.(map[string]interface{})
		if eMap["name"] == "WATCH_NAMESPACE" {
			if eMap["value"] != "" {
				t.Errorf("WATCH_NAMESPACE = %q, want empty string", eMap["value"])
			}
		}
	}
}

func TestSetWatchNamespace_NoExistingEnv(t *testing.T) {
	dep := makeDeploymentWithContainers("my-deploy", []map[string]interface{}{
		{
			"name":  "operator",
			"image": "quay.io/operator:v1",
		},
	})

	m := &Manifests{Deployments: []*unstructured.Unstructured{dep}}
	m.SetWatchNamespace("test-ns")

	env := getContainerEnv(t, m.Deployments[0], 0)
	if len(env) != 1 {
		t.Fatalf("env length = %d, want 1", len(env))
	}
	eMap := env[0].(map[string]interface{})
	if eMap["name"] != "WATCH_NAMESPACE" || eMap["value"] != "test-ns" {
		t.Errorf("expected WATCH_NAMESPACE=test-ns, got %v", eMap)
	}
}

func TestExtract(t *testing.T) {
	// Create a minimal bundle directory with a CRD and a deployment
	bundleDir := t.TempDir()
	manifestDir := filepath.Join(bundleDir, "manifests")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}

	crdYAML := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: myresources.example.com
spec:
  group: example.com
  names:
    kind: MyResource
    plural: myresources
  scope: Namespaced`

	deployYAML := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-operator
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: operator
        image: quay.io/my-operator:v1`

	os.WriteFile(filepath.Join(manifestDir, "crd.yaml"), []byte(crdYAML), 0o644)
	os.WriteFile(filepath.Join(manifestDir, "deployment.yaml"), []byte(deployYAML), 0o644)

	manifests, err := Extract(bundleDir)
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	if len(manifests.CRDs) != 1 {
		t.Errorf("CRDs count = %d, want 1", len(manifests.CRDs))
	}
	if len(manifests.Deployments) != 1 {
		t.Errorf("Deployments count = %d, want 1", len(manifests.Deployments))
	}
}

func TestExtract_FallbackToRoot(t *testing.T) {
	// When there's no manifests/ subdirectory, Extract falls back to the root
	bundleDir := t.TempDir()

	saYAML := `apiVersion: v1
kind: ServiceAccount
metadata:
  name: my-sa`

	os.WriteFile(filepath.Join(bundleDir, "sa.yaml"), []byte(saYAML), 0o644)

	manifests, err := Extract(bundleDir)
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	if len(manifests.RBAC) != 1 {
		t.Errorf("RBAC count = %d, want 1", len(manifests.RBAC))
	}
}

// Helper functions

func makeDeploymentWithContainers(name string, containers []map[string]interface{}) *unstructured.Unstructured {
	ifaces := make([]interface{}, len(containers))
	for i, c := range containers {
		ifaces[i] = c
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": name},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": ifaces,
					},
				},
			},
		},
	}
}

func getContainerEnv(t *testing.T, dep *unstructured.Unstructured, idx int) []interface{} {
	t.Helper()
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	if idx >= len(containers) {
		t.Fatalf("container index %d out of range (have %d)", idx, len(containers))
	}
	c := containers[idx].(map[string]interface{})
	env, _ := c["env"].([]interface{})
	return env
}
