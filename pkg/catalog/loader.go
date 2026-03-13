package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anandf/kubectl-catalog/pkg/registry"
	"github.com/anandf/kubectl-catalog/pkg/util"
)

const (
	schemaPackage = "olm.package"
	schemaChannel = "olm.channel"
	schemaBundle  = "olm.bundle"

	// Known catalog directory paths within FBC images
	fbcPath = "configs"
)

// LoadFromDirectory parses a File-Based Catalog from an extracted image directory.
// FBC stores data as JSON objects separated by newlines, organized by package directories.
func LoadFromDirectory(dir string) (*FBC, error) {
	fbc := &FBC{}

	// FBC images typically have catalogs under /configs or the root
	catalogDir := dir
	if info, err := os.Stat(filepath.Join(dir, fbcPath)); err == nil && info.IsDir() {
		catalogDir = filepath.Join(dir, fbcPath)
	}

	err := filepath.Walk(catalogDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		ext := filepath.Ext(path)
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return nil
		}

		return parseFile(path, fbc)
	})

	if err != nil {
		return nil, fmt.Errorf("walking catalog directory %s: %w", catalogDir, err)
	}

	return fbc, nil
}

// parseFile parses a single FBC file containing newline-delimited JSON objects.
// Uses json.Decoder for correct tokenization (handles braces inside strings).
func parseFile(path string, fbc *FBC) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	decoder := json.NewDecoder(f)

	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("decoding JSON in %s: %w", path, err)
		}
		if err := parseBlob(raw, fbc); err != nil {
			return fmt.Errorf("parsing entry in %s: %w", path, err)
		}
	}

	return nil
}

// schemaDetector is used to extract the schema field from a JSON blob.
type schemaDetector struct {
	Schema string `json:"schema"`
}

func parseBlob(data []byte, fbc *FBC) error {
	var detector schemaDetector
	if err := json.Unmarshal(data, &detector); err != nil {
		return fmt.Errorf("detecting schema: %w", err)
	}

	switch detector.Schema {
	case schemaPackage:
		var pkg Package
		if err := json.Unmarshal(data, &pkg); err != nil {
			return fmt.Errorf("parsing package: %w", err)
		}
		fbc.Packages = append(fbc.Packages, pkg)

	case schemaChannel:
		var ch Channel
		if err := json.Unmarshal(data, &ch); err != nil {
			return fmt.Errorf("parsing channel: %w", err)
		}
		fbc.Channels = append(fbc.Channels, ch)

	case schemaBundle:
		var b Bundle
		if err := json.Unmarshal(data, &b); err != nil {
			return fmt.Errorf("parsing bundle: %w", err)
		}
		fbc.Bundles = append(fbc.Bundles, b)
	}

	return nil
}

// Load loads a catalog by image reference. If the catalog is already cached
// locally (and refresh is false), it loads from the cache. Otherwise, it pulls
// the image from the registry, caches it, and then loads it.
// Load loads a catalog by image reference. If the catalog is already cached
// locally (and refresh is false), it loads from the cache. Otherwise, it pulls
// the image from the registry using the provided puller, caches it, and loads it.
// If puller is nil, a default puller with the Docker keychain is used.
func Load(ctx context.Context, imageRef string, refresh bool, puller *registry.ImagePuller) (*FBC, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home directory: %w", err)
	}

	cacheDir := filepath.Join(home, ".kubectl-catalog", "catalogs", util.SanitizeRef(imageRef))

	// If cached and no refresh requested, load directly
	if !refresh {
		if _, err := os.Stat(cacheDir); err == nil {
			return LoadFromDirectory(cacheDir)
		}
	}

	// Remove stale cache if refreshing
	if refresh {
		os.RemoveAll(cacheDir)
	}

	// Pull from registry
	fmt.Printf("Pulling catalog %s...\n", imageRef)
	if puller == nil {
		puller = registry.NewImagePuller()
	}
	catalogDir, err := puller.PullCatalog(ctx, imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to pull catalog %s: %w", imageRef, err)
	}

	return LoadFromDirectory(catalogDir)
}


