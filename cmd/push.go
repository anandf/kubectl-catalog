package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

var (
	pushRegistry      string
	pushPackage       string
	pushTag           string
	pushRepoNamespace string
)

var pushCmd = &cobra.Command{
	Use:   "push <manifest-directory>",
	Short: "Push generated manifests as an OCI artifact to a registry",
	Long: `Package operator manifests into a standard OCI artifact and push to a container registry.

The artifact uses the standard OCI layer media type (application/vnd.oci.image.layer.v1.tar+gzip)
which is natively supported by:
  - Argo CD v3.1+ (OCI artifact source)
  - FluxCD (OCIRepository source)
  - ORAS CLI

This enables GitOps workflows without requiring a Git repository. Cluster administrators
can generate, review, and push operator manifests to a registry, then configure Argo CD
or FluxCD to continuously sync from the OCI artifact.

The manifest directory must have been created by "kubectl catalog generate".

The image reference is computed automatically from the operator metadata:
  <registry>[/<repo-namespace>]/<package>:<tag>

Use --registry, --package, --tag, and --repo-namespace to customize the image reference.

Example workflow:
  kubectl catalog generate cluster-logging --ocp-version 4.20 --pull-secret ~/ps.json -o ./manifests
  # Review and edit manifests
  kubectl catalog push ./manifests --registry quay.io --repo-namespace myorg`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		manifestDir := args[0]
		ctx := context.Background()

		// Verify the directory exists and has a _metadata.yaml
		metaPath := filepath.Join(manifestDir, "_metadata.yaml")
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			return fmt.Errorf("%s does not contain _metadata.yaml (was it created by 'kubectl catalog generate'?)", manifestDir)
		}

		// Read metadata to set OCI annotations
		meta, err := readGenerateMetadata(manifestDir)
		if err != nil {
			return fmt.Errorf("reading metadata: %w", err)
		}

		// Derive image reference from metadata and flags
		pkg := pushPackage
		if pkg == "" {
			pkg = meta.PackageName
		}
		tag := pushTag
		if tag == "" {
			tag = fmt.Sprintf("v%s", meta.Version)
		}

		// Build image reference: <registry>[/<repo-namespace>]/<package>:<tag>
		imageRef := pushRegistry
		if pushRepoNamespace != "" {
			imageRef = fmt.Sprintf("%s/%s", imageRef, pushRepoNamespace)
		}
		imageRef = fmt.Sprintf("%s/%s:%s", imageRef, pkg, tag)

		puller, err := newImagePuller()
		if err != nil {
			return fmt.Errorf("failed to create image client: %w", err)
		}

		// Count files being pushed
		fileCount := 0
		if err := filepath.Walk(manifestDir, func(_ string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info != nil && !info.IsDir() {
				fileCount++
			}
			return nil
		}); err != nil {
			return fmt.Errorf("scanning manifest directory: %w", err)
		}

		fmt.Printf("Pushing %d file(s) from %s to %s...\n", fileCount, manifestDir, imageRef)

		// Build standard OCI annotations from metadata
		ociAnnotations := map[string]string{
			"org.opencontainers.image.title":   meta.PackageName,
			"org.opencontainers.image.version": meta.Version,
			"org.opencontainers.image.created": time.Now().UTC().Format(time.RFC3339),
			"org.opencontainers.image.source":  meta.CatalogRef,
		}

		if err := puller.PushManifests(ctx, manifestDir, imageRef, ociAnnotations); err != nil {
			return fmt.Errorf("failed to push manifests: %w", err)
		}

		fmt.Printf("\nSuccessfully pushed %s v%s to %s\n\n", meta.PackageName, meta.Version, imageRef)

		fmt.Println("--- Argo CD Application ---")
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
`, meta.PackageName, imageRef, tag, meta.Namespace)

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
`, meta.PackageName, imageRef, tag, meta.PackageName, meta.PackageName, meta.Namespace)

		fmt.Println("\n--- Pull and apply manually ---")
		fmt.Printf("  oras pull %s -o ./manifests\n", imageRef)
		fmt.Println("  kubectl catalog apply ./manifests")

		return nil
	},
}

func init() {
	pushCmd.Flags().StringVar(&pushRegistry, "registry", "quay.io", "Registry to push to")
	pushCmd.Flags().StringVar(&pushPackage, "package", "", "Package name for the image (default: from metadata)")
	pushCmd.Flags().StringVar(&pushTag, "tag", "", "Tag for the image (default: v<version>)")
	pushCmd.Flags().StringVar(&pushRepoNamespace, "repo-namespace", "", "Repository namespace/organization (e.g., myorg)")
	rootCmd.AddCommand(pushCmd)
}
