package registry

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// maxExtractSize is the maximum total bytes that can be extracted from a single archive.
	// This prevents resource exhaustion from maliciously crafted archives (zip bombs).
	maxExtractSize = 2 * 1024 * 1024 * 1024 // 2 GiB

	// maxFileSize is the maximum size of a single extracted file.
	maxFileSize = 512 * 1024 * 1024 // 512 MiB
)

// Untar extracts a tar archive from the reader into the destination directory.
// If pathPrefixes is non-empty, only entries whose cleaned path starts with one
// of the given prefixes (e.g. "configs/", "manifests/") are extracted.
func Untar(r io.Reader, destDir string, pathPrefixes ...string) error {
	tr := tar.NewReader(r)
	var totalExtracted int64

	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		clean := filepath.Clean(header.Name)

		// If path prefixes are specified, skip entries that don't match
		if len(pathPrefixes) > 0 && !matchesPrefix(clean, pathPrefixes) {
			continue
		}

		target := filepath.Join(destDir, clean)

		// Protect against zip-slip
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid tar entry: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("creating directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if header.Size > maxFileSize {
				return fmt.Errorf("file %s size %d exceeds maximum allowed size %d", header.Name, header.Size, maxFileSize)
			}
			if totalExtracted+header.Size > maxExtractSize {
				return fmt.Errorf("archive extraction would exceed maximum total size %d", maxExtractSize)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent directory: %w", err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", target, err)
			}
			written, copyErr := io.Copy(f, io.LimitReader(tr, maxFileSize+1))
			f.Close()
			if copyErr != nil {
				return fmt.Errorf("writing file %s: %w", target, copyErr)
			}
			if written > maxFileSize {
				return fmt.Errorf("file %s exceeded maximum allowed size %d during extraction", header.Name, maxFileSize)
			}
			totalExtracted += written
		case tar.TypeSymlink:
			// Reject absolute symlinks — they could point anywhere on the filesystem
			if filepath.IsAbs(header.Linkname) {
				return fmt.Errorf("invalid symlink %q: absolute symlink targets are not allowed", header.Name)
			}
			// Resolve the relative symlink and verify it stays within destDir
			linkTarget := filepath.Join(filepath.Dir(target), header.Linkname)
			cleanedLink := filepath.Clean(linkTarget)
			destPrefix := filepath.Clean(destDir) + string(os.PathSeparator)
			if cleanedLink != filepath.Clean(destDir) && !strings.HasPrefix(cleanedLink, destPrefix) {
				return fmt.Errorf("invalid symlink target %q escapes destination directory", header.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent directory for symlink: %w", err)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("creating symlink %s: %w", target, err)
			}
		}
	}
}

// matchesPrefix returns true if the path equals or is nested under one of the prefixes.
func matchesPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}
