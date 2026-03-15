package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/anandf/kubectl-catalog/internal/registry"
	"github.com/anandf/kubectl-catalog/internal/util"
	goyaml "gopkg.in/yaml.v3"
	"sigs.k8s.io/yaml"
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

	fbc.BuildIndexes()
	return fbc, nil
}

// parseFile parses a single FBC file containing one or more catalog entries.
// JSON files use json.Decoder for newline-delimited objects.
// YAML files use gopkg.in/yaml.v3's streaming Decoder for --- separated documents.
func parseFile(path string, fbc *FBC) error {
	ext := filepath.Ext(path)
	if ext == ".yaml" || ext == ".yml" {
		return parseYAMLFile(path, fbc)
	}
	return parseJSONFile(path, fbc)
}

func parseJSONFile(path string, fbc *FBC) error {
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
		if err := parseDocument(raw, path, fbc); err != nil {
			return err
		}
	}
	return nil
}

func parseYAMLFile(path string, fbc *FBC) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	decoder := goyaml.NewDecoder(f)
	for {
		var node goyaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decoding YAML in %s: %w", path, err)
		}

		data, err := goyaml.Marshal(&node)
		if err != nil {
			return fmt.Errorf("re-marshaling document in %s: %w", path, err)
		}

		if err := parseDocument(data, path, fbc); err != nil {
			return err
		}
	}
}

// schemaDetector is used to extract the schema field from a document.
type schemaDetector struct {
	Schema string `json:"schema"`
}

func parseDocument(data []byte, path string, fbc *FBC) error {
	var detector schemaDetector
	if err := yaml.Unmarshal(data, &detector); err != nil {
		return fmt.Errorf("detecting schema in %s: %w", path, err)
	}

	switch detector.Schema {
	case schemaPackage:
		var pkg Package
		if err := yaml.Unmarshal(data, &pkg); err != nil {
			return fmt.Errorf("parsing package in %s: %w", path, err)
		}
		fbc.Packages = append(fbc.Packages, pkg)

	case schemaChannel:
		var ch Channel
		if err := yaml.Unmarshal(data, &ch); err != nil {
			return fmt.Errorf("parsing channel in %s: %w", path, err)
		}
		fbc.Channels = append(fbc.Channels, ch)

	case schemaBundle:
		var b Bundle
		if err := yaml.Unmarshal(data, &b); err != nil {
			return fmt.Errorf("parsing bundle in %s: %w", path, err)
		}
		fbc.Bundles = append(fbc.Bundles, b)
	}

	return nil
}

// Load loads a catalog by image reference. If the catalog is already cached
// locally (and refresh is false), it loads from the cache. Otherwise, it pulls
// the image from the registry using the provided puller, caches it, and loads it.
func Load(ctx context.Context, imageRef string, refresh bool, puller *registry.ImagePuller) (*FBC, error) {
	cacheDir := filepath.Join(puller.CacheDir(), "catalogs", util.SanitizeRef(imageRef))

	// If cached and no refresh requested, load directly
	if !refresh {
		if _, err := os.Stat(cacheDir); err == nil {
			return LoadFromDirectory(cacheDir)
		}
	}

	// Remove stale cache if refreshing
	if refresh {
		if err := os.RemoveAll(cacheDir); err != nil {
			return nil, fmt.Errorf("removing stale cache at %s: %w", cacheDir, err)
		}
	}

	// Pull from registry
	fmt.Printf("Pulling catalog %s...\n", imageRef)
	catalogDir, err := puller.PullCatalog(ctx, imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to pull catalog %s: %w", imageRef, err)
	}

	return LoadFromDirectory(catalogDir)
}


