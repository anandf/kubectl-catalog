package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anandf/kubectl-catalog/internal/applier"
	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/anandf/kubectl-catalog/internal/registry"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "kubectl-catalog",
	Short: "A kubectl plugin for installing OLM catalog operators on vanilla Kubernetes",
	Long: `kubectl-catalog works with OpenShift Catalog bundles placed in public container
registries and helps install OLM bundles without requiring OLM on the target cluster.

It supports:
  - Pulling and parsing File-Based Catalogs (FBC) from container registries
  - Browsing available operators, channels, and versions
  - Resolving operator dependencies
  - Installing operators by extracting and applying bundle manifests
  - Managing upgrades using the catalog's upgrade graph

Install the binary as kubectl-catalog on your PATH to use it as "kubectl catalog".`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Catalog types and their default image bases.
var catalogTypes = map[string]struct {
	imageBase       string
	requiresVersion bool
}{
	"redhat":      {imageBase: "registry.redhat.io/redhat/redhat-operator-index", requiresVersion: true},
	"community":   {imageBase: "registry.redhat.io/redhat/community-operator-index", requiresVersion: true},
	"certified":   {imageBase: "registry.redhat.io/redhat/certified-operator-index", requiresVersion: true},
	"operatorhub": {imageBase: "quay.io/operatorhubio/catalog", requiresVersion: false},
}

var (
	kubeconfig      string
	namespace       string
	ocpVersion      string
	catalogType     string
	clusterType     string
	catalogOverride string
	cacheDir        string
	refreshCache    bool
	pullSecretPath  string
	timeout            time.Duration
	dryRun             bool
	noWait             bool
	deploymentTimeout  time.Duration
)

func init() {
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file (defaults to $KUBECONFIG or ~/.kube/config)")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "target namespace for operator installation")
	rootCmd.PersistentFlags().StringVar(&ocpVersion, "ocp-version", "", "OCP version to derive the catalog image (e.g. 4.20)")
	rootCmd.PersistentFlags().StringVar(&catalogType, "catalog-type", "redhat", "catalog type: redhat, community, certified, operatorhub")
	rootCmd.PersistentFlags().StringVar(&clusterType, "cluster-type", "k8s", "target cluster type: k8s, ocp, okd")
	rootCmd.PersistentFlags().StringVar(&catalogOverride, "catalog", "", "full catalog image reference (overrides --catalog-type and --ocp-version)")
	rootCmd.PersistentFlags().StringVar(&cacheDir, "cache-dir", registry.DefaultCacheDir(), "directory for caching catalog and bundle images")
	rootCmd.PersistentFlags().BoolVar(&refreshCache, "refresh", false, "force re-pull of cached catalog images")
	rootCmd.PersistentFlags().StringVar(&pullSecretPath, "pull-secret", "", "path to a pull secret file for registry authentication")
	rootCmd.PersistentFlags().DurationVar(&timeout, "timeout", 30*time.Minute, "maximum time for the operation (e.g. 10m, 1h)")
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "show what would be applied without making changes")
	rootCmd.PersistentFlags().BoolVar(&noWait, "no-wait", false, "skip deployment readiness checks after install/upgrade")
	rootCmd.PersistentFlags().DurationVar(&deploymentTimeout, "deployment-timeout", 0, "timeout for deployment readiness checks (defaults to 5m; use --no-wait to skip entirely)")
}

// isVanillaK8s returns true if the target cluster does not have the
// OpenShift service-ca-operator (i.e. cluster-type is "k8s").
func isVanillaK8s() bool {
	return clusterType == "k8s"
}

