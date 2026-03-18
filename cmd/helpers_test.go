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

func TestParseEnvVars(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "single var",
			input: "KEY=value",
			want:  map[string]string{"KEY": "value"},
		},
		{
			name:  "multiple vars",
			input: "KEY1=val1,KEY2=val2,KEY3=val3",
			want:  map[string]string{"KEY1": "val1", "KEY2": "val2", "KEY3": "val3"},
		},
		{
			name:  "value with equals sign",
			input: "DSN=host=localhost port=5432",
			want:  map[string]string{"DSN": "host=localhost port=5432"},
		},
		{
			name:  "empty value",
			input: "KEY=",
			want:  map[string]string{"KEY": ""},
		},
		{
			name:  "whitespace trimmed",
			input: " KEY1 = val1 , KEY2=val2 ",
			want:  map[string]string{"KEY1": " val1", "KEY2": "val2"},
		},
		{
			name:    "missing equals",
			input:   "INVALID",
			wantErr: true,
		},
		{
			name:    "empty key",
			input:   "=value",
			wantErr: true,
		},
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "trailing comma ignored",
			input: "KEY=val,",
			want:  map[string]string{"KEY": "val"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEnvVars(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d vars, want %d", len(got), len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestPartitionResources(t *testing.T) {
	resources := []unstructured.Unstructured{
		{Object: map[string]interface{}{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]interface{}{"name": "widgets.example.com"},
			"spec": map[string]interface{}{
				"group": "example.com",
				"names": map[string]interface{}{"kind": "Widget", "plural": "widgets"},
			},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "example.com/v1",
			"kind":       "Widget",
			"metadata":   map[string]interface{}{"name": "my-widget", "namespace": "default"},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "my-deploy", "namespace": "default"},
		}},
		{Object: map[string]interface{}{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRole",
			"metadata":   map[string]interface{}{"name": "my-role"},
		}},
	}

	crds, crs, operational := partitionResources(resources)

	if len(crds) != 1 {
		t.Errorf("expected 1 CRD, got %d", len(crds))
	}
	if len(crs) != 1 {
		t.Errorf("expected 1 custom resource, got %d", len(crs))
	}
	if crs[0].GetKind() != "Widget" {
		t.Errorf("custom resource kind = %q, want Widget", crs[0].GetKind())
	}
	if len(operational) != 2 {
		t.Errorf("expected 2 operational resources, got %d", len(operational))
	}
}

func TestPartitionResources_NoCRDs(t *testing.T) {
	resources := []unstructured.Unstructured{
		{Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "deploy"},
		}},
	}

	crds, crs, operational := partitionResources(resources)
	if len(crds) != 0 {
		t.Errorf("expected 0 CRDs, got %d", len(crds))
	}
	if len(crs) != 0 {
		t.Errorf("expected 0 custom resources, got %d", len(crs))
	}
	if len(operational) != 1 {
		t.Errorf("expected 1 operational, got %d", len(operational))
	}
}

