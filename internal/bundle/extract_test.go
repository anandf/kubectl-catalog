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

func TestSetEnvVars(t *testing.T) {
	dep := makeDeploymentWithContainers("my-deploy", []map[string]interface{}{
		{
			"name":  "operator",
			"image": "quay.io/operator:v1",
			"env": []interface{}{
				map[string]interface{}{"name": "EXISTING", "value": "keep"},
				map[string]interface{}{"name": "REPLACE_ME", "value": "old"},
			},
		},
	})

	m := &Manifests{Deployments: []*unstructured.Unstructured{dep}}
	m.SetEnvVars(map[string]string{
		"REPLACE_ME": "new",
		"NEW_VAR":    "hello",
	})

	env := getContainerEnv(t, m.Deployments[0], 0)

	envMap := make(map[string]string)
	for _, e := range env {
		eMap := e.(map[string]interface{})
		envMap[eMap["name"].(string)] = eMap["value"].(string)
	}

	if envMap["EXISTING"] != "keep" {
		t.Errorf("EXISTING = %q, want keep", envMap["EXISTING"])
	}
	if envMap["REPLACE_ME"] != "new" {
		t.Errorf("REPLACE_ME = %q, want new", envMap["REPLACE_ME"])
	}
	if envMap["NEW_VAR"] != "hello" {
		t.Errorf("NEW_VAR = %q, want hello", envMap["NEW_VAR"])
	}
}

func TestSetEnvVars_Empty(t *testing.T) {
	dep := makeDeploymentWithContainers("my-deploy", []map[string]interface{}{
		{"name": "operator", "image": "img:v1"},
	})
	m := &Manifests{Deployments: []*unstructured.Unstructured{dep}}

	// Should be a no-op
	m.SetEnvVars(nil)
	m.SetEnvVars(map[string]string{})

	env := getContainerEnv(t, m.Deployments[0], 0)
	if len(env) != 0 {
		t.Errorf("expected no env vars, got %d", len(env))
	}
}

func TestSetEnvVars_NoContainers(t *testing.T) {
	dep := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "empty"},
			"spec":       map[string]interface{}{},
		},
	}
	m := &Manifests{Deployments: []*unstructured.Unstructured{dep}}

	// Should not panic
	m.SetEnvVars(map[string]string{"FOO": "bar"})
}

func TestSetImagePullSecrets(t *testing.T) {
	dep := makeDeploymentWithContainers("my-deploy", []map[string]interface{}{
		{"name": "operator", "image": "img:v1"},
	})
	m := &Manifests{Deployments: []*unstructured.Unstructured{dep}}

	m.SetImagePullSecrets("my-pull-secret")

	pullSecrets, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "imagePullSecrets")
	if len(pullSecrets) != 1 {
		t.Fatalf("expected 1 imagePullSecret, got %d", len(pullSecrets))
	}
	ps := pullSecrets[0].(map[string]interface{})
	if ps["name"] != "my-pull-secret" {
		t.Errorf("imagePullSecret name = %q, want my-pull-secret", ps["name"])
	}
}

func TestSetImagePullSecrets_NoDuplicate(t *testing.T) {
	dep := makeDeploymentWithContainers("my-deploy", []map[string]interface{}{
		{"name": "operator", "image": "img:v1"},
	})
	m := &Manifests{Deployments: []*unstructured.Unstructured{dep}}

	// Call twice with the same secret name
	m.SetImagePullSecrets("my-pull-secret")
	m.SetImagePullSecrets("my-pull-secret")

	pullSecrets, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "imagePullSecrets")
	if len(pullSecrets) != 1 {
		t.Errorf("expected 1 imagePullSecret (no duplicates), got %d", len(pullSecrets))
	}
}

