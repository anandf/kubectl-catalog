package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anandf/kubectl-catalog/internal/applier"
	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/anandf/kubectl-catalog/internal/catalog"
	"github.com/anandf/kubectl-catalog/internal/certs"
	"github.com/anandf/kubectl-catalog/internal/resolver"
	"github.com/anandf/kubectl-catalog/internal/state"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

var (
	upgradeChannel string
	upgradeMode    string
	upgradeEnv     string
	upgradeDiff    bool
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade <package-name>",
	Short: "Upgrade an installed operator",
	Long: `Upgrade an operator to the latest version in the channel's upgrade graph.
The current installed version is discovered from resource annotations in the cluster.
The target version is the channel head — the bundle that the upgrade graph resolves to.
Only the target bundle is applied (intermediate versions are skipped).

Use --diff to preview what will change before applying. This shows a diff of current
vs new manifests without making any changes to the cluster.

The catalog is resolved from --ocp-version or --catalog. If neither is specified,
the catalog reference stored in the installed resource annotations is used.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		packageName := args[0]
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if dryRun {
			fmt.Println("Running in dry-run mode — no changes will be made to the cluster")
		}

		// Discover current installed version from cluster annotations
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

		if err := requirePullSecretForRedHat(catalogRef); err != nil {
			return err
		}

		puller, err := newImagePuller()
		if err != nil {
			return fmt.Errorf("failed to create image puller: %w", err)
		}

		// Verify pull secret credentials against the catalog registry before proceeding
		if pullSecretPath != "" {
			fmt.Printf("Verifying pull secret credentials against %s...\n", catalogRef)
			if err := puller.VerifyCredentials(ctx, catalogRef); err != nil {
				return fmt.Errorf("pull secret validation failed: %w", err)
			}
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
			return withHint(
				fmt.Errorf("no upgrade available: %w", err),
				"run 'kubectl catalog list --show-channels' to check available versions, or try a different --channel",
			)
		}

		if len(upgradePlan.Bundles) == 0 {
			return fmt.Errorf("no upgrade bundles resolved for %q", packageName)
		}

		// Only apply the target (head) bundle — intermediate versions are skipped.
		target := upgradePlan.Bundles[len(upgradePlan.Bundles)-1]

		fmt.Printf("Upgrade: %s -> %s\n", installed.Version, target.Version)

		// Resolve dependencies for the target version to detect new dependencies
		depPlan, err := res.Resolve(packageName, channel, target.Version)
		if err != nil {
			return fmt.Errorf("failed to resolve dependencies for target version: %w", err)
		}

		// Track newly installed dependencies for rollback if the main upgrade fails
		type installedDep struct {
			name      string
			manifests *bundle.Manifests
			applier   *applier.Applier
		}
		var newlyInstalledDeps []installedDep

		// Install any new dependencies that aren't already installed
		if len(depPlan.Bundles) > 1 {
			for _, dep := range depPlan.Bundles {
				if dep.Package == packageName {
					continue // skip the main package itself
				}
				// Check if this dependency is already installed
				depInstalled, _ := stateManager.GetInstalled(ctx, dep.Package)
				if depInstalled != nil {
					fmt.Printf("  Dependency %s v%s already installed\n", dep.Package, depInstalled.Version)
					continue
				}
				fmt.Printf("\nInstalling new dependency %s v%s...\n", dep.Name, dep.Version)

				depBundleDir, err := puller.PullBundle(ctx, dep.Image)
				if err != nil {
					return fmt.Errorf("failed to pull dependency bundle %q: %w", dep.Image, err)
				}
				depManifests, err := bundle.Extract(depBundleDir)
				if err != nil {
					return fmt.Errorf("failed to extract dependency bundle %q: %w", dep.Name, err)
				}

				depApplier, err := applier.New(kubeconfig, namespace, applierOptions())
				if err != nil {
					return fmt.Errorf("failed to create applier for dependency: %w", err)
				}

				depNS := namespace
				if !cmd.Flags().Changed("namespace") && depManifests.SuggestedNamespace != "" {
					depNS = depManifests.SuggestedNamespace
					depApplier.SetNamespace(depNS)
					if err := depApplier.EnsureNamespace(ctx, depNS); err != nil {
						return fmt.Errorf("failed to ensure namespace %q: %w", depNS, err)
					}
				}

				if err := applyInstallMode(depManifests, "", depNS); err != nil {
					return err
				}

				depIC := &applier.InstallContext{
					PackageName: dep.Package,
					Version:     dep.Version,
					Channel:     dep.Channel,
					BundleName:  dep.Name,
					BundleImage: dep.Image,
					CatalogRef:  catalogRef,
				}
				if err := depApplier.Apply(ctx, depManifests, depIC); err != nil {
					return fmt.Errorf("failed to apply dependency %q: %w", dep.Name, err)
				}

				newlyInstalledDeps = append(newlyInstalledDeps, installedDep{
					name:      dep.Name,
					manifests: depManifests,
					applier:   depApplier,
				})
			}
		}

		fmt.Printf("\nApplying %s v%s...\n", target.Name, target.Version)

		k8sApplier, err := applier.New(kubeconfig, namespace, applierOptions())
		if err != nil {
			return fmt.Errorf("failed to create applier: %w", err)
		}

		bundleDir, err := puller.PullBundle(ctx, target.Image)
		if err != nil {
			return fmt.Errorf("failed to pull bundle %q: %w", target.Image, err)
		}

		manifests, err := bundle.Extract(bundleDir)
		if err != nil {
			return fmt.Errorf("failed to extract bundle %q: %w", target.Name, err)
		}

		// Pre-flight validation
		warnings, preflightErr := k8sApplier.Preflight(ctx, manifests, target.Package)
		if preflightErr != nil {
			return fmt.Errorf("pre-flight check failed: %w", preflightErr)
		}
		for _, w := range warnings {
			fmt.Printf("  Warning: %s\n", w)
		}

		// Use the CSV's suggested namespace if --namespace was not explicitly set
		targetNamespace := namespace
		if !cmd.Flags().Changed("namespace") && manifests.SuggestedNamespace != "" {
			targetNamespace = manifests.SuggestedNamespace
			k8sApplier.SetNamespace(targetNamespace)
			fmt.Printf("  Using suggested namespace %q from bundle\n", targetNamespace)
			if err := k8sApplier.EnsureNamespace(ctx, targetNamespace); err != nil {
				return fmt.Errorf("failed to ensure namespace %q: %w", targetNamespace, err)
			}
		}

		// Create the pull secret in the resolved namespace (where the operator
		// pods will actually run) so that imagePullSecrets references resolve.
		if pullSecretPath != "" {
			if err := ensureClusterPullSecret(ctx, k8sApplier, packageName); err != nil {
				return err
			}
		}

		// Validate and apply the install mode
		if err := applyInstallMode(manifests, upgradeMode, targetNamespace); err != nil {
			return err
		}

		// Inject user-specified environment variables into all containers
		if upgradeEnv != "" {
			envVars, err := parseEnvVars(upgradeEnv)
			if err != nil {
				return err
			}
			manifests.SetEnvVars(envVars)
			fmt.Printf("  Injected %d environment variable(s) into operator containers\n", len(envVars))
		}

		// Inject imagePullSecrets into Deployment pod templates BEFORE
		// applying so pods have credentials from the moment they start.
		if pullSecretPath != "" {
			manifests.SetImagePullSecrets(applier.PullSecretName(packageName))
		}

		ic := &applier.InstallContext{
			PackageName: target.Package,
			Version:     target.Version,
			Channel:     target.Channel,
			BundleName:  target.Name,
			BundleImage: target.Image,
			CatalogRef:  catalogRef,
		}

		// On vanilla k8s, generate self-signed serving certs for services
		// that have the OpenShift serving-cert annotation, and inject
		// webhook cert volumes into Deployments.
		if isVanillaK8s() {
			if err := certs.EnsureServingCerts(ctx, kubeconfig, targetNamespace, target.Package, manifests.Services, manifests.Deployments, manifests.Other); err != nil {
				return fmt.Errorf("failed to provision serving certificates: %w", err)
			}
			webhookSecretName := bundle.WebhookCertSecretName
			if manifests.InjectWebhookCertVolumes(webhookSecretName) {
				if err := certs.EnsureWebhookCert(ctx, kubeconfig, targetNamespace, webhookSecretName, target.Package, manifests.Services, manifests.Other); err != nil {
					return fmt.Errorf("failed to provision webhook certificate: %w", err)
				}
			}
		}

		// If --diff is set, show what would change and exit without applying
		if upgradeDiff {
			fmt.Println("\n--- Diff: current vs upgrade ---")
			currentResources, resErr := stateManager.ResourcesForPackage(ctx, packageName)
			if resErr != nil {
				fmt.Printf("  Warning: could not fetch current resources: %v\n", resErr)
			}
			showUpgradeDiff(currentResources, manifests)
			fmt.Println("\nNo changes applied (--diff mode)")
			return nil
		}

		if err := k8sApplier.Apply(ctx, manifests, ic); err != nil {
			// Roll back newly installed dependencies if the main upgrade fails
			if len(newlyInstalledDeps) > 0 && !dryRun {
				fmt.Printf("\nUpgrade failed, rolling back %d newly installed dependency(ies)...\n", len(newlyInstalledDeps))
				rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer rollbackCancel()
				for i := len(newlyInstalledDeps) - 1; i >= 0; i-- {
					dep := newlyInstalledDeps[i]
					allResources := dep.manifests.AllResources()
					var asList []unstructured.Unstructured
					for _, obj := range allResources {
						asList = append(asList, *obj)
					}
					if delErr := dep.applier.DeleteResources(rollbackCtx, asList); delErr != nil {
						fmt.Printf("  Warning: rollback error for dependency %q: %v\n", dep.name, delErr)
					} else {
						fmt.Printf("  Rolled back dependency %q\n", dep.name)
					}
				}
			}
			return fmt.Errorf("failed to apply bundle %q: %w", target.Name, err)
		}

		// Also patch ServiceAccounts so that any pods created later
		// (e.g., by the operator itself) inherit the pull secret.
		if pullSecretPath != "" {
			if err := patchBundleServiceAccounts(ctx, k8sApplier, manifests, packageName); err != nil {
				return err
			}
		}

		fmt.Printf("\nSuccessfully upgraded %s to v%s\n", packageName, target.Version)
		return nil
	},
}

// showUpgradeDiff compares current cluster resources with the new bundle manifests
// and prints a summary of additions, removals, and changes.
func showUpgradeDiff(currentResources []unstructured.Unstructured, newManifests *bundle.Manifests) {
	// Build maps by kind/name for comparison
	type resourceKey struct {
		kind string
		name string
	}

	currentByKey := make(map[resourceKey]*unstructured.Unstructured)
	for i := range currentResources {
		r := &currentResources[i]
		key := resourceKey{kind: r.GetKind(), name: r.GetName()}
		currentByKey[key] = r
	}

	newResources := newManifests.AllResources()
	newByKey := make(map[resourceKey]*unstructured.Unstructured)
	for _, r := range newResources {
		key := resourceKey{kind: r.GetKind(), name: r.GetName()}
		newByKey[key] = r
	}

	// Find additions and changes
	var added, changed, unchanged []string
	for key, newRes := range newByKey {
		label := fmt.Sprintf("%s/%s", key.kind, key.name)
		currentRes, exists := currentByKey[key]
		if !exists {
			added = append(added, label)
			continue
		}

		// Compare YAML representations (strip metadata that changes between versions)
		if resourceDiffers(currentRes, newRes) {
			changed = append(changed, label)
		} else {
			unchanged = append(unchanged, label)
		}
	}

	// Find removals
	var removed []string
	for key := range currentByKey {
		if _, exists := newByKey[key]; !exists {
			removed = append(removed, fmt.Sprintf("%s/%s", key.kind, key.name))
		}
	}

	// Sort for stable output
	sortStrings(added, changed, removed, unchanged)

	if len(added) > 0 {
		fmt.Printf("\n  Added (%d):\n", len(added))
		for _, r := range added {
			fmt.Printf("    + %s\n", r)
		}
	}

	if len(removed) > 0 {
		fmt.Printf("\n  Removed (%d):\n", len(removed))
		for _, r := range removed {
			fmt.Printf("    - %s\n", r)
		}
	}

	if len(changed) > 0 {
		fmt.Printf("\n  Changed (%d):\n", len(changed))
		for _, r := range changed {
			fmt.Printf("    ~ %s\n", r)
		}
	}

	if len(unchanged) > 0 {
		fmt.Printf("\n  Unchanged (%d):\n", len(unchanged))
		for _, r := range unchanged {
			fmt.Printf("      %s\n", r)
		}
	}

	fmt.Printf("\nSummary: %d added, %d removed, %d changed, %d unchanged\n",
		len(added), len(removed), len(changed), len(unchanged))
}

// resourceDiffers compares two resources by their spec content, ignoring
// metadata fields that naturally differ (resourceVersion, uid, timestamps, etc.).
func resourceDiffers(current, new *unstructured.Unstructured) bool {
	// Compare the spec/data/rules sections — the parts that actually matter
	fieldsToCompare := []string{"spec", "data", "rules", "roleRef", "subjects", "webhooks"}

	for _, field := range fieldsToCompare {
		currentVal, currentFound := current.Object[field]
		newVal, newFound := new.Object[field]
		if currentFound != newFound {
			return true
		}
		if !currentFound {
			continue
		}
		currentYAML, _ := yaml.Marshal(currentVal)
		newYAML, _ := yaml.Marshal(newVal)
		if string(currentYAML) != string(newYAML) {
			return true
		}
	}

	// Also compare annotations that we care about (version, channel, etc.)
	currentAnn := current.GetAnnotations()
	newAnn := new.GetAnnotations()
	for _, key := range []string{state.AnnVersion, state.AnnChannel, state.AnnBundle, state.AnnBundleImage} {
		if currentAnn[key] != newAnn[key] {
			return true
		}
	}

	// Compare container images in deployments
	if current.GetKind() == "Deployment" {
		currentImages := extractContainerImages(current)
		newImages := extractContainerImages(new)
		if strings.Join(currentImages, ",") != strings.Join(newImages, ",") {
			return true
		}
	}

	return false
}

// extractContainerImages returns a sorted list of container images from a Deployment.
func extractContainerImages(dep *unstructured.Unstructured) []string {
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	var images []string
	for _, c := range containers {
		cMap, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if img, ok := cMap["image"].(string); ok {
			images = append(images, img)
		}
	}
	return images
}

func sortStrings(slices ...[]string) {
	for _, s := range slices {
		sort.Strings(s)
	}
}

func init() {
	upgradeCmd.Flags().StringVar(&upgradeChannel, "channel", "", "switch to a different channel for upgrade")
	upgradeCmd.Flags().StringVar(&upgradeMode, "install-mode", "", "install mode: AllNamespaces, SingleNamespace, OwnNamespace (defaults to operator's preferred mode)")
	upgradeCmd.Flags().StringVar(&upgradeEnv, "env", "", "comma-separated environment variables to inject into operator containers (e.g. KEY1=val1,KEY2=val2)")
	upgradeCmd.Flags().BoolVar(&upgradeDiff, "diff", false, "show diff of current vs new manifests without applying")
	upgradeCmd.ValidArgsFunction = completeInstalledPackages
	rootCmd.AddCommand(upgradeCmd)
}
