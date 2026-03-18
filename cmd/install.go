package cmd

import (
	"context"
	"fmt"

	"github.com/anandf/kubectl-catalog/internal/applier"
	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/anandf/kubectl-catalog/internal/catalog"
	"github.com/anandf/kubectl-catalog/internal/certs"
	"github.com/anandf/kubectl-catalog/internal/registry"
	"github.com/anandf/kubectl-catalog/internal/resolver"
	"github.com/anandf/kubectl-catalog/internal/state"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	installChannel string
	installVersion string
	installForce   bool
	installMode    string
	installEnv     string
)

var installCmd = &cobra.Command{
	Use:   "install <package-name>",
	Short: "Install an operator from a catalog",
	Long: `Install an operator by pulling its bundle from the catalog, resolving
dependencies, extracting manifests, and applying them to the target cluster.

The catalog is resolved from --ocp-version or --catalog.
If --channel is not specified, the package's default channel is used.
If --version is not specified, the latest version (channel head) is installed.

If the operator is already installed, use --force to re-install.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		packageName := args[0]
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if dryRun {
			fmt.Println("Running in dry-run mode — no changes will be made to the cluster")
		}

		// Check if already installed
		stateManager, err := state.NewManager(kubeconfig, namespace)
		if err != nil {
			return fmt.Errorf("failed to create state manager: %w", err)
		}

		installed, getErr := stateManager.GetInstalled(ctx, packageName)
		if getErr == nil && installed != nil {
			if !installForce {
				return fmt.Errorf("package %q is already installed (v%s, channel %s); use --force to re-install",
					packageName, installed.Version, installed.Channel)
			}
			fmt.Printf("Package %q already installed (v%s), re-installing with --force\n",
				packageName, installed.Version)
		}

		catalogImage, err := resolveCatalogImage("")
		if err != nil {
			return err
		}

		if err := requirePullSecretForRedHat(catalogImage); err != nil {
			return err
		}

		puller, err := newImagePuller()
		if err != nil {
			return fmt.Errorf("failed to create image puller: %w", err)
		}

		// Verify pull secret credentials against the catalog registry before proceeding
		if pullSecretPath != "" {
			fmt.Printf("Verifying pull secret credentials against %s...\n", catalogImage)
			if err := puller.VerifyCredentials(ctx, catalogImage); err != nil {
				return fmt.Errorf("pull secret validation failed: %w", err)
			}
		}

		fbc, err := catalog.Load(ctx, catalogImage, refreshCache, puller)
		if err != nil {
			return fmt.Errorf("failed to load catalog %q: %w", catalogImage, err)
		}

		// Resolve the bundle and its dependencies
		res := resolver.New(fbc)
		installPlan, err := res.Resolve(packageName, installChannel, installVersion)
		if err != nil {
			return withHint(
				fmt.Errorf("failed to resolve %q: %w", packageName, err),
				"run 'kubectl catalog search <keyword>' to find available packages, or 'kubectl catalog list --show-channels' to see channels and versions",
			)
		}

		fmt.Printf("Resolved install plan: %d bundle(s) to install\n", len(installPlan.Bundles))
		for _, b := range installPlan.Bundles {
			fmt.Printf("  - %s v%s (from %s)\n", b.Name, b.Version, b.Image)
		}

		k8sApplier, err := applier.New(kubeconfig, namespace, applierOptions())
		if err != nil {
			return fmt.Errorf("failed to create applier: %w", err)
		}

		namespaceExplicit := cmd.Flags().Changed("namespace")

		// Pre-flight: pull the first bundle to validate CRDs and permissions
		fmt.Println("\nRunning pre-flight checks...")
		firstBundle := installPlan.Bundles[len(installPlan.Bundles)-1] // main package (last in dependency order)
		preflightDir, err := puller.PullBundle(ctx, firstBundle.Image)
		if err != nil {
			return fmt.Errorf("failed to pull bundle for pre-flight: %w", err)
		}
		preflightManifests, err := bundle.Extract(preflightDir)
		if err != nil {
			return fmt.Errorf("failed to extract bundle for pre-flight: %w", err)
		}
		warnings, err := k8sApplier.Preflight(ctx, preflightManifests, firstBundle.Package)
		if err != nil {
			return fmt.Errorf("pre-flight check failed: %w", err)
		}
		for _, w := range warnings {
			fmt.Printf("  Warning: %s\n", w)
		}
		fmt.Println("  Pre-flight checks passed")

		// Track all successfully applied manifests for rollback on failure
		var appliedManifests []*bundle.Manifests

		installErr := func() error {
			for _, b := range installPlan.Bundles {
				fmt.Printf("\nInstalling %s v%s...\n", b.Name, b.Version)

				bundleDir, err := puller.PullBundle(ctx, b.Image)
				if err != nil {
					return fmt.Errorf("failed to pull bundle %q: %w", b.Image, err)
				}

				manifests, err := bundle.Extract(bundleDir)
				if err != nil {
					return fmt.Errorf("failed to extract bundle %q: %w", b.Name, err)
				}

				// Use the CSV's suggested namespace if --namespace was not explicitly set
				targetNamespace := namespace
				if !namespaceExplicit && manifests.SuggestedNamespace != "" {
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
				if err := applyInstallMode(manifests, installMode, targetNamespace); err != nil {
					return err
				}

				// Inject user-specified environment variables into all containers
				if installEnv != "" {
					envVars, err := parseEnvVars(installEnv)
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
					PackageName: b.Package,
					Version:     b.Version,
					Channel:     b.Channel,
					BundleName:  b.Name,
					BundleImage: b.Image,
					CatalogRef:  catalogImage,
				}

				// On vanilla k8s, generate self-signed serving certs for services
				// that have the OpenShift serving-cert annotation, and inject
				// webhook cert volumes into Deployments.
				if isVanillaK8s() {
					if err := certs.EnsureServingCerts(ctx, kubeconfig, targetNamespace, b.Package, manifests.Services, manifests.Deployments, manifests.Other); err != nil {
						return fmt.Errorf("failed to provision serving certificates: %w", err)
					}
					// If the bundle has webhook configurations, inject the cert
					// volume mount into the Deployment and create the TLS secret.
					// On OpenShift, OLM handles this; on vanilla k8s we do it.
					webhookSecretName := bundle.WebhookCertSecretName
					if manifests.InjectWebhookCertVolumes(webhookSecretName) {
						if err := certs.EnsureWebhookCert(ctx, kubeconfig, targetNamespace, webhookSecretName, b.Package, manifests.Services, manifests.Other); err != nil {
							return fmt.Errorf("failed to provision webhook certificate: %w", err)
						}
					}
				}

				if err := k8sApplier.Apply(ctx, manifests, ic); err != nil {
					return fmt.Errorf("failed to apply bundle %q: %w", b.Name, err)
				}

				appliedManifests = append(appliedManifests, manifests)

				// Also patch ServiceAccounts so that any pods created later
				// (e.g., by the operator itself) inherit the pull secret.
				if pullSecretPath != "" {
					if err := patchBundleServiceAccounts(ctx, k8sApplier, manifests, packageName); err != nil {
						return err
					}
				}
			}
			return nil
		}()

		if installErr != nil {
			// Roll back previously applied resources on failure
			if len(appliedManifests) > 0 && !dryRun {
				fmt.Printf("\nInstall failed, rolling back %d previously applied bundle(s)...\n", len(appliedManifests))
				rollbackCtx := context.Background()
				for i := len(appliedManifests) - 1; i >= 0; i-- {
					allResources := appliedManifests[i].AllResources()
					var asList []unstructured.Unstructured
					for _, obj := range allResources {
						asList = append(asList, *obj)
					}
					if err := k8sApplier.DeleteResources(rollbackCtx, asList); err != nil {
						fmt.Printf("  Warning: rollback error: %v\n", err)
					}
				}
				fmt.Println("Rollback complete.")
			}
			return installErr
		}

		fmt.Printf("\nSuccessfully installed %s v%s\n", packageName, installPlan.TargetVersion)
		return nil
	},
}

// ensureClusterPullSecret reads the pull secret file and creates it as a
// kubernetes.io/dockerconfigjson Secret in the target namespace.
func ensureClusterPullSecret(ctx context.Context, k8sApplier *applier.Applier, packageName string) error {
	data, err := registry.ReadPullSecretData(pullSecretPath)
	if err != nil {
		return fmt.Errorf("failed to read pull secret: %w", err)
	}
	if err := k8sApplier.EnsurePullSecret(ctx, data, packageName); err != nil {
		return fmt.Errorf("failed to create pull secret in cluster: %w", err)
	}
	return nil
}

// patchBundleServiceAccounts patches all ServiceAccounts referenced by the bundle
// (plus the namespace's default SA) with imagePullSecrets referencing the pull
// secret. This ensures all operator pods can pull images from authenticated registries.
func patchBundleServiceAccounts(ctx context.Context, k8sApplier *applier.Applier, manifests *bundle.Manifests, packageName string) error {
	secretName := applier.PullSecretName(packageName)
	patched := make(map[string]bool)

	// Always patch the default SA — pods that don't specify a SA use it
	if err := k8sApplier.PatchServiceAccountPullSecret(ctx, "default", secretName); err != nil {
		return fmt.Errorf("failed to patch default ServiceAccount: %w", err)
	}
	patched["default"] = true

	// Patch all ServiceAccounts from the bundle's RBAC manifests
	for _, obj := range manifests.RBAC {
		if obj.GetKind() == "ServiceAccount" && !patched[obj.GetName()] {
			if err := k8sApplier.PatchServiceAccountPullSecret(ctx, obj.GetName(), secretName); err != nil {
				return fmt.Errorf("failed to patch ServiceAccount %q: %w", obj.GetName(), err)
			}
			patched[obj.GetName()] = true
		}
	}

	// Also patch any ServiceAccounts referenced by Deployments that weren't
	// in the RBAC manifests (e.g. pre-existing SAs the operator expects)
	for _, dep := range manifests.Deployments {
		saName, found, _ := unstructured.NestedString(dep.Object, "spec", "template", "spec", "serviceAccountName")
		if found && saName != "" && !patched[saName] {
			if err := k8sApplier.PatchServiceAccountPullSecret(ctx, saName, secretName); err != nil {
				// Non-fatal: the SA might not exist yet or might be in a different namespace
				fmt.Printf("  Warning: could not patch ServiceAccount %q: %v\n", saName, err)
			} else {
				patched[saName] = true
			}
		}
	}

	return nil
}

func init() {
	installCmd.Flags().StringVar(&installChannel, "channel", "", "channel to install from (defaults to package's default channel)")
	installCmd.Flags().StringVar(&installVersion, "version", "", "specific version to install (defaults to channel head)")
	installCmd.Flags().BoolVar(&installForce, "force", false, "force re-install if already installed")
	installCmd.Flags().StringVar(&installMode, "install-mode", "", "install mode: AllNamespaces, SingleNamespace, OwnNamespace (defaults to operator's preferred mode)")
	installCmd.Flags().StringVar(&installEnv, "env", "", "comma-separated environment variables to inject into operator containers (e.g. KEY1=val1,KEY2=val2)")
	installCmd.ValidArgsFunction = completeCatalogPackages
	rootCmd.AddCommand(installCmd)
}
