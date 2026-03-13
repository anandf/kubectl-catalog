package cmd

import (
	"fmt"
	"os"

	"github.com/anandf/kubectl-catalog/pkg/registry"
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
}

const (
	defaultCatalogImageBase = "registry.redhat.io/redhat/redhat-operator-index"
)

var (
	kubeconfig      string
	namespace       string
	ocpVersion      string
	catalogOverride string
	refreshCache    bool
	pullSecretPath  string
)

func init() {
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig file (defaults to $KUBECONFIG or ~/.kube/config)")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "target namespace for operator installation")
	rootCmd.PersistentFlags().StringVar(&ocpVersion, "ocp-version", "", "OCP version to derive the catalog image (e.g. 4.20)")
	rootCmd.PersistentFlags().StringVar(&catalogOverride, "catalog", "", "catalog image override (takes precedence over --ocp-version)")
	rootCmd.PersistentFlags().BoolVar(&refreshCache, "refresh", false, "force re-pull of cached catalog images")
	rootCmd.PersistentFlags().StringVar(&pullSecretPath, "pull-secret", "", "path to a pull secret file for registry authentication")
}

// resolveCatalogImage returns the catalog image to use.
// If --catalog is set, it takes precedence. Otherwise, the image is derived
// from --ocp-version using the default catalog image base.
// For commands that have their own --catalog flag, pass that value as cmdCatalog.
func resolveCatalogImage(cmdCatalog string) (string, error) {
	// Command-level --catalog flag takes highest precedence
	if cmdCatalog != "" {
		return cmdCatalog, nil
	}
	// Global --catalog flag
	if catalogOverride != "" {
		return catalogOverride, nil
	}
	// Derive from OCP version
	if ocpVersion != "" {
		return fmt.Sprintf("%s:v%s", defaultCatalogImageBase, ocpVersion), nil
	}
	return "", fmt.Errorf("either --ocp-version or --catalog must be specified")
}

// newImagePuller creates an ImagePuller using the pull secret if provided,
// otherwise falls back to the default Docker keychain.
func newImagePuller() (*registry.ImagePuller, error) {
	if pullSecretPath != "" {
		return registry.NewImagePullerWithPullSecret(pullSecretPath)
	}
	return registry.NewImagePuller(), nil
}

func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	return nil
}
