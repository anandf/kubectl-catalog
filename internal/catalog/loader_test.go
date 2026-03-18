package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromDirectoryJSON(t *testing.T) {
	dir := t.TempDir()
	configsDir := filepath.Join(dir, "configs", "test-operator")
	if err := os.MkdirAll(configsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `{"schema":"olm.package","name":"test-operator","defaultChannel":"stable"}
{"schema":"olm.channel","name":"stable","package":"test-operator","entries":[{"name":"test-operator.v1.0.0"}]}
{"schema":"olm.bundle","name":"test-operator.v1.0.0","package":"test-operator","image":"img:v1","properties":[{"type":"olm.package","value":{"version":"1.0.0"}}]}
`
	if err := os.WriteFile(filepath.Join(configsDir, "catalog.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fbc, err := LoadFromDirectory(dir)
	if err != nil {
		t.Fatalf("LoadFromDirectory() error: %v", err)
	}

	if len(fbc.Packages) != 1 {
		t.Errorf("expected 1 package, got %d", len(fbc.Packages))
	}
	if fbc.Packages[0].Name != "test-operator" {
		t.Errorf("expected package name test-operator, got %s", fbc.Packages[0].Name)
	}
	if fbc.Packages[0].DefaultChannel != "stable" {
		t.Errorf("expected default channel stable, got %s", fbc.Packages[0].DefaultChannel)
	}

	if len(fbc.Channels) != 1 {
		t.Errorf("expected 1 channel, got %d", len(fbc.Channels))
	}
	if len(fbc.Bundles) != 1 {
		t.Errorf("expected 1 bundle, got %d", len(fbc.Bundles))
	}
	if fbc.Bundles[0].Image != "img:v1" {
		t.Errorf("expected bundle image img:v1, got %s", fbc.Bundles[0].Image)
	}
}

func TestLoadFromDirectoryMultiLineJSON(t *testing.T) {
	dir := t.TempDir()

	// Multi-line JSON (not newline-delimited) — json.Decoder handles this natively
	content := `{
  "schema": "olm.package",
  "name": "multi-line-op",
  "defaultChannel": "alpha"
}
{
  "schema": "olm.channel",
  "name": "alpha",
  "package": "multi-line-op",
  "entries": [{"name": "multi-line-op.v1.0.0"}]
}
`
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fbc, err := LoadFromDirectory(dir)
	if err != nil {
		t.Fatalf("LoadFromDirectory() error: %v", err)
	}

	if len(fbc.Packages) != 1 {
		t.Errorf("expected 1 package, got %d", len(fbc.Packages))
	}
	if fbc.Packages[0].Name != "multi-line-op" {
		t.Errorf("expected package name multi-line-op, got %s", fbc.Packages[0].Name)
	}
	if len(fbc.Channels) != 1 {
		t.Errorf("expected 1 channel, got %d", len(fbc.Channels))
	}
}

func TestLoadFromDirectoryBracesInStrings(t *testing.T) {
	dir := t.TempDir()

	// JSON with braces inside string values — previously broke brace-depth counter
	content := `{"schema":"olm.package","name":"brace-op","defaultChannel":"stable","description":"use { and } in config"}
`
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fbc, err := LoadFromDirectory(dir)
	if err != nil {
		t.Fatalf("LoadFromDirectory() error: %v", err)
	}

	if len(fbc.Packages) != 1 {
		t.Errorf("expected 1 package, got %d", len(fbc.Packages))
	}
	if fbc.Packages[0].Description != "use { and } in config" {
		t.Errorf("unexpected description: %s", fbc.Packages[0].Description)
	}
}

func TestGetPackage(t *testing.T) {
	fbc := &FBC{
		Packages: []Package{
			{Name: "a"},
			{Name: "b"},
		},
	}

	if p := fbc.GetPackage("a"); p == nil || p.Name != "a" {
		t.Errorf("GetPackage(a) failed")
	}
	if p := fbc.GetPackage("nonexistent"); p != nil {
		t.Errorf("GetPackage(nonexistent) should return nil")
	}
}

func TestGetBundle(t *testing.T) {
	fbc := &FBC{
		Bundles: []Bundle{
			{Name: "b1", Package: "pkg1"},
			{Name: "b2", Package: "pkg2"},
		},
	}

	if b := fbc.GetBundle("b1"); b == nil || b.Package != "pkg1" {
		t.Errorf("GetBundle(b1) failed")
	}
	if b := fbc.GetBundle("nonexistent"); b != nil {
		t.Errorf("GetBundle(nonexistent) should return nil")
	}
}

func TestChannelsForPackage(t *testing.T) {
	fbc := &FBC{
		Channels: []Channel{
			{Name: "stable", Package: "pkg1"},
			{Name: "alpha", Package: "pkg1"},
			{Name: "stable", Package: "pkg2"},
		},
	}

	channels := fbc.ChannelsForPackage("pkg1")
	if len(channels) != 2 {
		t.Errorf("expected 2 channels for pkg1, got %d", len(channels))
	}

	channels = fbc.ChannelsForPackage("pkg2")
	if len(channels) != 1 {
		t.Errorf("expected 1 channel for pkg2, got %d", len(channels))
	}

	channels = fbc.ChannelsForPackage("nonexistent")
	if len(channels) != 0 {
		t.Errorf("expected 0 channels for nonexistent, got %d", len(channels))
	}
}

func TestParseGVKQuery(t *testing.T) {
	tests := []struct {
		input       string
		wantGroup   string
		wantVersion string
		wantKind    string
	}{
		{"argoproj.io/v1alpha1/ArgoCD", "argoproj.io", "v1alpha1", "ArgoCD"},
		{"argoproj.io_v1alpha1_ArgoCD", "argoproj.io", "v1alpha1", "ArgoCD"},
		{"argoproj.io/ArgoCD", "argoproj.io", "", "ArgoCD"},
		{"argoproj.io_ArgoCD", "argoproj.io", "", "ArgoCD"},
		{"ArgoCD", "", "", "ArgoCD"},
		{"logging.openshift.io/v1/ClusterLogging", "logging.openshift.io", "v1", "ClusterLogging"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			q := ParseGVKQuery(tt.input)
			if q.Group != tt.wantGroup {
				t.Errorf("Group = %q, want %q", q.Group, tt.wantGroup)
			}
			if q.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", q.Version, tt.wantVersion)
			}
			if q.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", q.Kind, tt.wantKind)
			}
		})
	}
}

