package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anandf/kubectl-catalog/internal/util"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// DefaultCacheDir returns the default cache directory (~/.kubectl-catalog).
func DefaultCacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kubectl-catalog")
}

// ImagePuller pulls container images and extracts their filesystem contents.
type ImagePuller struct {
	cacheBase string
	keychain  authn.Keychain
}

// CacheDir returns the base cache directory used by this puller.
func (p *ImagePuller) CacheDir() string {
	return p.cacheBase
}

// NewImagePuller creates a puller using the default Docker keychain
// (~/.docker/config.json, credential helpers).
func NewImagePuller(cacheDir string) *ImagePuller {
	return &ImagePuller{
		cacheBase: cacheDir,
		keychain:  authn.DefaultKeychain,
	}
}

// NewImagePullerWithPullSecret creates a puller that uses a pull secret file
// for registry authentication. The pull secret is tried first; if it doesn't
// have credentials for a given registry, the default Docker keychain is used
// as a fallback.
func NewImagePullerWithPullSecret(cacheDir, pullSecretPath string) (*ImagePuller, error) {
	data, err := os.ReadFile(pullSecretPath)
	if err != nil {
		return nil, fmt.Errorf("reading pull secret %s: %w", pullSecretPath, err)
	}

	keychain, err := newPullSecretKeychain(data)
	if err != nil {
		return nil, fmt.Errorf("parsing pull secret %s: %w", pullSecretPath, err)
	}

	return &ImagePuller{
		cacheBase: cacheDir,
		keychain:  authn.NewMultiKeychain(keychain, authn.DefaultKeychain),
	}, nil
}

// VerifyCredentials checks that the pull secret credentials are valid for the
// given image reference by performing a lightweight HEAD request against the
// registry (fetches only the image manifest descriptor, not layers).
// This provides fast feedback before creating the Secret in the cluster.
func (p *ImagePuller) VerifyCredentials(ctx context.Context, imageRef string) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parsing image reference %s: %w", imageRef, err)
	}

	_, err = remote.Head(ref, remote.WithAuthFromKeychain(p.keychain), remote.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("authentication failed for %s: %w (check your --pull-secret credentials)", ref.Context().RegistryStr(), err)
	}

	return nil
}

// PullCatalog pulls a File-Based Catalog image and extracts only the catalog
// config files to the local cache. Returns the path to the extracted directory.
func (p *ImagePuller) PullCatalog(ctx context.Context, imageRef string) (string, error) {
	destDir := filepath.Join(p.cacheBase, "catalogs", util.SanitizeRef(imageRef))
	if err := p.pullAndExtract(ctx, imageRef, destDir, "configs"); err != nil {
		if removeErr := os.RemoveAll(destDir); removeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clean up partial extraction at %s: %v\n", destDir, removeErr)
		}
		return "", err
	}
	return destDir, nil
}

// PullBundle pulls a bundle image and extracts its manifests and metadata
// to the local cache. Returns the path to the extracted bundle directory.
// If the bundle is already cached locally, it returns the cached path without re-pulling.
func (p *ImagePuller) PullBundle(ctx context.Context, imageRef string) (string, error) {
	destDir := filepath.Join(p.cacheBase, "bundles", util.SanitizeRef(imageRef))

	// Return cached bundle if it exists
	if _, err := os.Stat(destDir); err == nil {
		return destDir, nil
	}

	if err := p.pullAndExtract(ctx, imageRef, destDir, "manifests", "metadata"); err != nil {
		if removeErr := os.RemoveAll(destDir); removeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clean up partial extraction at %s: %v\n", destDir, removeErr)
		}
		return "", err
	}
	return destDir, nil
}

