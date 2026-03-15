package catalog

import (
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
