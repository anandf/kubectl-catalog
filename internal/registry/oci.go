package registry

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/stream"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// OCILayerMediaType is the standard OCI layer media type used by Argo CD and FluxCD
// for plain manifest OCI artifacts.
const OCILayerMediaType types.MediaType = "application/vnd.oci.image.layer.v1.tar+gzip"

// PushManifests packages the files in the given directory as a single-layer OCI
// artifact and pushes it to the specified image reference.
//
// The artifact uses the standard OCI media type (application/vnd.oci.image.layer.v1.tar+gzip)
// which is natively supported by:
//   - Argo CD v3.1+ (OCI artifact sources)
//   - FluxCD (OCIRepository sources)
//   - ORAS CLI
//
// OCI annotations (org.opencontainers.image.*) are set from the _metadata.yaml
// if present in the directory.
func (p *ImagePuller) PushManifests(ctx context.Context, dir, imageRef string, ociAnnotations map[string]string) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parsing image reference %s: %w", imageRef, err)
	}

	// Create a tar archive of all files in the directory
	tarBuf, err := tarDirectory(dir)
	if err != nil {
		return fmt.Errorf("creating tar archive: %w", err)
	}

	// Create a gzipped layer with the standard OCI media type.
	// stream.NewLayer compresses by default (gzip), matching the +gzip suffix.
	layer := stream.NewLayer(io.NopCloser(tarBuf), stream.WithMediaType(OCILayerMediaType))

	// Build the image: empty base + our manifest layer
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return fmt.Errorf("building OCI image: %w", err)
	}

	// Set OCI manifest media type
	img = mutate.MediaType(img, types.OCIManifestSchema1)

	// Set OCI annotations on the manifest
	if len(ociAnnotations) > 0 {
		img = mutate.Annotations(img, ociAnnotations).(v1.Image)
	}

	if err := remote.Write(ref, img, remote.WithAuthFromKeychain(p.keychain), remote.WithContext(ctx)); err != nil {
		return fmt.Errorf("pushing to %s: %w", imageRef, err)
	}

	return nil
}

// tarDirectory creates a tar archive of all files in the directory.
// Files are stored with paths relative to the directory root.
func tarDirectory(dir string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// Use forward slashes for tar paths
		relPath = filepath.ToSlash(relPath)

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		header := &tar.Header{
			Name:    relPath,
			Mode:    0o644,
			Size:    int64(len(data)),
			ModTime: time.Now(),
		}

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("writing tar header for %s: %w", relPath, err)
		}

		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("writing tar data for %s: %w", relPath, err)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing tar writer: %w", err)
	}

	return &buf, nil
}
