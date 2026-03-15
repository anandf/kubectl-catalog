package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestResolveCatalogImage(t *testing.T) {
	tests := []struct {
		name         string
		cmdCatalog   string
		catOverride  string
		catType      string
		ocpVer       string
		want         string
		wantErr      bool
	}{
		{
			name:       "command catalog override takes precedence",
			cmdCatalog: "my-registry.example.com/catalog:v1",
			catType:    "redhat",
			want:       "my-registry.example.com/catalog:v1",
		},
		{
			name:        "global catalog override",
			catOverride: "my-registry.example.com/catalog:v2",
			catType:     "redhat",
			want:        "my-registry.example.com/catalog:v2",
		},
		{
			name:    "operatorhub does not require version",
			catType: "operatorhub",
			want:    "quay.io/operatorhubio/catalog:latest",
		},
		{
			name:    "redhat requires ocp-version",
			catType: "redhat",
			wantErr: true,
		},
		{
			name:    "redhat with version",
			catType: "redhat",
			ocpVer:  "4.20",
			want:    "registry.redhat.io/redhat/redhat-operator-index:v4.20",
		},
		{
			name:    "community with version",
			catType: "community",
			ocpVer:  "4.20",
			want:    "registry.redhat.io/redhat/community-operator-index:v4.20",
		},
		{
			name:    "certified with version",
			catType: "certified",
			ocpVer:  "4.20",
			want:    "registry.redhat.io/redhat/certified-operator-index:v4.20",
		},
		{
			name:    "unknown catalog type",
			catType: "unknown",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore global state
			oldOverride := catalogOverride
			oldType := catalogType
			oldVer := ocpVersion
			defer func() {
				catalogOverride = oldOverride
				catalogType = oldType
				ocpVersion = oldVer
			}()

			catalogOverride = tt.catOverride
			catalogType = tt.catType
			ocpVersion = tt.ocpVer

			got, err := resolveCatalogImage(tt.cmdCatalog)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveCatalogImage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequirePullSecretForRedHat(t *testing.T) {
	tests := []struct {
		name         string
		catalogImage string
		pullSecret   string
		wantErr      bool
	}{
		{
			name:         "redhat without pull secret",
			catalogImage: "registry.redhat.io/redhat/redhat-operator-index:v4.20",
			pullSecret:   "",
			wantErr:      true,
		},
		{
			name:         "redhat with pull secret",
			catalogImage: "registry.redhat.io/redhat/redhat-operator-index:v4.20",
			pullSecret:   "/path/to/secret",
			wantErr:      false,
		},
		{
			name:         "quay.io without pull secret",
			catalogImage: "quay.io/operatorhubio/catalog:latest",
			pullSecret:   "",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := pullSecretPath
			defer func() { pullSecretPath = old }()
			pullSecretPath = tt.pullSecret

			err := requirePullSecretForRedHat(tt.catalogImage)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestIsVanillaK8s(t *testing.T) {
	old := clusterType
	defer func() { clusterType = old }()

	clusterType = "k8s"
	if !isVanillaK8s() {
		t.Error("isVanillaK8s() = false for k8s, want true")
	}

	clusterType = "ocp"
	if isVanillaK8s() {
		t.Error("isVanillaK8s() = true for ocp, want false")
	}

	clusterType = "okd"
	if isVanillaK8s() {
		t.Error("isVanillaK8s() = true for okd, want false")
	}
}

func TestApplyInstallMode(t *testing.T) {
	tests := []struct {
		name      string
		modes     []bundle.InstallMode
		mode      string
		namespace string
		wantErr   bool
	}{
		{
			name:      "AllNamespaces with empty modes",
			modes:     nil,
			mode:      "AllNamespaces",
			namespace: "default",
		},
		{
			name: "supported SingleNamespace",
			modes: []bundle.InstallMode{
				{Type: "AllNamespaces", Supported: true},
				{Type: "SingleNamespace", Supported: true},
			},
			mode:      "SingleNamespace",
			namespace: "my-ns",
		},
		{
			name: "unsupported mode",
			modes: []bundle.InstallMode{
				{Type: "AllNamespaces", Supported: true},
				{Type: "SingleNamespace", Supported: false},
			},
			mode:    "SingleNamespace",
			wantErr: true,
		},
		{
			name:    "invalid mode name",
			mode:    "InvalidMode",
			wantErr: true,
		},
		{
			name:      "empty mode uses default",
			modes:     nil,
			mode:      "",
			namespace: "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &bundle.Manifests{InstallModes: tt.modes}
			err := applyInstallMode(m, tt.mode, tt.namespace)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestSanitizeFileName(t *testing.T) {
	tests := []struct {
		kind string
		name string
		want string
	}{
		{"Deployment", "my-operator", "deployment-my-operator"},
		{"ClusterRole", "my:role", "clusterrole-my-role"},
		{"CRD", "path/name", "crd-path-name"},
		{"Service", "Simple", "service-simple"},
	}

	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.name, func(t *testing.T) {
			got := sanitizeFileName(tt.kind, tt.name)
			if got != tt.want {
				t.Errorf("sanitizeFileName(%q, %q) = %q, want %q", tt.kind, tt.name, got, tt.want)
			}
		})
	}
}

func TestIsNamespacedKind(t *testing.T) {
	clusterScoped := []string{
		"CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding",
		"Namespace", "PersistentVolume", "StorageClass",
		"PriorityClass", "ValidatingWebhookConfiguration",
		"MutatingWebhookConfiguration", "APIService",
	}
	for _, kind := range clusterScoped {
		if isNamespacedKind(kind) {
			t.Errorf("isNamespacedKind(%q) = true, want false", kind)
		}
	}

	namespacedKinds := []string{
		"Deployment", "Service", "ConfigMap", "Secret",
		"ServiceAccount", "Role", "RoleBinding", "Pod",
	}
	for _, kind := range namespacedKinds {
		if !isNamespacedKind(kind) {
			t.Errorf("isNamespacedKind(%q) = false, want true", kind)
		}
	}
}

func TestSetSubjectNamespaces(t *testing.T) {
	// ClusterRoleBinding with a ServiceAccount subject missing namespace
	crb := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRoleBinding",
			"metadata":   map[string]interface{}{"name": "my-binding"},
			"subjects": []interface{}{
				map[string]interface{}{
					"kind": "ServiceAccount",
					"name": "my-sa",
				},
				map[string]interface{}{
					"kind":      "ServiceAccount",
					"name":      "other-sa",
					"namespace": "existing-ns",
				},
			},
		},
	}

	setSubjectNamespaces(crb, "target-ns")

	subjects, _, _ := unstructured.NestedSlice(crb.Object, "subjects")
	// First subject should get namespace set
	s0 := subjects[0].(map[string]interface{})
	if s0["namespace"] != "target-ns" {
		t.Errorf("subject[0].namespace = %q, want target-ns", s0["namespace"])
	}
	// Second subject already had a namespace — should keep it
	s1 := subjects[1].(map[string]interface{})
	if s1["namespace"] != "existing-ns" {
		t.Errorf("subject[1].namespace = %q, want existing-ns", s1["namespace"])
	}
}

func TestSetSubjectNamespaces_NonBinding(t *testing.T) {
	// Should be a no-op for non-binding kinds
	dep := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "my-deploy"},
		},
	}

	setSubjectNamespaces(dep, "target-ns")
	// No panic, no changes
	if dep.GetKind() != "Deployment" {
		t.Error("unexpected kind change")
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1610612736, "1.5 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatSize(tt.bytes)
			if got != tt.want {
				t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()

	// Create some files
	writeTestFile(t, dir, "a.txt", 100)
	writeTestFile(t, dir, "b.txt", 200)

	size, err := dirSize(dir)
	if err != nil {
		t.Fatalf("dirSize() error: %v", err)
	}
	if size != 300 {
		t.Errorf("dirSize() = %d, want 300", size)
	}
}

func TestDirSize_NonExistent(t *testing.T) {
	_, err := dirSize("/nonexistent/path/xyz")
	if err == nil {
		t.Error("expected error for non-existent directory")
	}
}

func writeTestFile(t *testing.T, dir, name string, size int) {
	t.Helper()
	data := make([]byte, size)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
