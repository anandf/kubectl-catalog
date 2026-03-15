package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	cleanCatalogs bool
	cleanBundles  bool
	cleanAll      bool
)

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove cached catalog and bundle data",
	Long: `Remove cached data stored in the cache directory (default: ~/.kubectl-catalog).

By default, both catalogs and bundles are removed. Use --catalogs or --bundles
to selectively clean only one type.

The cache directory can be changed with the global --cache-dir flag.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// If neither flag is set, clean everything
		if !cleanCatalogs && !cleanBundles {
			cleanAll = true
		}

		if cleanAll {
			size, err := dirSize(cacheDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("Nothing to clean.")
					return nil
				}
				return fmt.Errorf("reading cache directory: %w", err)
			}
			if err := os.RemoveAll(cacheDir); err != nil {
				return fmt.Errorf("removing cache directory: %w", err)
			}
			fmt.Printf("Removed %s (%s)\n", cacheDir, formatSize(size))
			return nil
		}

		var dirs []string
		if cleanCatalogs {
			dirs = append(dirs, filepath.Join(cacheDir, "catalogs"))
		}
		if cleanBundles {
			dirs = append(dirs, filepath.Join(cacheDir, "bundles"))
		}

		for _, dir := range dirs {
			size, err := dirSize(dir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Printf("No cached data at %s\n", dir)
					continue
				}
				return fmt.Errorf("reading %s: %w", dir, err)
			}
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("removing %s: %w", dir, err)
			}
			fmt.Printf("Removed %s (%s)\n", dir, formatSize(size))
		}
		return nil
	},
}

func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func formatSize(bytes int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func init() {
	cleanCmd.Flags().BoolVar(&cleanCatalogs, "catalogs", false, "only remove cached catalogs")
	cleanCmd.Flags().BoolVar(&cleanBundles, "bundles", false, "only remove cached bundles")
	rootCmd.AddCommand(cleanCmd)
}
