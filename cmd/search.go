package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/anandf/kubectl-catalog/internal/catalog"
	"github.com/spf13/cobra"
)

var searchWhatProvides string

var searchCmd = &cobra.Command{
	Use:   "search [keyword]",
	Short: "Search for operators in a catalog",
	Long: `Search for operators by name or keyword in a catalog.
If no keyword is given, all packages are listed.

Use --what-provides to find operators that provide a specific CRD (GVK).

Supported GVK query formats:
  group/version/kind     e.g. argoproj.io/v1alpha1/ArgoCD
  group_version_kind     e.g. argoproj.io_v1alpha1_ArgoCD
  group/kind             e.g. argoproj.io/ArgoCD   (matches any version)
  group_kind             e.g. argoproj.io_ArgoCD   (matches any version)
  kind                   e.g. ArgoCD               (matches any group/version)

The catalog is resolved from --ocp-version or --catalog.

Examples:
  # Search by keyword
  kubectl catalog search logging --ocp-version 4.20

  # Find operators providing a specific CRD
  kubectl catalog search --what-provides argoproj.io/ArgoCD --ocp-version 4.20

  # Search with full GVK
  kubectl catalog search --what-provides argoproj.io/v1alpha1/ArgoCD --ocp-version 4.20

  # Search by kind only (broad search)
  kubectl catalog search --what-provides ArgoCD --ocp-version 4.20`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		if searchWhatProvides != "" && len(args) > 0 {
			return fmt.Errorf("cannot use both a keyword argument and --what-provides; use one or the other")
		}

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

		if searchWhatProvides != "" {
			return searchByGVK(fbc, searchWhatProvides)
		}

		return searchByKeyword(fbc, args)
	},
}

func searchByKeyword(fbc *catalog.FBC, args []string) error {
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
}

func searchByGVK(fbc *catalog.FBC, queryStr string) error {
	query := catalog.ParseGVKQuery(queryStr)

	// Show what we're searching for
	searchDesc := query.Kind
	if query.Group != "" {
		searchDesc = query.Group + "/" + searchDesc
		if query.Version != "" {
			searchDesc = query.Group + "/" + query.Version + "/" + query.Kind
		}
	}
	fmt.Printf("Searching for operators providing %s...\n\n", searchDesc)

	results := fbc.FindGVKProviders(query)

	if len(results) == 0 {
		fmt.Printf("No operators found providing %s.\n", searchDesc)
		fmt.Println("Hint: check the spelling and try a broader query (e.g. just the Kind name)")
		return nil
	}

	fmt.Printf("%-45s %-20s %s\n", "NAME", "DEFAULT CHANNEL", "PROVIDED GVK")
	fmt.Printf("%-45s %-20s %s\n", "----", "---------------", "------------")

	for _, r := range results {
		for i, gvk := range r.MatchedGVKs {
			name := r.PackageName
			channel := r.DefaultChannel
			if i > 0 {
				// Continuation line — blank out name and channel columns
				name = ""
				channel = ""
			}
			fmt.Printf("%-45s %-20s %s\n", name, channel, gvk.String())
		}
	}

	fmt.Printf("\n%d operator(s) found.\n", len(results))
	return nil
}

func init() {
	searchCmd.Flags().StringVar(&searchWhatProvides, "what-provides", "", "find operators providing a specific GVK (e.g. argoproj.io/ArgoCD)")
	rootCmd.AddCommand(searchCmd)
}
