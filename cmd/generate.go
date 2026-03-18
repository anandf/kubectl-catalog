package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/anandf/kubectl-catalog/internal/catalog"
	"github.com/anandf/kubectl-catalog/internal/certs"
	"github.com/anandf/kubectl-catalog/internal/registry"
	"github.com/anandf/kubectl-catalog/internal/resolver"
	"github.com/anandf/kubectl-catalog/internal/state"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

var (
	generateChannel    string
	generateVersion    string
	generateMode       string
	generateOutput     string
	generateEnv        string
	generatePushSecret string
)

// generateMetadata holds the install context written alongside generated manifests.
// It is read back by the apply command to stamp tracking labels/annotations.
type generateMetadata struct {
	PackageName string `json:"packageName"`
	Version     string `json:"version"`
	Channel     string `json:"channel"`
	BundleName  string `json:"bundleName"`
	BundleImage string `json:"bundleImage"`
	CatalogRef  string `json:"catalogRef"`
	Namespace   string `json:"namespace"`
	ClusterType string `json:"clusterType"`
	InstallMode string `json:"installMode"`
}

var generateCmd = &cobra.Command{
	Use:   "generate <package-name>",
	Short: "Generate manifests for an operator without applying them",
	Long: `Generate Kubernetes manifests for an operator installation.

This resolves the bundle, extracts manifests, applies all transformations
(namespace, install mode, WATCH_NAMESPACE, serving certs), and writes the
final YAML files to an output destination.

Output destination (--output / -o):
  Local directory (default):  -o ./my-manifests  or omit for ./<package>-manifests/
  OCI registry:               -o oci://quay.io/myorg/my-operator:v1.2

When pushing to an OCI registry, the artifact uses a single layer with media type
application/vnd.oci.image.layer.v1.tar+gzip, compatible with Argo CD and FluxCD.
Use --push-secret for registry authentication when pushing.

Supports all the same flags as "kubectl catalog install":
  --ocp-version      OCP version to derive the catalog image
  --catalog          Full catalog image reference
  --catalog-type     Catalog type: redhat, community, certified, operatorhub
  --cluster-type     Target cluster type: k8s, ocp, okd
  --namespace / -n   Target namespace for the operator
  --install-mode     AllNamespaces, SingleNamespace, or OwnNamespace
  --channel          Channel to install from
  --version          Specific version to generate
  --env              Comma-separated env vars to inject (e.g. KEY1=val1,KEY2=val2)
  --pull-secret      Path to a pull secret file for source registry authentication
  --push-secret      Path to a credentials file for OCI push authentication
  --cache-dir        Directory for caching catalog and bundle images
  --refresh          Force re-pull of cached catalog images

On vanilla Kubernetes (--cluster-type k8s), self-signed TLS serving certificates
are generated for services that use the OpenShift serving-cert annotation.

Use "kubectl catalog apply <source>" to apply the generated manifests.
The <source> can be a local directory or an oci:// reference.

Examples:
  # Generate to local directory (default)
  kubectl catalog generate cluster-logging --ocp-version 4.20 --pull-secret ~/ps.json

  # Generate to a custom output directory
  kubectl catalog generate my-operator --ocp-version 4.20 -o /tmp/manifests --pull-secret ~/ps.json

  # Generate and push directly to an OCI registry
  kubectl catalog generate my-operator --ocp-version 4.20 \
    -o oci://quay.io/myorg/my-operator:v1.2 \
    --pull-secret ~/ps.json --push-secret ~/push-creds.json

  # Generate from OperatorHub.io (no pull secret needed)
  kubectl catalog generate prometheus --catalog-type operatorhub

  # Generate with custom environment variables
  kubectl catalog generate my-operator --ocp-version 4.20 \
    --env "DISABLE_WEBHOOKS=true,LOG_LEVEL=debug" --pull-secret ~/ps.json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		packageName := args[0]
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

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

		// Verify pull secret credentials
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

		res := resolver.New(fbc)
		installPlan, err := res.Resolve(packageName, generateChannel, generateVersion)
		if err != nil {
			return fmt.Errorf("failed to resolve %q: %w", packageName, err)
		}

		fmt.Printf("Resolved install plan: %d bundle(s)\n", len(installPlan.Bundles))
		for _, b := range installPlan.Bundles {
			fmt.Printf("  - %s v%s (from %s)\n", b.Name, b.Version, b.Image)
		}

		isOCI := isOCIOutput(generateOutput)
		namespaceExplicit := cmd.Flags().Changed("namespace")

		// For OCI output, write to a temp directory first, then push
		outputDir := generateOutput
		if isOCI {
			tmpDir, err := os.MkdirTemp("", "kubectl-catalog-generate-*")
			if err != nil {
				return fmt.Errorf("creating temp directory: %w", err)
			}
			defer os.RemoveAll(tmpDir)
			outputDir = tmpDir
		} else if outputDir == "" {
			safePkg := filepath.Base(packageName)
			outputDir = filepath.Join(".", fmt.Sprintf("%s-manifests", safePkg))
		}

		var lastMeta *generateMetadata

		for _, b := range installPlan.Bundles {
			bundleDir, err := puller.PullBundle(ctx, b.Image)
			if err != nil {
				return fmt.Errorf("failed to pull bundle %q: %w", b.Image, err)
			}

			manifests, err := bundle.Extract(bundleDir)
			if err != nil {
				return fmt.Errorf("failed to extract bundle %q: %w", b.Name, err)
			}

			targetNamespace := namespace
			if !namespaceExplicit && manifests.SuggestedNamespace != "" {
				targetNamespace = manifests.SuggestedNamespace
				fmt.Printf("  Using suggested namespace %q from bundle\n", targetNamespace)
			}

			// Determine and apply install mode
			mode := generateMode
			if mode == "" {
				mode = manifests.DefaultInstallMode()
			}
			if err := applyInstallMode(manifests, mode, targetNamespace); err != nil {
				return err
			}

			// Inject user-specified environment variables into all containers
			if generateEnv != "" {
				envVars, err := parseEnvVars(generateEnv)
				if err != nil {
					return err
				}
				manifests.SetEnvVars(envVars)
				fmt.Printf("  Injected %d environment variable(s) into operator containers\n", len(envVars))
			}

			// Stamp tracking labels and annotations on all resources
			labels := state.TrackingLabels(b.Package)
			annotations := state.TrackingAnnotations(b.Version, b.Channel, b.Name, b.Image, catalogImage)
			for _, obj := range manifests.AllResources() {
				stampTrackingMetadata(obj, labels, annotations)
				// Set namespace on namespaced resources
				if isNamespacedKind(obj.GetKind()) && obj.GetNamespace() == "" {
					obj.SetNamespace(targetNamespace)
				}
				// Fill in namespace on binding subjects
				setSubjectNamespaces(obj, targetNamespace)
			}

			// For multi-bundle plans, put each bundle in a subdirectory
			bundleOutputDir := outputDir
			if len(installPlan.Bundles) > 1 {
				safeBundleName := filepath.Base(b.Name)
				bundleOutputDir = filepath.Join(outputDir, safeBundleName)
			}

			if err := writeManifests(bundleOutputDir, manifests, targetNamespace, &b, catalogImage, mode); err != nil {
				return fmt.Errorf("failed to write manifests for %q: %w", b.Name, err)
			}

			// Generate serving cert secrets for vanilla k8s
			if isVanillaK8s() {
				if err := generateServingCertSecrets(bundleOutputDir, targetNamespace, manifests); err != nil {
					return fmt.Errorf("failed to generate serving cert secrets: %w", err)
				}
			}

			lastMeta = &generateMetadata{
				PackageName: b.Package,
				Version:     b.Version,
				Channel:     b.Channel,
				BundleName:  b.Name,
				BundleImage: b.Image,
				CatalogRef:  catalogImage,
				Namespace:   targetNamespace,
				ClusterType: clusterType,
				InstallMode: mode,
			}

			if !isOCI {
				fmt.Printf("\nManifests written to %s\n", bundleOutputDir)
			}
		}

		// Push to OCI registry if output is oci://
		if isOCI {
			ociRef := strings.TrimPrefix(generateOutput, "oci://")
			if err := pushToOCI(ctx, outputDir, ociRef, lastMeta); err != nil {
				return err
			}
		} else {
			fmt.Printf("\nReview the generated manifests, then apply with:\n")
			fmt.Printf("  kubectl catalog apply %s\n", outputDir)
		}

		return nil
	},
}

// isOCIOutput returns true if the output destination is an OCI registry reference.
func isOCIOutput(output string) bool {
	return strings.HasPrefix(output, "oci://")
}

// pushToOCI packages the manifest directory as an OCI artifact and pushes it.
func pushToOCI(ctx context.Context, manifestDir, imageRef string, meta *generateMetadata) error {
	// Create a pusher with push-secret credentials if provided
	var pusher *registry.ImagePuller
	var err error
	if generatePushSecret != "" {
		pusher, err = registry.NewImagePullerWithPullSecret(cacheDir, generatePushSecret)
		if err != nil {
			return fmt.Errorf("failed to create OCI pusher with push-secret: %w", err)
		}
	} else {
		pusher = registry.NewImagePuller(cacheDir)
	}

	// Count files being pushed
	fileCount := 0
	if walkErr := filepath.Walk(manifestDir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info != nil && !info.IsDir() {
			fileCount++
		}
		return nil
	}); walkErr != nil {
		return fmt.Errorf("scanning manifest directory: %w", walkErr)
	}

	fmt.Printf("\nPushing %d file(s) to %s...\n", fileCount, imageRef)

	ociAnnotations := map[string]string{
		"org.opencontainers.image.title":   meta.PackageName,
		"org.opencontainers.image.version": meta.Version,
		"org.opencontainers.image.created": time.Now().UTC().Format(time.RFC3339),
		"org.opencontainers.image.source":  meta.CatalogRef,
	}

	if err := pusher.PushManifests(ctx, manifestDir, imageRef, ociAnnotations); err != nil {
		return fmt.Errorf("failed to push manifests: %w", err)
	}

	fmt.Printf("\nSuccessfully pushed %s v%s to %s\n", meta.PackageName, meta.Version, imageRef)

	// Split image reference into repo and tag for Argo CD / FluxCD templates
	repo, tag := splitImageRef(imageRef)

	fmt.Println("\n--- Argo CD Application ---")
	fmt.Printf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: %s
  namespace: argocd
spec:
  project: default
  source:
    repoURL: oci://%s
    targetRevision: %s
    path: .
  destination:
    server: https://kubernetes.default.svc
    namespace: %s
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
`, meta.PackageName, repo, tag, meta.Namespace)

	fmt.Println("\n--- FluxCD OCIRepository + Kustomization ---")
	fmt.Printf(`apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: OCIRepository
metadata:
  name: %s
  namespace: flux-system
spec:
  interval: 5m
  url: oci://%s
  ref:
    tag: %s
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: %s
  namespace: flux-system
spec:
  interval: 5m
  sourceRef:
    kind: OCIRepository
    name: %s
  targetNamespace: %s
  prune: true
`, meta.PackageName, repo, tag, meta.PackageName, meta.PackageName, meta.Namespace)

	fmt.Println("\n--- Pull and apply manually ---")
	fmt.Printf("  kubectl catalog apply oci://%s\n", imageRef)

	return nil
}

