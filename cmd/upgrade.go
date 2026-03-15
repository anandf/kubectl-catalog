package cmd

import (
	"context"
	"fmt"

	"github.com/anandf/kubectl-catalog/internal/applier"
	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/anandf/kubectl-catalog/internal/catalog"
	"github.com/anandf/kubectl-catalog/internal/certs"
	"github.com/anandf/kubectl-catalog/internal/resolver"
	"github.com/anandf/kubectl-catalog/internal/state"
	"github.com/spf13/cobra"
)

var (
	upgradeChannel string
	upgradeMode    string
	upgradeEnv     string
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade <package-name>",
	Short: "Upgrade an installed operator",
	Long: `Upgrade an operator to the latest version in the channel's upgrade graph.
The current installed version is discovered from resource annotations in the cluster.
The target version is the channel head — the bundle that the upgrade graph resolves to.
Only the target bundle is applied (intermediate versions are skipped).

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
			return fmt.Errorf("package %q is not installed: %w", packageName, err)
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
			return fmt.Errorf("no upgrade available: %w", err)
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

		if err := k8sApplier.Apply(ctx, manifests, ic); err != nil {
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

func init() {
	upgradeCmd.Flags().StringVar(&upgradeChannel, "channel", "", "switch to a different channel for upgrade")
	upgradeCmd.Flags().StringVar(&upgradeMode, "install-mode", "", "install mode: AllNamespaces, SingleNamespace, OwnNamespace (defaults to operator's preferred mode)")
	upgradeCmd.Flags().StringVar(&upgradeEnv, "env", "", "comma-separated environment variables to inject into operator containers (e.g. KEY1=val1,KEY2=val2)")
	rootCmd.AddCommand(upgradeCmd)
}
