package cmd

import (
	"context"
	"fmt"

	"github.com/anandf/kubectl-catalog/pkg/applier"
	"github.com/anandf/kubectl-catalog/pkg/bundle"
	"github.com/anandf/kubectl-catalog/pkg/catalog"
	"github.com/anandf/kubectl-catalog/pkg/resolver"
	"github.com/anandf/kubectl-catalog/pkg/state"
	"github.com/spf13/cobra"
)

var upgradeChannel string

var upgradeCmd = &cobra.Command{
	Use:   "upgrade <package-name>",
	Short: "Upgrade an installed operator",
	Long: `Upgrade an operator to the latest version in the channel's upgrade graph.
The current installed version is discovered from resource annotations in the cluster.
The target version is the channel head — the bundle that the upgrade graph resolves to.
Only the target bundle is applied (intermediate versions are skipped).

The catalog is resolved from --ocp-version or --catalog. If neither is specified,
the catalog reference stored in the installed resource annotations is used.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		packageName := args[0]
		ctx := context.Background()

		// Discover current installed version from cluster annotations
		stateManager, err := state.NewManager(kubeconfig, namespace)
		if err != nil {
			return fmt.Errorf("failed to create state manager: %w", err)
		}

		installed, err := stateManager.GetInstalled(ctx, packageName)
		if err != nil {
			return fmt.Errorf("package %q is not installed: %w", packageName, err)
		}

		fmt.Printf("Current: %s v%s (channel: %s)\n", packageName, installed.Version, installed.Channel)

		// Resolve catalog: global flags first, then fall back to installed annotation
		catalogRef, err := resolveCatalogImage("")
		if err != nil {
			if installed.CatalogRef != "" {
				catalogRef = installed.CatalogRef
			} else {
				return fmt.Errorf("--ocp-version or --catalog is required (no catalog reference found in installed annotations)")
			}
		}

		puller, err := newImagePuller()
		if err != nil {
			return fmt.Errorf("failed to create image puller: %w", err)
		}

		fbc, err := catalog.Load(ctx, catalogRef, refreshCache, puller)
		if err != nil {
			return fmt.Errorf("failed to load catalog: %w", err)
		}

		channel := upgradeChannel
		if channel == "" {
			channel = installed.Channel
		}

		res := resolver.New(fbc)
		upgradePlan, err := res.ResolveUpgrade(packageName, channel, installed.Version)
		if err != nil {
			return fmt.Errorf("no upgrade available: %w", err)
		}

		// Only apply the target (head) bundle — intermediate versions are skipped.
		target := upgradePlan.Bundles[len(upgradePlan.Bundles)-1]

		fmt.Printf("Upgrade: %s -> %s\n", installed.Version, target.Version)
		fmt.Printf("\nApplying %s v%s...\n", target.Name, target.Version)

		k8sApplier, err := applier.New(kubeconfig, namespace)
		if err != nil {
			return fmt.Errorf("failed to create applier: %w", err)
		}

		// If a pull secret was provided, ensure it exists in the cluster
		if pullSecretPath != "" {
			if err := ensureClusterPullSecret(ctx, k8sApplier, packageName); err != nil {
				return err
			}
		}

		bundleDir, err := puller.PullBundle(ctx, target.Image)
		if err != nil {
			return fmt.Errorf("failed to pull bundle %q: %w", target.Image, err)
		}

		manifests, err := bundle.Extract(bundleDir)
		if err != nil {
			return fmt.Errorf("failed to extract bundle %q: %w", target.Name, err)
		}

		ic := &applier.InstallContext{
			PackageName: target.Package,
			Version:     target.Version,
			Channel:     target.Channel,
			BundleName:  target.Name,
			CatalogRef:  catalogRef,
		}

		if err := k8sApplier.Apply(ctx, manifests, ic); err != nil {
			return fmt.Errorf("failed to apply bundle %q: %w", target.Name, err)
		}

		// Patch ServiceAccounts with the pull secret if provided
		if pullSecretPath != "" {
			if err := patchBundleServiceAccounts(ctx, k8sApplier, manifests); err != nil {
				return err
			}
		}

		fmt.Printf("\nSuccessfully upgraded %s to v%s\n", packageName, target.Version)
		return nil
	},
}

func init() {
	upgradeCmd.Flags().StringVar(&upgradeChannel, "channel", "", "switch to a different channel for upgrade")
	rootCmd.AddCommand(upgradeCmd)
}
