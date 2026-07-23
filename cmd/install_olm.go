package cmd

import (
	"context"
	"fmt"

	"github.com/anandf/kubectl-catalog/internal/applier"
	"github.com/anandf/kubectl-catalog/internal/catalog"
	"github.com/anandf/kubectl-catalog/internal/olm"
	"github.com/anandf/kubectl-catalog/internal/registry"
	"github.com/anandf/kubectl-catalog/internal/resolver"
	"github.com/spf13/cobra"
)

func installViaOLM(cmd *cobra.Command, ctx context.Context, packageName string) error {
	catalogImage, err := resolveCatalogImage("")
	if err != nil {
		return err
	}
	if err := requirePullSecretForRedHat(catalogImage); err != nil {
		return err
	}

	if installEnv != "" {
		fmt.Println("Warning: --env is not supported with --installation-type=olm (use Subscription spec.config.env instead)")
	}

	// Resolve startingCSV from catalog when --version is specified
	var startingCSV string
	channel := installChannel
	if installVersion != "" {
		puller, err := newImagePuller()
		if err != nil {
			return fmt.Errorf("failed to create image puller: %w", err)
		}
		fbc, err := catalog.Load(ctx, catalogImage, refreshCache, puller)
		if err != nil {
			return fmt.Errorf("failed to load catalog: %w", err)
		}
		res := resolver.New(fbc)
		plan, err := res.Resolve(packageName, channel, installVersion)
		if err != nil {
			return withHint(
				fmt.Errorf("failed to resolve %q v%s: %w", packageName, installVersion, err),
				"run 'kubectl catalog list --show-channels' to see available versions",
			)
		}
		target := plan.Bundles[len(plan.Bundles)-1]
		startingCSV = target.Name
		if channel == "" {
			channel = target.Channel
		}
		fmt.Printf("Resolved version %s to CSV %q\n", installVersion, startingCSV)
	}

	// If no channel was resolved from version lookup, load catalog to find default
	if channel == "" {
		puller, err := newImagePuller()
		if err != nil {
			return fmt.Errorf("failed to create image puller: %w", err)
		}
		fbc, err := catalog.Load(ctx, catalogImage, refreshCache, puller)
		if err != nil {
			return fmt.Errorf("failed to load catalog: %w", err)
		}
		pkg := fbc.GetPackage(packageName)
		if pkg == nil {
			return fmt.Errorf("package %q not found in catalog", packageName)
		}
		channel = pkg.DefaultChannel
	}

	// Create pull secret name if needed
	var pullSecretName string
	if pullSecretPath != "" {
		pullSecretName = applier.PullSecretName(packageName)
		// Create the pull secret in the namespace for the CatalogSource
		k8sApplier, err := applier.New(kubeconfig, namespace, applierOptions())
		if err != nil {
			return fmt.Errorf("failed to create applier: %w", err)
		}
		data, err := registry.ReadPullSecretData(pullSecretPath)
		if err != nil {
			return fmt.Errorf("failed to read pull secret: %w", err)
		}
		if err := k8sApplier.EnsurePullSecret(ctx, data, packageName); err != nil {
			return fmt.Errorf("failed to create pull secret in cluster: %w", err)
		}
	}

	manager, err := olm.NewManager(kubeconfig, namespace, dryRun)
	if err != nil {
		return fmt.Errorf("failed to create OLM manager: %w", err)
	}

	opts := olm.InstallOptions{
		PackageName:    packageName,
		CatalogImage:   catalogImage,
		CatalogType:    catalogType,
		Channel:        channel,
		StartingCSV:    startingCSV,
		InstallMode:    installMode,
		Force:          installForce,
		NoWait:         noWait,
		WaitTimeout:    deploymentTimeout,
		PullSecretName: pullSecretName,
	}

	if err := manager.Install(ctx, opts); err != nil {
		return err
	}

	fmt.Printf("\nSuccessfully created OLM Subscription for %s (channel: %s)\n", packageName, channel)
	return nil
}