// splitImageRef splits an image reference into repository and tag.
// "quay.io/org/repo:v1.2" -> ("quay.io/org/repo", "v1.2")
func splitImageRef(ref string) (string, string) {
	// Handle digest references
	if idx := strings.LastIndex(ref, "@"); idx >= 0 {
		return ref[:idx], ref[idx+1:]
	}
	// Handle tag references — find the last colon that's not part of a port
	if idx := strings.LastIndex(ref, ":"); idx >= 0 {
		// Make sure it's a tag, not a port (ports are followed by /)
		afterColon := ref[idx+1:]
		if !strings.Contains(afterColon, "/") {
			return ref[:idx], afterColon
		}
	}
	return ref, "latest"
}

func stampTrackingMetadata(obj *unstructured.Unstructured, labels, annotations map[string]string) {
	existing := obj.GetLabels()
	if existing == nil {
		existing = make(map[string]string)
	}
	for k, v := range labels {
		existing[k] = v
	}
	obj.SetLabels(existing)

	existingAnn := obj.GetAnnotations()
	if existingAnn == nil {
		existingAnn = make(map[string]string)
	}
	for k, v := range annotations {
		existingAnn[k] = v
	}
	obj.SetAnnotations(existingAnn)
}

func writeManifests(outputDir string, manifests *bundle.Manifests, targetNamespace string, b *resolver.BundleRef, catalogImage, installMode string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Write metadata
	meta := generateMetadata{
		PackageName: b.Package,
		Version:     b.Version,
		Channel:     b.Channel,
		BundleName:  b.Name,
		BundleImage: b.Image,
		CatalogRef:  catalogImage,
		Namespace:   targetNamespace,
		ClusterType: clusterType,
		InstallMode: installMode,
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}
	metaYAML, err := yaml.JSONToYAML(metaJSON)
	if err != nil {
		return fmt.Errorf("converting metadata to YAML: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "_metadata.yaml"), metaYAML, 0o644); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}

	counter := 0
	writeResources := func(phase string, resources []*unstructured.Unstructured) error {
		for _, obj := range resources {
			counter++
			name := sanitizeFileName(obj.GetKind(), obj.GetName())
			filename := fmt.Sprintf("%03d-%s-%s.yaml", counter, phase, name)

			data, err := yaml.Marshal(obj.Object)
			if err != nil {
				return fmt.Errorf("marshaling %s/%s: %w", obj.GetKind(), obj.GetName(), err)
			}
			if err := os.WriteFile(filepath.Join(outputDir, filename), data, 0o644); err != nil {
				return fmt.Errorf("writing %s: %w", filename, err)
			}
		}
		return nil
	}

	if err := writeResources("crd", manifests.CRDs); err != nil {
		return err
	}
	if err := writeResources("rbac", manifests.RBAC); err != nil {
		return err
	}
	if err := writeResources("deployment", manifests.Deployments); err != nil {
		return err
	}
	if err := writeResources("service", manifests.Services); err != nil {
		return err
	}
	if err := writeResources("other", manifests.Other); err != nil {
		return err
	}

	return nil
}

// generateServingCertSecrets generates self-signed TLS secret YAML files for
// services that have the OpenShift serving-cert annotation and for webhook
// cert secrets referenced by Deployment volumes.
func generateServingCertSecrets(outputDir, namespace string, manifests *bundle.Manifests) error {
	seen := make(map[string]bool)

	type certEntry struct {
		secretName  string
		serviceName string
	}
	var entries []certEntry

	// 1. Services with the OpenShift serving-cert annotation
	for _, svc := range manifests.Services {
		annotations := svc.GetAnnotations()
		if annotations == nil {
			continue
		}
		secretName, ok := annotations["service.beta.openshift.io/serving-cert-secret-name"]
		if !ok || secretName == "" {
			continue
		}
		if !seen[secretName] {
			seen[secretName] = true
			entries = append(entries, certEntry{secretName: secretName, serviceName: svc.GetName()})
		}
	}

	// 2. Webhook cert secrets from Deployment volumes
	webhookServiceMap := certs.BuildWebhookServiceMap(manifests.Other)
	for _, dep := range manifests.Deployments {
		for _, secretName := range certs.FindWebhookCertSecrets(dep) {
			if seen[secretName] {
				continue
			}
			seen[secretName] = true
			serviceName := webhookServiceMap[secretName]
			if serviceName == "" {
				serviceName = dep.GetName()
			}
			entries = append(entries, certEntry{secretName: secretName, serviceName: serviceName})
		}
	}

	for _, e := range entries {
		certPEM, keyPEM, caPEM, err := certs.GenerateServingCert(e.serviceName, namespace)
		if err != nil {
			return fmt.Errorf("generating serving cert for %q: %w", e.serviceName, err)
		}

		// Inject CA bundle into webhook configurations in the generated manifests
		certs.InjectCABundle(manifests.Other, e.serviceName, caPEM)

		secret := map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      e.secretName,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
				},
				"annotations": map[string]interface{}{
					"kubectl-catalog.io/self-signed": "true",
				},
			},
			"type": "kubernetes.io/tls",
			"stringData": map[string]interface{}{
				"tls.crt": string(certPEM),
				"tls.key": string(keyPEM),
			},
		}

		data, err := yaml.Marshal(secret)
		if err != nil {
			return fmt.Errorf("marshaling TLS secret: %w", err)
		}

		filename := fmt.Sprintf("tls-secret-%s.yaml", sanitizeFileName("Secret", e.secretName))
		if err := os.WriteFile(filepath.Join(outputDir, filename), data, 0o644); err != nil {
			return fmt.Errorf("writing TLS secret %q: %w", e.secretName, err)
		}

		fmt.Printf("  Generated self-signed serving cert secret %q for service %q\n", e.secretName, e.serviceName)
	}
	return nil
}

