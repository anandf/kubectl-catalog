package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/anandf/kubectl-catalog/pkg/applier"
	"github.com/anandf/kubectl-catalog/pkg/state"
	"github.com/spf13/cobra"
)

var (
	uninstallYes   bool
	uninstallForce bool
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall <package-name>",
	Short: "Uninstall an installed operator",
	Long: `Remove all resources that were installed as part of an operator bundle.
Resources are discovered by their kubectl-catalog tracking labels in the cluster.

By default, CRDs and their custom resource instances are preserved to protect
user data. Use --force to also remove CRDs and custom resources (with confirmation).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		packageName := args[0]
		ctx := context.Background()

		stateManager, err := state.NewManager(kubeconfig, namespace)
		if err != nil {
			return fmt.Errorf("failed to create state manager: %w", err)
		}

		installed, err := stateManager.GetInstalled(ctx, packageName)
		if err != nil {
			return fmt.Errorf("package %q is not installed: %w", packageName, err)
		}

		// Get all resources belonging to this package
		resources, err := stateManager.ResourcesForPackage(ctx, packageName)
		if err != nil {
			return fmt.Errorf("failed to find resources: %w", err)
		}

		// Partition resources into CRDs, CRs (instances of those CRDs), and operational resources
		crds, crs, operational := partitionResources(resources)

		fmt.Printf("Will uninstall %s v%s\n", packageName, installed.Version)
		fmt.Printf("  Operational resources to remove: %d\n", len(operational))
		if len(crds) > 0 {
			fmt.Printf("  CRDs to preserve: %d\n", len(crds))
			for _, crd := range crds {
				fmt.Printf("    - %s\n", crd.GetName())
			}
		}
		if len(crs) > 0 {
			fmt.Printf("  Custom resources to preserve: %d\n", len(crs))
			for _, cr := range crs {
				fmt.Printf("    - %s/%s\n", cr.GetKind(), cr.GetName())
			}
		}

		if !uninstallYes {
			fmt.Print("\nContinue? [y/N] ")
			if !promptConfirm() {
				fmt.Println("Aborted.")
				return nil
			}
		}

		k8sApplier, err := applier.New(kubeconfig, namespace)
		if err != nil {
			return fmt.Errorf("failed to create applier: %w", err)
		}

		// Always delete operational resources (Deployments, RBAC, Services, etc.)
		fmt.Printf("\nRemoving operational resources...\n")
		if err := k8sApplier.DeleteResources(ctx, operational); err != nil {
			return fmt.Errorf("failed to delete resources: %w", err)
		}

		// With --force, prompt separately for CRDs and CRs
		if uninstallForce && (len(crds) > 0 || len(crs) > 0) {
			fmt.Printf("\n--force specified. The following CRDs and custom resources will be PERMANENTLY deleted:\n")
			for _, crd := range crds {
				fmt.Printf("  CRD: %s\n", crd.GetName())
			}
			for _, cr := range crs {
				fmt.Printf("  CR:  %s/%s (%s)\n", cr.GetKind(), cr.GetName(), cr.GetNamespace())
			}
			fmt.Print("\nThis will destroy all custom resource data. Are you sure? [y/N] ")
			if promptConfirm() {
				fmt.Printf("\nRemoving custom resources...\n")
				if err := k8sApplier.DeleteResources(ctx, crs); err != nil {
					return fmt.Errorf("failed to delete custom resources: %w", err)
				}
				fmt.Printf("Removing CRDs...\n")
				if err := k8sApplier.DeleteResources(ctx, crds); err != nil {
					return fmt.Errorf("failed to delete CRDs: %w", err)
				}
			} else {
				fmt.Println("Skipping CRD and custom resource deletion.")
			}
		} else if len(crds) > 0 || len(crs) > 0 {
			fmt.Printf("\nCRDs and custom resources were preserved. Use --force to remove them.\n")
		}

		fmt.Printf("\nSuccessfully uninstalled %s\n", packageName)
		return nil
	},
}

// partitionResources splits resources into CRDs, custom resources (instances of those CRDs),
// and operational resources (Deployments, RBAC, Services, etc.).
func partitionResources(resources []unstructured.Unstructured) (crds, crs, operational []unstructured.Unstructured) {
	// First pass: identify CRDs and collect their group/kind info
	crdGroups := make(map[string]bool) // "group/kind" -> true
	for _, r := range resources {
		if r.GetKind() == "CustomResourceDefinition" {
			crds = append(crds, r)

			// Extract the group and kind this CRD defines
			group, _, _ := unstructured.NestedString(r.Object, "spec", "group")
			names, _, _ := unstructured.NestedMap(r.Object, "spec", "names")
			if names != nil {
				if kind, ok := names["kind"].(string); ok && group != "" {
					crdGroups[group+"/"+kind] = true
				}
			}
		}
	}

	// Second pass: classify non-CRD resources
	for _, r := range resources {
		if r.GetKind() == "CustomResourceDefinition" {
			continue
		}

		gvk := r.GroupVersionKind()
		key := gvk.Group + "/" + gvk.Kind
		if crdGroups[key] {
			crs = append(crs, r)
		} else {
			operational = append(operational, r)
		}
	}

	return crds, crs, operational
}

func promptConfirm() bool {
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	return strings.TrimSpace(strings.ToLower(answer)) == "y"
}

func init() {
	uninstallCmd.Flags().BoolVarP(&uninstallYes, "yes", "y", false, "skip initial confirmation prompt")
	uninstallCmd.Flags().BoolVar(&uninstallForce, "force", false, "also remove CRDs and custom resources (with confirmation)")
	rootCmd.AddCommand(uninstallCmd)
}
