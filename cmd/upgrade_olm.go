package cmd

import (
	"context"
	"fmt"

	"github.com/anandf/kubectl-catalog/internal/olm"
	"github.com/spf13/cobra"
)

func upgradeViaOLM(_ *cobra.Command, ctx context.Context, packageName string) error {
	if upgradeDiff {
		fmt.Println("Warning: --diff is not supported with --installation-type=olm")
	}
	if upgradeEnv != "" {
		fmt.Println("Warning: --env is not supported with --installation-type=olm (use Subscription spec.config.env instead)")
	}

	manager, err := olm.NewManager(kubeconfig, namespace, dryRun)
	if err != nil {
		return fmt.Errorf("failed to create OLM manager: %w", err)
	}

	opts := olm.UpgradeOptions{
		PackageName: packageName,
		Channel:     upgradeChannel,
		NoWait:      noWait,
		WaitTimeout: deploymentTimeout,
	}

	if err := manager.Upgrade(ctx, opts); err != nil {
		return err
	}

	fmt.Printf("\nSuccessfully updated OLM Subscription for %s\n", packageName)
	return nil
}