func sanitizeFileName(kind, name string) string {
	s := strings.ToLower(fmt.Sprintf("%s-%s", kind, name))
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

// isNamespacedKind returns true for resource kinds that are namespace-scoped.
func isNamespacedKind(kind string) bool {
	switch kind {
	case "CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding",
		"Namespace", "PersistentVolume", "StorageClass",
		"PriorityClass", "ValidatingWebhookConfiguration",
		"MutatingWebhookConfiguration", "APIService":
		return false
	default:
		return true
	}
}

// setSubjectNamespaces fills in namespace on ServiceAccount subjects in bindings.
func setSubjectNamespaces(obj *unstructured.Unstructured, ns string) {
	kind := obj.GetKind()
	if kind != "ClusterRoleBinding" && kind != "RoleBinding" {
		return
	}

	subjects, found, _ := unstructured.NestedSlice(obj.Object, "subjects")
	if !found {
		return
	}

	modified := false
	for i, s := range subjects {
		subject, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		subjectKind, _ := subject["kind"].(string)
		subjectNS, _ := subject["namespace"].(string)
		if subjectKind == "ServiceAccount" && subjectNS == "" {
			subject["namespace"] = ns
			subjects[i] = subject
			modified = true
		}
	}

	if modified {
		unstructured.SetNestedSlice(obj.Object, subjects, "subjects")
	}
}

func init() {
	generateCmd.Flags().StringVar(&generateChannel, "channel", "", "channel to install from (defaults to package's default channel)")
	generateCmd.Flags().StringVar(&generateVersion, "version", "", "specific version to install (defaults to channel head)")
	generateCmd.Flags().StringVar(&generateMode, "install-mode", "", "install mode: AllNamespaces, SingleNamespace, OwnNamespace (defaults to operator's preferred mode)")
	generateCmd.Flags().StringVarP(&generateOutput, "output", "o", "", "output destination: local directory or oci:// registry reference (defaults to ./<package-name>-manifests)")
	generateCmd.Flags().StringVar(&generateEnv, "env", "", "comma-separated environment variables to inject into operator containers (e.g. KEY1=val1,KEY2=val2)")
	generateCmd.Flags().StringVar(&generatePushSecret, "push-secret", "", "path to a credentials file for OCI push authentication (only used with oci:// output)")
	rootCmd.AddCommand(generateCmd)
}
