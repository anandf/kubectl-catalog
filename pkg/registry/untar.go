package registry

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Untar extracts a tar archive from the reader into the destination directory.
func Untar(r io.Reader, destDir string) error {
	tr := tar.NewReader(r)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		target := filepath.Join(destDir, header.Name)

		// Protect against zip-slip
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid tar entry: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("creating directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating parent directory: %w", err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("writing file %s: %w", target, err)
			}
			f.Close()
		case tar.TypeSymlink:
			// Resolve symlink target and validate it stays within destDir
			linkTarget := header.Linkname
			if !filepath.IsAbs(linkTarget) {
				linkTarget = filepath.Join(filepath.Dir(target), linkTarget)
			}
			if !strings.HasPrefix(filepath.Clean(linkTarget), filepath.Clean(destDir)+string(os.PathSeparator)) {
				return fmt.Errorf("invalid symlink target: %s -> %s", header.Name, header.Linkname)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("creating symlink %s: %w", target, err)
			}
		}
	}
}