func TestSetImagePullSecrets_MultipleDeployments(t *testing.T) {
	dep1 := makeDeploymentWithContainers("dep1", []map[string]interface{}{
		{"name": "op1", "image": "img:v1"},
	})
	dep2 := makeDeploymentWithContainers("dep2", []map[string]interface{}{
		{"name": "op2", "image": "img:v2"},
	})
	m := &Manifests{Deployments: []*unstructured.Unstructured{dep1, dep2}}

	m.SetImagePullSecrets("secret")

	for i, dep := range m.Deployments {
		pullSecrets, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "imagePullSecrets")
		if len(pullSecrets) != 1 {
			t.Errorf("deployment %d: expected 1 imagePullSecret, got %d", i, len(pullSecrets))
		}
	}
}

func TestInjectWebhookCertVolumes(t *testing.T) {
	dep := makeDeploymentWithContainers("my-operator", []map[string]interface{}{
		{
			"name":  "manager",
			"image": "img:v1",
			"ports": []interface{}{
				map[string]interface{}{"containerPort": int64(9443)},
			},
		},
	})

	webhook := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "admissionregistration.k8s.io/v1",
			"kind":       "ValidatingWebhookConfiguration",
			"metadata":   map[string]interface{}{"name": "my-webhook"},
		},
	}

	m := &Manifests{
		Deployments: []*unstructured.Unstructured{dep},
		Other:       []*unstructured.Unstructured{webhook},
	}

	injected := m.InjectWebhookCertVolumes("webhook-server-cert")
	if !injected {
		t.Fatal("InjectWebhookCertVolumes returned false, expected true")
	}

	// Verify volume was added
	volumes, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "volumes")
	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(volumes))
	}
	vol := volumes[0].(map[string]interface{})
	if vol["name"] != "webhook-server-cert" {
		t.Errorf("volume name = %q, want webhook-server-cert", vol["name"])
	}

	// Verify volume mount was added to manager container
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]interface{})
	mounts := container["volumeMounts"].([]interface{})
	if len(mounts) != 1 {
		t.Fatalf("expected 1 volume mount, got %d", len(mounts))
	}
	mount := mounts[0].(map[string]interface{})
	if mount["mountPath"] != WebhookCertMountPath {
		t.Errorf("mountPath = %q, want %q", mount["mountPath"], WebhookCertMountPath)
	}
}

func TestInjectWebhookCertVolumes_NoWebhooks(t *testing.T) {
	dep := makeDeploymentWithContainers("my-operator", []map[string]interface{}{
		{"name": "manager", "image": "img:v1"},
	})

	m := &Manifests{
		Deployments: []*unstructured.Unstructured{dep},
	}

	injected := m.InjectWebhookCertVolumes("webhook-server-cert")
	if injected {
		t.Error("InjectWebhookCertVolumes returned true for deployment without webhook signals")
	}
}

func TestInjectWebhookCertVolumes_AlreadyMounted(t *testing.T) {
	dep := makeDeploymentWithContainers("my-operator", []map[string]interface{}{
		{
			"name":  "manager",
			"image": "img:v1",
			"ports": []interface{}{
				map[string]interface{}{"containerPort": int64(9443)},
			},
			"volumeMounts": []interface{}{
				map[string]interface{}{
					"name":      "existing-cert",
					"mountPath": WebhookCertMountPath,
				},
			},
		},
	})

	webhook := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "admissionregistration.k8s.io/v1",
			"kind":       "MutatingWebhookConfiguration",
			"metadata":   map[string]interface{}{"name": "my-webhook"},
		},
	}

	m := &Manifests{
		Deployments: []*unstructured.Unstructured{dep},
		Other:       []*unstructured.Unstructured{webhook},
	}

	injected := m.InjectWebhookCertVolumes("webhook-server-cert")
	if injected {
		t.Error("InjectWebhookCertVolumes should skip deployments that already have the mount")
	}
}

func TestInjectWebhookCertVolumes_NoDeployments(t *testing.T) {
	m := &Manifests{}
	injected := m.InjectWebhookCertVolumes("webhook-server-cert")
	if injected {
		t.Error("InjectWebhookCertVolumes returned true with no deployments")
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