func TestDetermineOperatorNamespace(t *testing.T) {
	tests := []struct {
		name      string
		resources []unstructured.Unstructured
		want      string
	}{
		{
			name: "most common namespace wins",
			resources: []unstructured.Unstructured{
				{Object: map[string]interface{}{"metadata": map[string]interface{}{"namespace": "ns-a"}}},
				{Object: map[string]interface{}{"metadata": map[string]interface{}{"namespace": "ns-b"}}},
				{Object: map[string]interface{}{"metadata": map[string]interface{}{"namespace": "ns-a"}}},
				{Object: map[string]interface{}{"metadata": map[string]interface{}{"namespace": "ns-a"}}},
			},
			want: "ns-a",
		},
		{
			name: "cluster-scoped resources ignored",
			resources: []unstructured.Unstructured{
				{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "cluster-thing"}}},
				{Object: map[string]interface{}{"metadata": map[string]interface{}{"namespace": "the-ns"}}},
			},
			want: "the-ns",
		},
		{
			name: "empty resources",
			resources: []unstructured.Unstructured{},
			want: "",
		},
		{
			name: "all cluster-scoped",
			resources: []unstructured.Unstructured{
				{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "cr1"}}},
				{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "cr2"}}},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := determineOperatorNamespace(tt.resources)
			if got != tt.want {
				t.Errorf("determineOperatorNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyResource(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		wantField  string
	}{
		{"CRD", "CustomResourceDefinition", "CRDs"},
		{"ClusterRole", "ClusterRole", "RBAC"},
		{"ClusterRoleBinding", "ClusterRoleBinding", "RBAC"},
		{"Role", "Role", "RBAC"},
		{"RoleBinding", "RoleBinding", "RBAC"},
		{"ServiceAccount", "ServiceAccount", "RBAC"},
		{"Deployment", "Deployment", "Deployments"},
		{"Service", "Service", "Services"},
		{"ConfigMap", "ConfigMap", "Other"},
		{"PriorityClass", "PriorityClass", "Other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &bundle.Manifests{}
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       tt.kind,
					"metadata":   map[string]interface{}{"name": "test"},
				},
			}

			classifyResource(m, obj)

			var count int
			switch tt.wantField {
			case "CRDs":
				count = len(m.CRDs)
			case "RBAC":
				count = len(m.RBAC)
			case "Deployments":
				count = len(m.Deployments)
			case "Services":
				count = len(m.Services)
			case "Other":
				count = len(m.Other)
			}
			if count != 1 {
				t.Errorf("expected 1 resource in %s, got %d", tt.wantField, count)
			}
		})
	}
}

func TestClassifyResource_TLSSecret(t *testing.T) {
	m := &bundle.Manifests{}
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name": "my-tls",
				"annotations": map[string]interface{}{
					"kubectl-catalog.io/self-signed": "true",
				},
			},
		},
	}

	classifyResource(m, obj)
	if len(m.Other) != 1 {
		t.Errorf("TLS secret should go to Other, got %d in Other", len(m.Other))
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a long string", 10, "this is..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestResourceDiffers(t *testing.T) {
	makeDeployment := func(name, image, version string) *unstructured.Unstructured {
		return &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name": name,
					"annotations": map[string]interface{}{
						"kubectl-catalog.io/version": version,
					},
				},
				"spec": map[string]interface{}{
					"replicas": int64(1),
					"template": map[string]interface{}{
						"spec": map[string]interface{}{
							"containers": []interface{}{
								map[string]interface{}{
									"name":  "operator",
									"image": image,
								},
							},
						},
					},
				},
			},
		}
	}

	t.Run("same resources", func(t *testing.T) {
		a := makeDeployment("my-op", "img:v1", "1.0.0")
		b := makeDeployment("my-op", "img:v1", "1.0.0")
		if resourceDiffers(a, b) {
			t.Error("identical resources should not differ")
		}
	})

	t.Run("different image", func(t *testing.T) {
		a := makeDeployment("my-op", "img:v1", "1.0.0")
		b := makeDeployment("my-op", "img:v2", "1.0.0")
		if !resourceDiffers(a, b) {
			t.Error("different images should differ")
		}
	})

	t.Run("different version annotation", func(t *testing.T) {
		a := makeDeployment("my-op", "img:v1", "1.0.0")
		b := makeDeployment("my-op", "img:v1", "2.0.0")
		if !resourceDiffers(a, b) {
			t.Error("different version annotations should differ")
		}
	})

	t.Run("different spec", func(t *testing.T) {
		a := makeDeployment("my-op", "img:v1", "1.0.0")
		b := makeDeployment("my-op", "img:v1", "1.0.0")
		unstructured.SetNestedField(b.Object, int64(3), "spec", "replicas")
		if !resourceDiffers(a, b) {
			t.Error("different spec should differ")
		}
	})

	t.Run("non-deployment same spec", func(t *testing.T) {
		a := &unstructured.Unstructured{Object: map[string]interface{}{
			"kind":     "ConfigMap",
			"metadata": map[string]interface{}{"name": "cm"},
			"data":     map[string]interface{}{"key": "val"},
		}}
		b := &unstructured.Unstructured{Object: map[string]interface{}{
			"kind":     "ConfigMap",
			"metadata": map[string]interface{}{"name": "cm"},
			"data":     map[string]interface{}{"key": "val"},
		}}
		if resourceDiffers(a, b) {
			t.Error("identical ConfigMaps should not differ")
		}
	})

	t.Run("non-deployment different data", func(t *testing.T) {
		a := &unstructured.Unstructured{Object: map[string]interface{}{
			"kind":     "ConfigMap",
			"metadata": map[string]interface{}{"name": "cm"},
			"data":     map[string]interface{}{"key": "val1"},
		}}
		b := &unstructured.Unstructured{Object: map[string]interface{}{
			"kind":     "ConfigMap",
			"metadata": map[string]interface{}{"name": "cm"},
			"data":     map[string]interface{}{"key": "val2"},
		}}
		if !resourceDiffers(a, b) {
			t.Error("different ConfigMap data should differ")
		}
	})
}