func mustMarshalJSON(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func TestFindGVKProviders(t *testing.T) {
	fbc := &FBC{
		Packages: []Package{
			{Name: "gitops-operator", DefaultChannel: "latest", Description: "GitOps operator"},
			{Name: "argocd-operator", DefaultChannel: "alpha", Description: "Argo CD operator"},
			{Name: "logging-operator", DefaultChannel: "stable", Description: "Logging operator"},
		},
		Bundles: []Bundle{
			{
				Name: "gitops-operator.v1.0.0", Package: "gitops-operator",
				Properties: []Property{
					{Type: "olm.gvk", Value: mustMarshalJSON(map[string]string{"group": "argoproj.io", "version": "v1beta1", "kind": "ArgoCD"})},
					{Type: "olm.gvk", Value: mustMarshalJSON(map[string]string{"group": "argoproj.io", "version": "v1alpha1", "kind": "ArgoCD"})},
				},
			},
			{
				Name: "gitops-operator.v1.1.0", Package: "gitops-operator",
				Properties: []Property{
					{Type: "olm.gvk", Value: mustMarshalJSON(map[string]string{"group": "argoproj.io", "version": "v1beta1", "kind": "ArgoCD"})},
				},
			},
			{
				Name: "argocd-operator.v0.5.0", Package: "argocd-operator",
				Properties: []Property{
					{Type: "olm.gvk", Value: mustMarshalJSON(map[string]string{"group": "argoproj.io", "version": "v1alpha1", "kind": "ArgoCD"})},
				},
			},
			{
				Name: "logging-operator.v5.0.0", Package: "logging-operator",
				Properties: []Property{
					{Type: "olm.gvk", Value: mustMarshalJSON(map[string]string{"group": "logging.openshift.io", "version": "v1", "kind": "ClusterLogging"})},
				},
			},
		},
	}
	fbc.BuildIndexes()

	t.Run("group/kind matches any version", func(t *testing.T) {
		results := fbc.FindGVKProviders(GVKQuery{Group: "argoproj.io", Kind: "ArgoCD"})
		if len(results) != 2 {
			t.Fatalf("expected 2 providers, got %d", len(results))
		}
		// Should be sorted: argocd-operator, gitops-operator
		if results[0].PackageName != "argocd-operator" {
			t.Errorf("results[0] = %q, want argocd-operator", results[0].PackageName)
		}
		if results[1].PackageName != "gitops-operator" {
			t.Errorf("results[1] = %q, want gitops-operator", results[1].PackageName)
		}
		// gitops-operator should have 2 distinct GVKs (v1alpha1 and v1beta1)
		if len(results[1].MatchedGVKs) != 2 {
			t.Errorf("expected 2 matched GVKs for gitops-operator, got %d", len(results[1].MatchedGVKs))
		}
	})

	t.Run("full GVK narrows to specific version", func(t *testing.T) {
		results := fbc.FindGVKProviders(GVKQuery{Group: "argoproj.io", Version: "v1beta1", Kind: "ArgoCD"})
		if len(results) != 1 {
			t.Fatalf("expected 1 provider, got %d", len(results))
		}
		if results[0].PackageName != "gitops-operator" {
			t.Errorf("got %q, want gitops-operator", results[0].PackageName)
		}
	})

	t.Run("kind-only search", func(t *testing.T) {
		results := fbc.FindGVKProviders(GVKQuery{Kind: "ArgoCD"})
		if len(results) != 2 {
			t.Fatalf("expected 2 providers, got %d", len(results))
		}
	})

	t.Run("no match", func(t *testing.T) {
		results := fbc.FindGVKProviders(GVKQuery{Group: "nonexistent.io", Kind: "Foo"})
		if len(results) != 0 {
			t.Errorf("expected 0 providers, got %d", len(results))
		}
	})

	t.Run("case insensitive group", func(t *testing.T) {
		results := fbc.FindGVKProviders(GVKQuery{Group: "ARGOPROJ.IO", Kind: "ArgoCD"})
		if len(results) != 2 {
			t.Fatalf("expected 2 providers, got %d", len(results))
		}
	})

	t.Run("case sensitive kind", func(t *testing.T) {
		results := fbc.FindGVKProviders(GVKQuery{Group: "argoproj.io", Kind: "argocd"})
		if len(results) != 0 {
			t.Errorf("expected 0 providers (case mismatch), got %d", len(results))
		}
	})

	t.Run("metadata populated", func(t *testing.T) {
		results := fbc.FindGVKProviders(GVKQuery{Group: "logging.openshift.io", Kind: "ClusterLogging"})
		if len(results) != 1 {
			t.Fatalf("expected 1 provider, got %d", len(results))
		}
		if results[0].DefaultChannel != "stable" {
			t.Errorf("DefaultChannel = %q, want stable", results[0].DefaultChannel)
		}
		if results[0].Description != "Logging operator" {
			t.Errorf("Description = %q, want 'Logging operator'", results[0].Description)
		}
	})
}

func TestLoadFromDirectoryEmpty(t *testing.T) {
	dir := t.TempDir()

	fbc, err := LoadFromDirectory(dir)
	if err != nil {
		t.Fatalf("LoadFromDirectory() error: %v", err)
	}

	if len(fbc.Packages) != 0 || len(fbc.Channels) != 0 || len(fbc.Bundles) != 0 {
		t.Errorf("expected empty FBC for empty directory")
	}
}
