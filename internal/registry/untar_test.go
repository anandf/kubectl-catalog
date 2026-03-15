package registry

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func createTarBuffer(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for name, content := range files {
		header := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("writing tar header for %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing tar data for %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	return &buf
}

func TestUntar(t *testing.T) {
	files := map[string]string{
		"manifests/deployment.yaml": "apiVersion: apps/v1\nkind: Deployment",
		"manifests/crd.yaml":        "apiVersion: apiextensions.k8s.io/v1\nkind: CRD",
		"metadata/annotations.yaml": "annotations: {}",
	}

	destDir := t.TempDir()
	buf := createTarBuffer(t, files)

	err := Untar(buf, destDir)
	if err != nil {
		t.Fatalf("Untar() error: %v", err)
	}

	// Verify all files were extracted
	for name, content := range files {
		data, err := os.ReadFile(filepath.Join(destDir, name))
		if err != nil {
			t.Errorf("reading extracted file %s: %v", name, err)
			continue
		}
		if string(data) != content {
			t.Errorf("file %s content = %q, want %q", name, string(data), content)
		}
	}
}

func TestUntar_WithPathPrefixes(t *testing.T) {
	files := map[string]string{
		"manifests/deployment.yaml": "deployment",
		"manifests/crd.yaml":        "crd",
		"metadata/annotations.yaml": "annotations",
		"configs/catalog.json":      "catalog",
		"other/readme.txt":          "readme",
	}

	destDir := t.TempDir()
	buf := createTarBuffer(t, files)

	err := Untar(buf, destDir, "manifests", "metadata")
	if err != nil {
		t.Fatalf("Untar() error: %v", err)
	}

	// Verify only manifests/ and metadata/ files were extracted
	for _, name := range []string{"manifests/deployment.yaml", "manifests/crd.yaml", "metadata/annotations.yaml"} {
		if _, err := os.Stat(filepath.Join(destDir, name)); err != nil {
			t.Errorf("expected file %s to exist", name)
		}
	}

	// Verify configs/ and other/ were NOT extracted
	for _, name := range []string{"configs/catalog.json", "other/readme.txt"} {
		if _, err := os.Stat(filepath.Join(destDir, name)); err == nil {
			t.Errorf("file %s should not have been extracted", name)
		}
	}
}

func TestUntar_ZipSlipProtection(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	header := &tar.Header{
		Name: "../../../etc/passwd",
		Mode: 0o644,
		Size: 4,
	}
	tw.WriteHeader(header)
	tw.Write([]byte("evil"))
	tw.Close()

	destDir := t.TempDir()
	err := Untar(&buf, destDir)
	if err == nil {
		t.Error("expected zip-slip protection error")
	}
}

func TestUntar_AbsoluteSymlinkRejected(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	header := &tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "manifests/evil-link",
		Linkname: "/etc/passwd",
	}
	tw.WriteHeader(header)
	tw.Close()

	destDir := t.TempDir()
	err := Untar(&buf, destDir, "manifests")
	if err == nil {
		t.Error("expected absolute symlink to be rejected")
	}
	if err != nil && !bytes.Contains([]byte(err.Error()), []byte("absolute symlink")) {
		t.Errorf("expected error about absolute symlinks, got: %v", err)
	}
}

func TestUntar_RelativeSymlinkEscapeRejected(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	header := &tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "manifests/escape-link",
		Linkname: "../../etc/passwd",
	}
	tw.WriteHeader(header)
	tw.Close()

	destDir := t.TempDir()
	err := Untar(&buf, destDir, "manifests")
	if err == nil {
		t.Error("expected relative symlink escape to be rejected")
	}
}

func TestUntar_OversizedFileRejected(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Create a header claiming a file larger than maxFileSize
	header := &tar.Header{
		Name: "manifests/huge.yaml",
		Mode: 0o644,
		Size: maxFileSize + 1,
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("writing header: %v", err)
	}
	// Write just enough to satisfy tar (we don't need to write the full size)
	data := make([]byte, 1024)
	tw.Write(data)
	tw.Close()

	destDir := t.TempDir()
	err := Untar(&buf, destDir)
	if err == nil {
		t.Error("expected oversized file to be rejected")
	}
}

func TestUntar_EmptyArchive(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.Close()

	destDir := t.TempDir()
	err := Untar(&buf, destDir)
	if err != nil {
		t.Fatalf("Untar() on empty archive: %v", err)
	}
}

func TestMatchesPrefix(t *testing.T) {
	tests := []struct {
		path     string
		prefixes []string
		want     bool
	}{
		{"manifests/foo.yaml", []string{"manifests"}, true},
		{"manifests", []string{"manifests"}, true},
		{"metadata/annotations.yaml", []string{"manifests", "metadata"}, true},
		{"configs/catalog.json", []string{"manifests", "metadata"}, false},
		{"other.txt", []string{"manifests"}, false},
		{"manifestsextra/foo.yaml", []string{"manifests"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := matchesPrefix(tt.path, tt.prefixes)
			if got != tt.want {
				t.Errorf("matchesPrefix(%q, %v) = %v, want %v", tt.path, tt.prefixes, got, tt.want)
			}
		})
	}
}
