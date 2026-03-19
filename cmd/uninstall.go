package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/anandf/kubectl-catalog/internal/applier"
	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/anandf/kubectl-catalog/internal/state"
	"github.com/spf13/cobra"
)

var (
	uninstallForce bool
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall <package-name>",
	Short: "Uninstall an installed operator",
	Long: `Remove all resources that were installed as part of an operator bundle.
Resources are discovered by their kubectl-catalog tracking labels in the cluster.

Operational resources (Deployments, RBAC, Services, etc.) are removed immediately.
CRDs and their custom resource instances are preserved by default to protect user data.

Use --force to also remove CRDs and custom resources. Since deleting CRDs permanently
destroys all custom resource instances across the cluster, a separate confirmation
prompt requires you to type "yes" before proceeding.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		packageName := args[0]
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if dryRun {
			fmt.Println("Running in dry-run mode — no changes will be made to the cluster")
		}

		stateManager, err := state.NewManager(kubeconfig, namespace)
		if err != nil {
			return fmt.Errorf("failed to create state manager: %w", err)
		}

		installed, err := stateManager.GetInstalled(ctx, packageName)
		if err != nil {
			return withHint(
				fmt.Errorf("package %q is not installed: %w", packageName, err),
				"run 'kubectl catalog list --installed' to see installed packages",
			)
		}

		// Get all resources belonging to this package (searches all namespaces)
		resources, err := stateManager.ResourcesForPackage(ctx, packageName)
		if err != nil {
			return fmt.Errorf("failed to find resources: %w", err)
		}

		// Determine the actual namespace where the operator was installed.
		// This may differ from --namespace if the operator used a suggested namespace.
		operatorNamespace := determineOperatorNamespace(resources)
		targetNamespace := namespace
		if operatorNamespace != "" {
			targetNamespace = operatorNamespace
			if targetNamespace != namespace {
				fmt.Printf("Operator %q is installed in namespace %q\n", packageName, targetNamespace)
			}
		}

		// Partition resources into CRDs, CRs (instances of those CRDs), and operational resources
		crds, crs, operational := partitionResources(resources)

		fmt.Printf("Will uninstall %s v%s\n", packageName, installed.Version)
		fmt.Printf("  Namespace: %s\n", targetNamespace)
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

		// Use the discovered namespace for the applier so it can find and
		// delete resources like the pull secret in the correct namespace.
		k8sApplier, err := applier.New(kubeconfig, targetNamespace, applierOptions())
		if err != nil {
			return fmt.Errorf("failed to create applier: %w", err)
		}

		// Always delete operational resources (Deployments, RBAC, Services, etc.)
		fmt.Printf("\nRemoving operational resources...\n")
		if err := k8sApplier.DeleteResources(ctx, operational); err != nil {
			return fmt.Errorf("failed to delete resources: %w", err)
		}

		// Clean up webhook configurations that reference services in the operator namespace
		webhooksCleaned, cleanupErr := k8sApplier.CleanupWebhookConfigurations(ctx, targetNamespace)
		if cleanupErr != nil {
			fmt.Printf("  Warning: failed to clean up webhook configurations: %v\n", cleanupErr)
		} else if webhooksCleaned > 0 {
			fmt.Printf("  Cleaned up %d stale webhook configuration(s)\n", webhooksCleaned)
		}

		// Remove the pull secret created for image pulls (if it exists)
		if err := k8sApplier.DeletePullSecret(ctx, packageName); err != nil {
			return fmt.Errorf("failed to delete pull secret: %w", err)
		}

		// With --force, prompt for CRD and CR removal with a clear warning
		if uninstallForce && (len(crds) > 0 || len(crs) > 0) {
			fmt.Printf("\n╔══════════════════════════════════════════════════════════════╗\n")
			fmt.Printf("║  WARNING: IRREVOCABLE DATA DESTRUCTION                      ║\n")
			fmt.Printf("╚══════════════════════════════════════════════════════════════╝\n")
			fmt.Printf("\n--force specified. The following CRDs and custom resources will be PERMANENTLY deleted:\n\n")
			if len(crds) > 0 {
				fmt.Printf("  CRDs (%d):\n", len(crds))
				for _, crd := range crds {
					fmt.Printf("    - %s\n", crd.GetName())
				}
			}
			if len(crs) > 0 {
				fmt.Printf("  Custom Resources (%d):\n", len(crs))
				for _, cr := range crs {
					fmt.Printf("    - %s/%s (%s)\n", cr.GetKind(), cr.GetName(), cr.GetNamespace())
				}
			}
			fmt.Printf("\nDeleting CRDs will remove ALL custom resource instances of these types\n")
			fmt.Printf("across the ENTIRE cluster, including resources created by users.\n")
			fmt.Printf("This operation CANNOT be undone.\n")
			fmt.Print("\nType 'yes' to confirm deletion: ")
			if promptConfirmFull() {
				if len(crs) > 0 {
					fmt.Printf("\nRemoving custom resources...\n")
					if err := k8sApplier.DeleteResources(ctx, crs); err != nil {
						return fmt.Errorf("failed to delete custom resources: %w", err)
					}
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

		// Cross-reference with bundle manifests to find untracked resources
		cleanupUntrackedResources(ctx, k8sApplier, installed, resources)

		// Clean up the namespace if it was created by kubectl-catalog
		if operatorNamespace != "" {
			if err := k8sApplier.DeleteNamespace(ctx, operatorNamespace); err != nil {
				return fmt.Errorf("failed to delete namespace %q: %w", operatorNamespace, err)
			}
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

// cleanupUntrackedResources re-extracts the bundle manifests and attempts to
// delete any resources that exist in the bundle but were not found via tracking
// annotations. Errors are logged but not treated as fatal, since these resources
// may have already been removed or never created.
func cleanupUntrackedResources(ctx context.Context, k8sApplier *applier.Applier, installed *state.InstalledOperator, trackedResources []unstructured.Unstructured) {
	if installed.BundleImage == "" {
		return
	}

	puller, err := newImagePuller()
	if err != nil {
		fmt.Printf("  Warning: could not create image puller for bundle cleanup: %v\n", err)
		return
	}

	bundleDir, err := puller.PullBundle(ctx, installed.BundleImage)
	if err != nil {
		fmt.Printf("  Warning: could not pull bundle %q for cleanup: %v\n", installed.BundleImage, err)
		fmt.Printf("  The following cluster-scoped resource types may be orphaned:\n")
		fmt.Printf("    - CustomResourceDefinitions\n")
		fmt.Printf("    - ClusterRoles / ClusterRoleBindings\n")
		fmt.Printf("    - ValidatingWebhookConfigurations / MutatingWebhookConfigurations\n")
		fmt.Printf("  To find them, run:\n")
		fmt.Printf("    kubectl get crd,clusterrole,clusterrolebinding -l kubectl-catalog.io/package=%s\n", installed.PackageName)
		return
	}

	manifests, err := bundle.Extract(bundleDir)
	if err != nil {
		fmt.Printf("  Warning: could not extract bundle %q for cleanup: %v\n", installed.BundleImage, err)
		fmt.Printf("  The following cluster-scoped resource types may be orphaned:\n")
		fmt.Printf("    - CustomResourceDefinitions\n")
		fmt.Printf("    - ClusterRoles / ClusterRoleBindings\n")
		fmt.Printf("    - ValidatingWebhookConfigurations / MutatingWebhookConfigurations\n")
		fmt.Printf("  To find them, run:\n")
		fmt.Printf("    kubectl get crd,clusterrole,clusterrolebinding -l kubectl-catalog.io/package=%s\n", installed.PackageName)
		return
	}

	// Build a set of tracked resource keys for quick lookup
	trackedKeys := make(map[string]bool)
	for _, r := range trackedResources {
		trackedKeys[resourceKey(r.GetKind(), r.GetName(), r.GetNamespace())] = true
	}

	// Check each resource from the bundle manifests
	var untracked []*unstructured.Unstructured
	for _, obj := range manifests.AllResources() {
		key := resourceKey(obj.GetKind(), obj.GetName(), obj.GetNamespace())
		if !trackedKeys[key] {
			untracked = append(untracked, obj)
		}
	}

	if len(untracked) == 0 {
		return
	}

	fmt.Printf("\nFound %d resource(s) from bundle manifests without tracking annotations, attempting cleanup...\n", len(untracked))
	for _, obj := range untracked {
		asSlice := []unstructured.Unstructured{*obj}
		if err := k8sApplier.DeleteResources(ctx, asSlice); err != nil {
			fmt.Printf("  Warning: could not delete %s/%s: %v (ignoring)\n", obj.GetKind(), obj.GetName(), err)
		}
	}
}

// determineOperatorNamespace finds the namespace used by the operator's namespaced
// resources (typically the suggested namespace from the CSV). Returns the most
// common namespace among the tracked resources, excluding cluster-scoped resources.
func determineOperatorNamespace(resources []unstructured.Unstructured) string {
	nsCounts := make(map[string]int)
	for _, r := range resources {
		ns := r.GetNamespace()
		if ns != "" {
			nsCounts[ns]++
		}
	}

	var bestNS string
	var bestCount int
	for ns, count := range nsCounts {
		if count > bestCount {
			bestNS = ns
			bestCount = count
		}
	}
	return bestNS
}

func resourceKey(kind, name, namespace string) string {
	if namespace != "" {
		return kind + "/" + namespace + "/" + name
	}
	return kind + "/" + name
}

// promptConfirmFull requires the user to type "yes" (not just "y") to confirm
// irrevocable destructive operations like CRD deletion.
func promptConfirmFull() bool {
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	return strings.TrimSpace(strings.ToLower(answer)) == "yes"
}

func init() {
	uninstallCmd.Flags().BoolVar(&uninstallForce, "force", false, "also remove CRDs and custom resources (requires typing 'yes' to confirm)")
	uninstallCmd.ValidArgsFunction = completeInstalledPackages
	rootCmd.AddCommand(uninstallCmd)
}
