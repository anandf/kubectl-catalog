package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTarDirectory(t *testing.T) {
	dir := t.TempDir()

	// Create test files
	os.WriteFile(filepath.Join(dir, "file1.yaml"), []byte("content1"), 0o644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(dir, "subdir", "file2.yaml"), []byte("content2"), 0o644)

	reader, err := tarDirectory(dir)
	if err != nil {
		t.Fatalf("tarDirectory() error: %v", err)
	}

	// Extract the tar to verify it's valid
	extractDir := t.TempDir()
	err = Untar(reader, extractDir)
	if err != nil {
		t.Fatalf("Untar() error: %v", err)
	}

	// Verify extracted files
	data, err := os.ReadFile(filepath.Join(extractDir, "file1.yaml"))
	if err != nil {
		t.Fatalf("reading extracted file1.yaml: %v", err)
	}
	if string(data) != "content1" {
		t.Errorf("file1.yaml content = %q, want %q", string(data), "content1")
	}

	data, err = os.ReadFile(filepath.Join(extractDir, "subdir", "file2.yaml"))
	if err != nil {
		t.Fatalf("reading extracted subdir/file2.yaml: %v", err)
	}
	if string(data) != "content2" {
		t.Errorf("subdir/file2.yaml content = %q, want %q", string(data), "content2")
	}
}

func TestTarDirectory_Empty(t *testing.T) {
	dir := t.TempDir()

	reader, err := tarDirectory(dir)
	if err != nil {
		t.Fatalf("tarDirectory() on empty dir error: %v", err)
	}
	if reader == nil {
		t.Fatal("tarDirectory() returned nil reader")
	}
}

func TestTarDirectory_UsesForwardSlashes(t *testing.T) {
	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", "b", "c.yaml"), []byte("nested"), 0o644)

	reader, err := tarDirectory(dir)
	if err != nil {
		t.Fatalf("tarDirectory() error: %v", err)
	}

	extractDir := t.TempDir()
	err = Untar(reader, extractDir)
	if err != nil {
		t.Fatalf("Untar() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(extractDir, "a", "b", "c.yaml"))
	if err != nil {
		t.Fatalf("reading extracted nested file: %v", err)
	}
	if string(data) != "nested" {
		t.Errorf("nested file content = %q, want %q", string(data), "nested")
	}
}