// resolveCatalogImage returns the catalog image to use.
// Priority: --catalog (full image ref) > --type + --ocp-version.
// For commands that have their own --catalog flag, pass that value as cmdCatalog.
func resolveCatalogImage(cmdCatalog string) (string, error) {
	// Command-level --catalog flag takes highest precedence
	if cmdCatalog != "" {
		return cmdCatalog, nil
	}
	// Global --catalog flag (full image override)
	if catalogOverride != "" {
		return catalogOverride, nil
	}

	ct, ok := catalogTypes[catalogType]
	if !ok {
		return "", fmt.Errorf("unknown catalog type %q (valid types: redhat, community, certified, operatorhub)", catalogType)
	}

	if !ct.requiresVersion {
		return ct.imageBase + ":latest", nil
	}

	if ocpVersion == "" {
		return "", fmt.Errorf("--ocp-version is required for catalog type %q", catalogType)
	}
	return fmt.Sprintf("%s:v%s", ct.imageBase, ocpVersion), nil
}

// newImagePuller creates an ImagePuller using the pull secret if provided,
// otherwise falls back to the default Docker keychain.
func newImagePuller() (*registry.ImagePuller, error) {
	if pullSecretPath != "" {
		return registry.NewImagePullerWithPullSecret(cacheDir, pullSecretPath)
	}
	return registry.NewImagePuller(cacheDir), nil
}

// requirePullSecretForRedHat returns an error if the catalog image is from
// registry.redhat.io and no --pull-secret was provided.
func requirePullSecretForRedHat(catalogImage string) error {
	if strings.HasPrefix(catalogImage, "registry.redhat.io/") && pullSecretPath == "" {
		return fmt.Errorf("--pull-secret is required when using Red Hat catalog images (registry.redhat.io)")
	}
	return nil
}

// applyInstallMode validates the requested install mode against the bundle's
// supported modes and configures WATCH_NAMESPACE on deployments accordingly.
// If mode is empty, the bundle's default install mode is used.
// For AllNamespaces: WATCH_NAMESPACE="" (watches everything).
// For SingleNamespace/OwnNamespace: WATCH_NAMESPACE=<targetNamespace>.
func applyInstallMode(manifests *bundle.Manifests, mode, targetNamespace string) error {
	// Use the bundle's default install mode if not explicitly set
	if mode == "" {
		mode = manifests.DefaultInstallMode()
	}

	switch mode {
	case "AllNamespaces", "SingleNamespace", "OwnNamespace":
		// valid
	default:
		return fmt.Errorf("invalid --install-mode %q (valid modes: AllNamespaces, SingleNamespace, OwnNamespace)", mode)
	}

	// Validate the mode is supported by the CSV (if install modes were declared)
	if len(manifests.InstallModes) > 0 && !manifests.SupportsInstallMode(mode) {
		var supported []string
		for _, im := range manifests.InstallModes {
			if im.Supported {
				supported = append(supported, im.Type)
			}
		}
		return fmt.Errorf("install mode %q is not supported by this operator (supported: %s)",
			mode, strings.Join(supported, ", "))
	}

	switch mode {
	case "AllNamespaces":
		manifests.SetWatchNamespace("")
		fmt.Printf("  Install mode: AllNamespaces (operator watches all namespaces)\n")
	case "SingleNamespace", "OwnNamespace":
		manifests.SetWatchNamespace(targetNamespace)
		fmt.Printf("  Install mode: %s (operator watches namespace %q)\n", mode, targetNamespace)
	}

	return nil
}

// parseEnvVars parses a comma-separated list of key=value pairs into a map.
// Example: "KEY1=val1,KEY2=val2" -> {"KEY1": "val1", "KEY2": "val2"}
func parseEnvVars(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	result := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid env var %q: expected KEY=VALUE format", pair)
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			return nil, fmt.Errorf("invalid env var %q: key cannot be empty", pair)
		}
		result[key] = parts[1]
	}
	return result, nil
}

// applierOptions returns the standard applier options derived from global flags.
func applierOptions() applier.Options {
	return applier.Options{
		DryRun:                 dryRun,
		NoWait:                 noWait,
		DeploymentReadyTimeout: deploymentTimeout,
	}
}

func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}
