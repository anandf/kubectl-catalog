package cmd

import (
	"context"
	"fmt"

	"github.com/anandf/kubectl-catalog/pkg/applier"
	"github.com/anandf/kubectl-catalog/pkg/bundle"
	"github.com/anandf/kubectl-catalog/pkg/catalog"
	"github.com/anandf/kubectl-catalog/pkg/registry"
	"github.com/anandf/kubectl-catalog/pkg/resolver"
	"github.com/anandf/kubectl-catalog/pkg/state"
	"github.com/spf13/cobra"
)

var (
	installChannel string
	installVersion string
	installForce   bool
)

var installCmd = &cobra.Command{
	Use:   "install <package-name>",
	Short: "Install an operator from a catalog",
	Long: `Install an operator by pulling its bundle from the catalog, resolving
dependencies, extracting manifests, and applying them to the target cluster.

The catalog is resolved from --ocp-version or --catalog.
If --channel is not specified, the package's default channel is used.
If --version is not specified, the latest version (channel head) is installed.

If the operator is already installed, use --force to re-install.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		packageName := args[0]
		ctx := context.Background()

		// Check if already installed
		stateManager, err := state.NewManager(kubeconfig, namespace)
		if err == nil {
			installed, err := stateManager.GetInstalled(ctx, packageName)
			if err == nil && installed != nil {
				if !installForce {
					return fmt.Errorf("package %q is already installed (v%s, channel %s); use --force to re-install",
						packageName, installed.Version, installed.Channel)
				}
				fmt.Printf("Package %q already installed (v%s), re-installing with --force\n",
					packageName, installed.Version)
			}
		}

		puller, err := newImagePuller()
		if err != nil {
			return fmt.Errorf("failed to create image puller: %w", err)
		}

		catalogImage, err := resolveCatalogImage("")
		if err != nil {
			return err
		}

		fbc, err := catalog.Load(ctx, catalogImage, refreshCache, puller)
		if err != nil {
			return fmt.Errorf("failed to load catalog %q: %w", catalogImage, err)
		}

		// Resolve the bundle and its dependencies
		res := resolver.New(fbc)
		installPlan, err := res.Resolve(packageName, installChannel, installVersion)
		if err != nil {
			return fmt.Errorf("failed to resolve %q: %w", packageName, err)
		}

		fmt.Printf("Resolved install plan: %d bundle(s) to install\n", len(installPlan.Bundles))
		for _, b := range installPlan.Bundles {
			fmt.Printf("  - %s v%s (from %s)\n", b.Name, b.Version, b.Image)
		}

		k8sApplier, err := applier.New(kubeconfig, namespace)
		if err != nil {
			return fmt.Errorf("failed to create applier: %w", err)
		}

		// If a pull secret was provided, create it in the target namespace (--namespace).
		// Even for cluster-scoped operators, the controller pods run in this namespace,
		// so the pull secret must be co-located with the Deployments and ServiceAccounts.
		if pullSecretPath != "" {
			if err := ensureClusterPullSecret(ctx, k8sApplier, packageName); err != nil {
				return err
			}
		}

		for _, b := range installPlan.Bundles {
			fmt.Printf("\nInstalling %s v%s...\n", b.Name, b.Version)

			bundleDir, err := puller.PullBundle(ctx, b.Image)
			if err != nil {
				return fmt.Errorf("failed to pull bundle %q: %w", b.Image, err)
			}

			manifests, err := bundle.Extract(bundleDir)
			if err != nil {
				return fmt.Errorf("failed to extract bundle %q: %w", b.Name, err)
			}

			ic := &applier.InstallContext{
				PackageName: b.Package,
				Version:     b.Version,
				Channel:     b.Channel,
				BundleName:  b.Name,
				CatalogRef:  catalogImage,
			}

			if err := k8sApplier.Apply(ctx, manifests, ic); err != nil {
				return fmt.Errorf("failed to apply bundle %q: %w", b.Name, err)
			}

			// Patch ServiceAccounts with the pull secret if provided
			if pullSecretPath != "" {
				if err := patchBundleServiceAccounts(ctx, k8sApplier, manifests); err != nil {
					return err
				}
			}
		}

		fmt.Printf("\nSuccessfully installed %s v%s\n", packageName, installPlan.TargetVersion)
		return nil
	},
}

// ensureClusterPullSecret reads the pull secret file and creates it as a
// kubernetes.io/dockerconfigjson Secret in the target namespace.
func ensureClusterPullSecret(ctx context.Context, k8sApplier *applier.Applier, packageName string) error {
	data, err := registry.ReadPullSecretData(pullSecretPath)
	if err != nil {
		return fmt.Errorf("failed to read pull secret: %w", err)
	}
	if err := k8sApplier.EnsurePullSecret(ctx, data, packageName); err != nil {
		return fmt.Errorf("failed to create pull secret in cluster: %w", err)
	}
	return nil
}

// patchBundleServiceAccounts patches all ServiceAccounts created by the bundle
// (plus the namespace's default SA) with imagePullSecrets referencing the pull
// secret. This ensures all operator pods can pull images from authenticated registries.
func patchBundleServiceAccounts(ctx context.Context, k8sApplier *applier.Applier, manifests *bundle.Manifests) error {
	// Always patch the default SA — pods that don't specify a SA use it
	if err := k8sApplier.PatchServiceAccountPullSecret(ctx, "default"); err != nil {
		return fmt.Errorf("failed to patch default ServiceAccount: %w", err)
	}

	// Patch all operator-specific ServiceAccounts from the bundle
	for _, obj := range manifests.RBAC {
		if obj.GetKind() == "ServiceAccount" && obj.GetName() != "default" {
			if err := k8sApplier.PatchServiceAccountPullSecret(ctx, obj.GetName()); err != nil {
				return fmt.Errorf("failed to patch ServiceAccount %q: %w", obj.GetName(), err)
			}
		}
	}
	return nil
}

func init() {
	installCmd.Flags().StringVar(&installChannel, "channel", "", "channel to install from (defaults to package's default channel)")
	installCmd.Flags().StringVar(&installVersion, "version", "", "specific version to install (defaults to channel head)")
	installCmd.Flags().BoolVar(&installForce, "force", false, "force re-install if already installed")
	rootCmd.AddCommand(installCmd)
}