func (p *ImagePuller) pullAndExtract(ctx context.Context, imageRef string, destDir string, pathPrefixes ...string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parsing image reference %s: %w", imageRef, err)
	}

	fmt.Printf("  Pulling %s...\n", imageRef)

	img, err := remote.Image(ref, remote.WithAuthFromKeychain(p.keychain), remote.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("pulling image %s: %w", imageRef, err)
	}

	// Print image size for progress feedback
	manifest, err := img.Manifest()
	if err == nil && manifest != nil {
		var totalSize int64
		for _, layer := range manifest.Layers {
			totalSize += layer.Size
		}
		if totalSize > 0 {
			fmt.Printf("  Downloading %s (%.1f MB)...\n", ref.Context().RepositoryStr(), float64(totalSize)/(1024*1024))
		}
	}

	if err := extractImage(img, destDir, pathPrefixes...); err != nil {
		return fmt.Errorf("extracting image %s: %w", imageRef, err)
	}

	fmt.Printf("  Extracted to %s\n", destDir)
	return nil
}

// extractImage flattens all layers of a container image into a directory,
// producing the equivalent of the container's root filesystem.
func extractImage(img v1.Image, destDir string, pathPrefixes ...string) error {
	reader := mutate.Extract(img)
	defer reader.Close()

	return Untar(reader, destDir, pathPrefixes...)
}

// pullSecretKeychain implements authn.Keychain using a pull secret JSON file.
// Pull secrets have the format: {"auths": {"registry.example.com": {"auth": "base64..."}}}
type pullSecretKeychain struct {
	auths map[string]authEntry
}

type authEntry struct {
	Auth     string `json:"auth"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// resolvedAuth returns the base64-encoded auth string. If the Auth field is
// empty, it falls back to constructing it from Username and Password fields.
func (e authEntry) resolvedAuth() string {
	if e.Auth != "" {
		return e.Auth
	}
	if e.Username != "" {
		return base64.StdEncoding.EncodeToString([]byte(e.Username + ":" + e.Password))
	}
	return ""
}

type pullSecretFile struct {
	Auths map[string]authEntry `json:"auths"`
}

func newPullSecretKeychain(data []byte) (*pullSecretKeychain, error) {
	var ps pullSecretFile
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("unmarshaling pull secret: %w", err)
	}
	if ps.Auths == nil {
		return nil, fmt.Errorf("pull secret has no 'auths' field")
	}
	return &pullSecretKeychain{auths: ps.Auths}, nil
}

// Resolve implements authn.Keychain. It looks up the registry in the pull secret's
// auths map and returns the corresponding authenticator.
func (k *pullSecretKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	// Try the full registry host
	registryName := resource.RegistryStr()

	if entry, ok := k.auths[registryName]; ok {
		if auth := entry.resolvedAuth(); auth != "" {
			return authn.FromConfig(authn.AuthConfig{Auth: auth}), nil
		}
	}

	// Some pull secrets use "https://registry.example.com" as the key
	for key, entry := range k.auths {
		if matchesRegistry(key, registryName) {
			if auth := entry.resolvedAuth(); auth != "" {
				return authn.FromConfig(authn.AuthConfig{Auth: auth}), nil
			}
		}
	}

	return authn.Anonymous, nil
}

// matchesRegistry checks if a pull secret key (which may include https:// prefix
// or /v1/ or /v2/ suffixes) matches the given registry name.
func matchesRegistry(pullSecretKey, registry string) bool {
	// Strip common scheme prefixes from pull secret keys
	cleaned := pullSecretKey
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(cleaned, prefix) {
			cleaned = cleaned[len(prefix):]
			break
		}
	}
	// Strip trailing path like /v1/ or /v2/ but preserve host:port
	if idx := strings.Index(cleaned, "/"); idx >= 0 {
		cleaned = cleaned[:idx]
	}
	return cleaned == registry
}

// ReadPullSecretData reads and returns the raw bytes of a pull secret file.
// This is used by the applier to create the Kubernetes Secret in the cluster.
func ReadPullSecretData(pullSecretPath string) ([]byte, error) {
	data, err := os.ReadFile(pullSecretPath)
	if err != nil {
		return nil, fmt.Errorf("reading pull secret %s: %w", pullSecretPath, err)
	}

	// Validate it's valid JSON with an auths field
	var ps pullSecretFile
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("pull secret is not valid JSON: %w", err)
	}
	if ps.Auths == nil {
		return nil, fmt.Errorf("pull secret has no 'auths' field")
	}

	return data, nil
}
