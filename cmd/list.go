package cmd

import (
	"context"
	"fmt"

	"github.com/anandf/kubectl-catalog/internal/catalog"
	"github.com/anandf/kubectl-catalog/internal/state"
	"github.com/spf13/cobra"
)

var (
	listInstalled    bool
	showChannels     bool
	limitChannels    int
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List available or installed operators",
	Long: `List operators available in a catalog, or list operators currently
installed in the cluster (discovered via resource annotations).

Use --show-channels to display channel details for each package.
Use --limit-channels to cap the number of channels shown per package.

The catalog is resolved from --ocp-version or --catalog.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		if listInstalled {
			return listInstalledOperators(ctx)
		}
		return listAvailableOperators(ctx)
	},
}

func listInstalledOperators(ctx context.Context) error {
	stateManager, err := state.NewManager(kubeconfig, namespace)
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	operators, err := stateManager.ListInstalled(ctx)
	if err != nil {
		return fmt.Errorf("failed to list installed operators: %w", err)
	}

	if len(operators) == 0 {
		fmt.Println("No operators installed.")
		return nil
	}

	fmt.Printf("%-35s %-15s %-15s %-10s %s\n", "PACKAGE", "VERSION", "CHANNEL", "RESOURCES", "CATALOG")
	fmt.Printf("%-35s %-15s %-15s %-10s %s\n", "-------", "-------", "-------", "---------", "-------")
	for _, op := range operators {
		fmt.Printf("%-35s %-15s %-15s %-10d %s\n",
			op.PackageName, op.Version, op.Channel, len(op.Resources), op.CatalogRef)
		if op.Warning != "" {
			fmt.Printf("  WARNING: %s\n", op.Warning)
		}
	}
	return nil
}

func listAvailableOperators(ctx context.Context) error {
	imageRef, err := resolveCatalogImage("")
	if err != nil {
		return err
	}

	puller, err := newImagePuller()
	if err != nil {
		return fmt.Errorf("failed to create image puller: %w", err)
	}

	fbc, err := catalog.Load(ctx, imageRef, refreshCache, puller)
	if err != nil {
		return fmt.Errorf("failed to load catalog: %w", err)
	}

	if showChannels {
		return listWithChannels(fbc)
	}

	fmt.Printf("%-45s %-20s\n", "PACKAGE", "DEFAULT CHANNEL")
	fmt.Printf("%-45s %-20s\n", "-------", "---------------")
	for _, pkg := range fbc.Packages {
		fmt.Printf("%-45s %-20s\n", pkg.Name, pkg.DefaultChannel)
	}
	return nil
}

func listWithChannels(fbc *catalog.FBC) error {
	for _, pkg := range fbc.Packages {
		channels := fbc.ChannelsForPackage(pkg.Name)

		fmt.Printf("%-45s (default: %s)\n", pkg.Name, pkg.DefaultChannel)

		displayed := channels
		if limitChannels > 0 && len(displayed) > limitChannels {
			displayed = displayed[:limitChannels]
		}

		for _, ch := range displayed {
			head := findChannelHead(ch)
			marker := ""
			if ch.Name == pkg.DefaultChannel {
				marker = " *"
			}
			fmt.Printf("  %-25s %d entries, head: %s%s\n", ch.Name, len(ch.Entries), head, marker)
		}

		if limitChannels > 0 && len(channels) > limitChannels {
			fmt.Printf("  ... and %d more channel(s)\n", len(channels)-limitChannels)
		}

		fmt.Println()
	}
	return nil
}

// findChannelHead returns the name of the entry at the head of the channel
// (the entry that is not replaced or skipped by any other entry).
func findChannelHead(ch catalog.Channel) string {
	replaced := make(map[string]bool)
	for _, entry := range ch.Entries {
		if entry.Replaces != "" {
			replaced[entry.Replaces] = true
		}
		for _, skip := range entry.Skips {
			replaced[skip] = true
		}
	}
	for _, entry := range ch.Entries {
		if !replaced[entry.Name] {
			return entry.Name
		}
	}
	if len(ch.Entries) > 0 {
		return ch.Entries[0].Name
	}
	return ""
}

func init() {
	listCmd.Flags().BoolVar(&listInstalled, "installed", false, "list installed operators (discovered from cluster annotations)")
	listCmd.Flags().BoolVar(&showChannels, "show-channels", false, "show channel details for each package")
	listCmd.Flags().IntVar(&limitChannels, "limit-channels", 0, "limit number of channels shown per package (0 = no limit)")
	rootCmd.AddCommand(listCmd)
}