func TestIsOCIOutput(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"oci://quay.io/org/repo:v1", true},
		{"oci://localhost:5000/repo", true},
		{"./my-directory", false},
		{"/tmp/manifests", false},
		{"", false},
		{"OCI://uppercase", false}, // case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isOCIOutput(tt.input)
			if got != tt.want {
				t.Errorf("isOCIOutput(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeOCITag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"stable-v6.1", "stable-v6.1"},
		{"stable_5.9", "stable_5.9"},
		{"v1.2.3", "v1.2.3"},
		{"channel/with/slashes", "channel-with-slashes"},
		{"has spaces", "has-spaces"},
		{"special!@#chars", "special---chars"},
		{"...leading-dots", "leading-dots"},
		{"---leading-hyphens", "leading-hyphens"},
		{"trailing---", "trailing"},
		{"", "latest"},
		{"!!!", "latest"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeOCITag(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeOCITag(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSplitImageRef(t *testing.T) {
	tests := []struct {
		ref     string
		wantRepo string
		wantTag  string
	}{
		{"quay.io/org/repo:v1.2", "quay.io/org/repo", "v1.2"},
		{"quay.io/org/repo", "quay.io/org/repo", "latest"},
		{"quay.io/org/repo@sha256:abc123", "quay.io/org/repo", "sha256:abc123"},
		{"localhost:5000/repo:tag", "localhost:5000/repo", "tag"},
		{"localhost:5000/repo", "localhost:5000/repo", "latest"},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			repo, tag := splitImageRef(tt.ref)
			if repo != tt.wantRepo {
				t.Errorf("splitImageRef(%q) repo = %q, want %q", tt.ref, repo, tt.wantRepo)
			}
			if tag != tt.wantTag {
				t.Errorf("splitImageRef(%q) tag = %q, want %q", tt.ref, tag, tt.wantTag)
			}
		})
	}
}

func TestResolveOCIRef(t *testing.T) {
	tests := []struct {
		name           string
		ref            string
		version        string // simulates --version flag
		metaVersion    string
		metaChannel    string
		want           string
	}{
		{
			name:        "explicit tag unchanged",
			ref:         "quay.io/org/repo:custom-tag",
			metaChannel: "stable-v6.1",
			want:        "quay.io/org/repo:custom-tag",
		},
		{
			name:        "no tag uses channel",
			ref:         "quay.io/org/repo",
			metaChannel: "stable-v6.1",
			want:        "quay.io/org/repo:stable-v6.1",
		},
		{
			name:        "version flag uses v-prefixed version",
			ref:         "quay.io/org/repo",
			version:     "5.9.9",
			metaVersion: "5.9.9",
			metaChannel: "stable-5.9",
			want:        "quay.io/org/repo:v5.9.9",
		},
		{
			name:        "channel with special chars sanitized",
			ref:         "quay.io/org/repo",
			metaChannel: "preview/beta",
			want:        "quay.io/org/repo:preview-beta",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore global state
			old := generateVersion
			defer func() { generateVersion = old }()
			generateVersion = tt.version

			meta := &generateMetadata{
				Version: tt.metaVersion,
				Channel: tt.metaChannel,
			}
			got := resolveOCIRef(tt.ref, meta)
			if got != tt.want {
				t.Errorf("resolveOCIRef(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func writeTestFile(t *testing.T, dir, name string, size int) {
	t.Helper()
	data := make([]byte, size)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
