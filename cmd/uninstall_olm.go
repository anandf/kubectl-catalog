package cmd

import (
	"context"
	"fmt"

	"github.com/anandf/kubectl-catalog/internal/olm"
	"github.com/spf13/cobra"
)

func uninstallViaOLM(cmd *cobra.Command, ctx context.Context, packageName string) error {
	manager, err := olm.NewManager(kubeconfig, namespace, dryRun)
	if err != nil {
		return fmt.Errorf("failed to create OLM manager: %w", err)
	}

	fmt.Printf("Uninstalling %s via OLM...\n", packageName)

	if err := manager.Uninstall(ctx, packageName); err != nil {
		return err
	}

	fmt.Printf("\nSuccessfully uninstalled %s (OLM resources removed)\n", packageName)
	return nil
}
