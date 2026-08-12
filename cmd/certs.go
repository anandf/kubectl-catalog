package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"github.com/anandf/kubectl-catalog/internal/certs"
	"sigs.k8s.io/yaml"
)

// provisionCerts handles TLS certificate provisioning for webhook and serving
// certs based on the --cert-provider flag. This is only called on vanilla k8s
// clusters; on OpenShift the service-ca-operator handles cert provisioning.
func provisionCerts(ctx context.Context, kubeconfig, namespace, packageName string, manifests *bundle.Manifests) error {
	switch {
	case certs.IsCertManagerProvider(certProvider):
		return provisionCertsWithCertManager(ctx, kubeconfig, namespace, packageName, manifests)
	case certs.IsSelfSignedProvider(certProvider):
		return provisionSelfSignedCerts(ctx, kubeconfig, namespace, packageName, manifests)
	default:
		// "none" — skip cert provisioning entirely
		fmt.Println("  Skipping certificate provisioning (--cert-provider=none)")
		return nil
	}
}

func provisionSelfSignedCerts(ctx context.Context, kubeconfig, namespace, packageName string, manifests *bundle.Manifests) error {
	if err := certs.EnsureServingCerts(ctx, kubeconfig, namespace, packageName, manifests.Services, manifests.Deployments, manifests.Other); err != nil {
		return fmt.Errorf("failed to provision serving certificates: %w", err)
	}

	webhookSecretName := bundle.WebhookCertSecretName
	injected, err := manifests.InjectWebhookCertVolumes(webhookSecretName)
	if err != nil {
		return err
	}
	if injected {
		if err := certs.EnsureWebhookCert(ctx, kubeconfig, namespace, webhookSecretName, packageName, manifests.Services, manifests.Other); err != nil {
			return fmt.Errorf("failed to provision webhook certificate: %w", err)
		}
	}
	return nil
}

// generateCertResources writes cert-related resources to disk as part of
// the generate command, based on the --cert-provider flag.
func generateCertResources(outputDir, namespace, packageName string, manifests *bundle.Manifests) error {
	switch {
	case certs.IsCertManagerProvider(certProvider):
		return generateCertManagerResources(outputDir, namespace, packageName, manifests)
	case certs.IsSelfSignedProvider(certProvider):
		return generateServingCertSecrets(outputDir, namespace, manifests)
	default:
		fmt.Println("  Skipping certificate generation (--cert-provider=none)")
		return nil
	}
}

func generateCertManagerResources(outputDir, namespace, packageName string, manifests *bundle.Manifests) error {
	resources, err := certs.GenerateCertManagerResources(namespace, packageName, manifests.Services, manifests.Deployments, manifests.Other)
	if err != nil {
		return err
	}
	for _, obj := range resources {
		name := sanitizeFileName(obj.GetKind(), obj.GetName())
		filename := fmt.Sprintf("cert-manager-%s.yaml", name)

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

func provisionCertsWithCertManager(ctx context.Context, kubeconfig, namespace, packageName string, manifests *bundle.Manifests) error {
	fmt.Println("  Using cert-manager for TLS certificate provisioning")

	if err := certs.EnsureCertManagerResources(ctx, kubeconfig, namespace, packageName, manifests.Services, manifests.Deployments, manifests.Other); err != nil {
		return fmt.Errorf("failed to provision cert-manager resources: %w", err)
	}

	webhookSecretName := bundle.WebhookCertSecretName
	injected, err := manifests.InjectWebhookCertVolumes(webhookSecretName)
	if err != nil {
		return err
	}
	if injected {
		if err := certs.EnsureCertManagerWebhookCert(ctx, kubeconfig, namespace, webhookSecretName, packageName, manifests.Services, manifests.Other); err != nil {
			return fmt.Errorf("failed to provision cert-manager webhook certificate: %w", err)
		}
	}
	return nil
}
