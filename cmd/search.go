package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/anandf/kubectl-catalog/internal/catalog"
	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search [keyword]",
	Short: "Search for operators in a catalog",
	Long: `Search for operators by name or keyword in a catalog.
If no keyword is given, all packages are listed.

The catalog is resolved from --ocp-version or --catalog.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

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

		keyword := ""
		if len(args) > 0 {
			keyword = strings.ToLower(args[0])
		}

		fmt.Printf("%-45s %-20s %s\n", "NAME", "DEFAULT CHANNEL", "DESCRIPTION")
		fmt.Printf("%-45s %-20s %s\n", "----", "---------------", "-----------")

		for _, pkg := range fbc.Packages {
			if keyword != "" && !strings.Contains(strings.ToLower(pkg.Name), keyword) &&
				!strings.Contains(strings.ToLower(pkg.Description), keyword) {
				continue
			}

			desc := pkg.Description
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			fmt.Printf("%-45s %-20s %s\n", pkg.Name, pkg.DefaultChannel, desc)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(searchCmd)
}
