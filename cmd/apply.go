package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anandf/kubectl-catalog/internal/applier"
	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

var applyEnv string

var applyCmd = &cobra.Command{
	Use:   "apply <source>",
	Short: "Apply previously generated manifests to the cluster",
	Long: `Apply operator manifests that were generated with "kubectl catalog generate".

The <source> can be:
  - A local directory:   kubectl catalog apply ./my-manifests
  - An OCI reference:    kubectl catalog apply oci://quay.io/myorg/my-operator:v1.2

This reads YAML files from the source, classifies them by resource type,
and applies them using the same phased strategy as "kubectl catalog install":
CRDs first, then RBAC, Deployments, Services, and other resources.

The source must contain a _metadata.yaml file created by the generate command.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		source := args[0]
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if dryRun {
			fmt.Println("Running in dry-run mode — no changes will be made to the cluster")
		}

		// Resolve source: local directory or OCI artifact
		manifestDir, cleanupDir, err := resolveApplySource(ctx, source)
		if err != nil {
			return err
		}
		if cleanupDir != "" {
			defer os.RemoveAll(cleanupDir)
		}

		// Read metadata
		meta, err := readGenerateMetadata(manifestDir)
		if err != nil {
			return err
		}

		fmt.Printf("Applying %s v%s (channel: %s)\n", meta.PackageName, meta.Version, meta.Channel)
		fmt.Printf("  Namespace: %s\n", meta.Namespace)
		fmt.Printf("  Install mode: %s\n", meta.InstallMode)

		// Read and classify all YAML files
		manifests, err := readManifestDir(manifestDir)
		if err != nil {
			return fmt.Errorf("reading manifests from %s: %w", manifestDir, err)
		}

		totalResources := len(manifests.CRDs) + len(manifests.RBAC) +
			len(manifests.Deployments) + len(manifests.Services) + len(manifests.Other)
		fmt.Printf("  Found %d resource(s) to apply\n", totalResources)

		targetNamespace := meta.Namespace
		// Allow overriding namespace via --namespace flag
		if cmd.Flags().Changed("namespace") {
			targetNamespace = namespace
			fmt.Printf("  Overriding namespace to %q\n", targetNamespace)
		}

		k8sApplier, err := applier.New(kubeconfig, targetNamespace, applierOptions())
		if err != nil {
			return fmt.Errorf("failed to create applier: %w", err)
		}

		// Ensure the target namespace exists
		if err := k8sApplier.EnsureNamespace(ctx, targetNamespace); err != nil {
			return fmt.Errorf("failed to ensure namespace %q: %w", targetNamespace, err)
		}

		// Inject user-specified environment variables into all containers
		if applyEnv != "" {
			envVars, err := parseEnvVars(applyEnv)
			if err != nil {
				return err
			}
			manifests.SetEnvVars(envVars)
			fmt.Printf("  Injected %d environment variable(s) into operator containers\n", len(envVars))
		}

		// If a pull secret was provided, create it in the cluster and inject
		// imagePullSecrets into Deployment pod templates before applying.
		if pullSecretPath != "" {
			if err := ensureClusterPullSecret(ctx, k8sApplier, meta.PackageName); err != nil {
				return err
			}
			manifests.SetImagePullSecrets(applier.PullSecretName(meta.PackageName))
		}

		ic := &applier.InstallContext{
			PackageName: meta.PackageName,
			Version:     meta.Version,
			Channel:     meta.Channel,
			BundleName:  meta.BundleName,
			BundleImage: meta.BundleImage,
			CatalogRef:  meta.CatalogRef,
		}

		if err := k8sApplier.Apply(ctx, manifests, ic); err != nil {
			return fmt.Errorf("failed to apply manifests: %w", err)
		}

		// Also patch ServiceAccounts so that any pods created later
		// (e.g., by the operator itself) inherit the pull secret.
		if pullSecretPath != "" {
			if err := patchBundleServiceAccounts(ctx, k8sApplier, manifests, meta.PackageName); err != nil {
				return err
			}
		}

		fmt.Printf("\nSuccessfully applied %s v%s\n", meta.PackageName, meta.Version)
		return nil
	},
}

func readGenerateMetadata(dir string) (*generateMetadata, error) {
	metaPath := filepath.Join(dir, "_metadata.yaml")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w (was this directory created by 'kubectl catalog generate'?)", metaPath, err)
	}

	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parsing metadata: %w", err)
	}

	var meta generateMetadata
	if err := json.Unmarshal(jsonData, &meta); err != nil {
		return nil, fmt.Errorf("unmarshaling metadata: %w", err)
	}

	return &meta, nil
}

const maxReadDepth = 10

func readManifestDir(dir string) (*bundle.Manifests, error) {
	return readManifestDirDepth(dir, 0)
}

func readManifestDirDepth(dir string, depth int) (*bundle.Manifests, error) {
	if depth > maxReadDepth {
		return nil, fmt.Errorf("manifest directory nesting exceeds maximum depth (%d): %s", maxReadDepth, dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory %s: %w", dir, err)
	}

	// Sort entries by name to maintain apply order from generate
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	manifests := &bundle.Manifests{}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip metadata and non-YAML files
		if name == "_metadata.yaml" {
			continue
		}
		ext := filepath.Ext(name)
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		obj := &unstructured.Unstructured{}
		jsonData, err := yaml.YAMLToJSON(data)
		if err != nil {
			return nil, fmt.Errorf("parsing YAML in %s: %w", path, err)
		}
		if err := json.Unmarshal(jsonData, &obj.Object); err != nil {
			return nil, fmt.Errorf("unmarshaling %s: %w", path, err)
		}

		classifyResource(manifests, obj)
	}

	// Also check subdirectories for multi-bundle generate output
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subManifests, err := readManifestDirDepth(filepath.Join(dir, entry.Name()), depth+1)
		if err != nil {
			return nil, err
		}
		manifests.CRDs = append(manifests.CRDs, subManifests.CRDs...)
		manifests.RBAC = append(manifests.RBAC, subManifests.RBAC...)
		manifests.Deployments = append(manifests.Deployments, subManifests.Deployments...)
		manifests.Services = append(manifests.Services, subManifests.Services...)
		manifests.Other = append(manifests.Other, subManifests.Other...)
	}

	return manifests, nil
}

// classifyResource places a resource into the correct category based on its GVK.
func classifyResource(m *bundle.Manifests, obj *unstructured.Unstructured) {
	kind := obj.GetKind()
	switch {
	case kind == "CustomResourceDefinition":
		m.CRDs = append(m.CRDs, obj)
	case kind == "ClusterRole" || kind == "ClusterRoleBinding" ||
		kind == "Role" || kind == "RoleBinding" ||
		kind == "ServiceAccount":
		m.RBAC = append(m.RBAC, obj)
	case kind == "Deployment":
		m.Deployments = append(m.Deployments, obj)
	case kind == "Service":
		m.Services = append(m.Services, obj)
	case kind == "Secret" && hasAnnotation(obj, "kubectl-catalog.io/self-signed"):
		// TLS secrets generated for vanilla k8s go into Other so they're applied
		// after Services (which reference them)
		m.Other = append(m.Other, obj)
	default:
		m.Other = append(m.Other, obj)
	}
}

func hasAnnotation(obj *unstructured.Unstructured, key string) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return false
	}
	_, ok := annotations[key]
	return ok
}

// resolveApplySource resolves the source argument to a local directory path.
// If the source is an oci:// reference, it pulls the artifact to a temp directory.
// Returns the manifest directory path and a cleanup path (empty if no cleanup needed).
func resolveApplySource(ctx context.Context, source string) (manifestDir string, cleanupDir string, err error) {
	if !strings.HasPrefix(source, "oci://") {
		return source, "", nil
	}

	ociRef := strings.TrimPrefix(source, "oci://")

	// Validate pull secret for Red Hat registry images
	if strings.Contains(ociRef, "registry.redhat.io") {
		if err := requirePullSecretForRedHat(ociRef); err != nil {
			return "", "", err
		}
	}

	fmt.Printf("Pulling OCI artifact %s...\n", ociRef)

	tmpDir, err := os.MkdirTemp("", "kubectl-catalog-apply-*")
	if err != nil {
		return "", "", fmt.Errorf("creating temp directory: %w", err)
	}

	puller, pullerErr := newImagePuller()
	if pullerErr != nil {
		os.RemoveAll(tmpDir)
		return "", "", pullerErr
	}
	if err := puller.PullArtifact(ctx, ociRef, tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("failed to pull OCI artifact: %w", err)
	}

	return tmpDir, tmpDir, nil
}

func init() {
	applyCmd.Flags().StringVar(&applyEnv, "env", "", "comma-separated environment variables to inject into operator containers (e.g. KEY1=val1,KEY2=val2)")
	rootCmd.AddCommand(applyCmd)
}
