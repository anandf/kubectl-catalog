package certs

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	certManagerAPIVersion = "cert-manager.io/v1"
	caInjectorAnnotation  = "cert-manager.io/inject-ca-from"
)

func issuerName(packageName string) string {
	return packageName + "-selfsigned-issuer"
}

func certificateName(secretName string) string {
	return secretName + "-cert"
}

// EnsureCertManagerResources creates a self-signed cert-manager Issuer and
// Certificate resources for services and webhooks that need TLS. It also
// annotates webhook configurations so that cert-manager's cainjector
// automatically populates their caBundle fields.
func EnsureCertManagerResources(ctx context.Context, kubeconfig, namespace, packageName string, services, deployments, webhooksAndOther []*unstructured.Unstructured) error {
	seen := make(map[string]bool)
	var toProvision []certProvision

	for _, svc := range services {
		annotations := svc.GetAnnotations()
		if annotations == nil {
			continue
		}
		secretName, ok := annotations[servingCertAnnotation]
		if !ok || secretName == "" {
			continue
		}
		if !seen[secretName] {
			seen[secretName] = true
			toProvision = append(toProvision, certProvision{
				secretName:  secretName,
				serviceName: svc.GetName(),
			})
		}
	}

	webhookServiceMap := BuildWebhookServiceMap(webhooksAndOther)
	for _, dep := range deployments {
		for _, secretName := range FindWebhookCertSecrets(dep) {
			if seen[secretName] {
				continue
			}
			seen[secretName] = true
			serviceName := webhookServiceMap[secretName]
			if serviceName == "" {
				serviceName = dep.GetName()
			}
			toProvision = append(toProvision, certProvision{
				secretName:  secretName,
				serviceName: serviceName,
			})
		}
	}

	fmt.Printf("  cert-manager: %d certificate(s) to provision\n", len(toProvision))
	if len(toProvision) == 0 {
		return nil
	}

	client, err := newDynamicClient(kubeconfig)
	if err != nil {
		return err
	}

	if err := checkCertManagerCRDs(ctx, client); err != nil {
		return err
	}

	if err := ensureIssuer(ctx, client, namespace, packageName); err != nil {
		return fmt.Errorf("creating cert-manager Issuer: %w", err)
	}

	for _, p := range toProvision {
		if err := ensureCertificate(ctx, client, namespace, packageName, p); err != nil {
			return fmt.Errorf("creating cert-manager Certificate for %q: %w", p.secretName, err)
		}
		AnnotateWebhooksForCertManager(webhooksAndOther, p.serviceName, namespace, p.secretName)
	}

	return nil
}

// EnsureCertManagerWebhookCert creates a cert-manager Certificate for a
// webhook TLS secret and annotates webhook configurations for CA injection.
func EnsureCertManagerWebhookCert(ctx context.Context, kubeconfig, namespace, secretName, packageName string, services, webhooksAndOther []*unstructured.Unstructured) error {
	client, err := newDynamicClient(kubeconfig)
	if err != nil {
		return err
	}

	if err := checkCertManagerCRDs(ctx, client); err != nil {
		return err
	}

	serviceName := findWebhookServiceName(services, webhooksAndOther)

	if err := ensureIssuer(ctx, client, namespace, packageName); err != nil {
		return fmt.Errorf("creating cert-manager Issuer: %w", err)
	}

	p := certProvision{secretName: secretName, serviceName: serviceName}
	if err := ensureCertificate(ctx, client, namespace, packageName, p); err != nil {
		return fmt.Errorf("creating cert-manager Certificate: %w", err)
	}

	AnnotateWebhooksForCertManager(webhooksAndOther, serviceName, namespace, secretName)
	return nil
}

// GenerateCertManagerResources returns cert-manager Issuer and Certificate
// unstructured objects suitable for writing to disk (used by the generate command).
func GenerateCertManagerResources(namespace, packageName string, services, deployments, webhooksAndOther []*unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	seen := make(map[string]bool)
	var toProvision []certProvision

	for _, svc := range services {
		annotations := svc.GetAnnotations()
		if annotations == nil {
			continue
		}
		secretName, ok := annotations[servingCertAnnotation]
		if !ok || secretName == "" {
			continue
		}
		if !seen[secretName] {
			seen[secretName] = true
			toProvision = append(toProvision, certProvision{
				secretName:  secretName,
				serviceName: svc.GetName(),
			})
		}
	}

	webhookServiceMap := BuildWebhookServiceMap(webhooksAndOther)
	for _, dep := range deployments {
		for _, secretName := range FindWebhookCertSecrets(dep) {
			if seen[secretName] {
				continue
			}
			seen[secretName] = true
			serviceName := webhookServiceMap[secretName]
			if serviceName == "" {
				serviceName = dep.GetName()
			}
			toProvision = append(toProvision, certProvision{
				secretName:  secretName,
				serviceName: serviceName,
			})
		}
	}

	if len(toProvision) == 0 {
		return nil, nil
	}

	var resources []*unstructured.Unstructured

	issuer := buildIssuerObject(namespace, packageName)
	resources = append(resources, issuer)

	for _, p := range toProvision {
		cert := buildCertificateObject(namespace, packageName, p)
		resources = append(resources, cert)
		AnnotateWebhooksForCertManager(webhooksAndOther, p.serviceName, namespace, p.secretName)
		fmt.Printf("  Generated cert-manager Certificate %q for service %q\n", certificateName(p.secretName), p.serviceName)
	}

	return resources, nil
}

// AnnotateWebhooksForCertManager adds the cert-manager.io/inject-ca-from
// annotation to webhook configurations that reference the given service.
// This tells cert-manager's cainjector to populate the caBundle field
// automatically from the Certificate's CA.
func AnnotateWebhooksForCertManager(resources []*unstructured.Unstructured, serviceName, namespace, secretName string) {
	certName := certificateName(secretName)
	annotationValue := namespace + "/" + certName

	for _, obj := range resources {
		kind := obj.GetKind()
		if kind != "ValidatingWebhookConfiguration" && kind != "MutatingWebhookConfiguration" {
			continue
		}

		webhooks, found, _ := unstructured.NestedSlice(obj.Object, "webhooks")
		if !found {
			continue
		}

		shouldAnnotate := false
		for _, wh := range webhooks {
			whMap, ok := wh.(map[string]interface{})
			if !ok {
				continue
			}
			svcName, found, _ := unstructured.NestedString(whMap, "clientConfig", "service", "name")
			if !found {
				continue
			}
			if serviceName == "" || svcName == serviceName {
				shouldAnnotate = true
				break
			}
		}

		if shouldAnnotate {
			annotations := obj.GetAnnotations()
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations[caInjectorAnnotation] = annotationValue
			obj.SetAnnotations(annotations)
			fmt.Printf("    Annotated %s %q with %s=%s\n",
				kind, obj.GetName(), caInjectorAnnotation, annotationValue)
		}
	}
}

func checkCertManagerCRDs(ctx context.Context, client dynamic.Interface) error {
	crdGVR := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	requiredCRDs := []string{
		"certificates.cert-manager.io",
		"issuers.cert-manager.io",
	}
	for _, name := range requiredCRDs {
		_, err := client.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("cert-manager CRD %q not found — is cert-manager installed? (%w)\nHint: install cert-manager with: kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml", name, err)
		}
	}
	return nil
}

func ensureIssuer(ctx context.Context, client dynamic.Interface, namespace, packageName string) error {
	issuerGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}

	name := issuerName(packageName)
	_, err := client.Resource(issuerGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		fmt.Printf("    cert-manager Issuer %q already exists\n", name)
		return nil
	}

	issuer := buildIssuerObject(namespace, packageName)

	data, err := issuer.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling Issuer: %w", err)
	}

	_, err = client.Resource(issuerGVR).Namespace(namespace).Patch(
		ctx, name, types.ApplyPatchType, data,
		metav1.PatchOptions{FieldManager: fieldManager},
	)
	if err != nil {
		return err
	}
	fmt.Printf("    Created cert-manager Issuer %q\n", name)
	return nil
}

func ensureCertificate(ctx context.Context, client dynamic.Interface, namespace, packageName string, p certProvision) error {
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}

	name := certificateName(p.secretName)
	_, err := client.Resource(certGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		fmt.Printf("    cert-manager Certificate %q already exists\n", name)
		return nil
	}

	cert := buildCertificateObject(namespace, packageName, p)

	data, err := cert.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling Certificate: %w", err)
	}

	_, err = client.Resource(certGVR).Namespace(namespace).Patch(
		ctx, name, types.ApplyPatchType, data,
		metav1.PatchOptions{FieldManager: fieldManager},
	)
	if err != nil {
		return err
	}
	fmt.Printf("    Created cert-manager Certificate %q (secret: %q, service: %q)\n", name, p.secretName, p.serviceName)
	return nil
}

func buildIssuerObject(namespace, packageName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": certManagerAPIVersion,
			"kind":       "Issuer",
			"metadata": map[string]interface{}{
				"name":      issuerName(packageName),
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
					"kubectl-catalog.io/package":   packageName,
				},
			},
			"spec": map[string]interface{}{
				"selfSigned": map[string]interface{}{},
			},
		},
	}
}

func buildCertificateObject(namespace, packageName string, p certProvision) *unstructured.Unstructured {
	dnsNames := []interface{}{
		p.serviceName,
		p.serviceName + "." + namespace,
		p.serviceName + "." + namespace + ".svc",
		p.serviceName + "." + namespace + ".svc.cluster.local",
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": certManagerAPIVersion,
			"kind":       "Certificate",
			"metadata": map[string]interface{}{
				"name":      certificateName(p.secretName),
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
					"kubectl-catalog.io/package":   packageName,
				},
			},
			"spec": map[string]interface{}{
				"secretName": p.secretName,
				"issuerRef": map[string]interface{}{
					"name": issuerName(packageName),
					"kind": "Issuer",
				},
				"dnsNames": dnsNames,
				"usages": []interface{}{
					"server auth",
				},
				"duration":    "8760h",
				"renewBefore": "720h",
			},
		},
	}
}

func findWebhookServiceName(services, webhooksAndOther []*unstructured.Unstructured) string {
	for _, obj := range webhooksAndOther {
		kind := obj.GetKind()
		if kind != "ValidatingWebhookConfiguration" && kind != "MutatingWebhookConfiguration" {
			continue
		}
		webhooks, found, _ := unstructured.NestedSlice(obj.Object, "webhooks")
		if !found {
			continue
		}
		for _, wh := range webhooks {
			whMap, ok := wh.(map[string]interface{})
			if !ok {
				continue
			}
			svcName, found, _ := unstructured.NestedString(whMap, "clientConfig", "service", "name")
			if found && svcName != "" {
				return svcName
			}
		}
	}

	for _, svc := range services {
		return svc.GetName()
	}

	return "webhook-service"
}

// IsCertManagerProvider returns true if the given cert provider string
// indicates cert-manager should be used.
func IsCertManagerProvider(provider string) bool {
	return strings.EqualFold(provider, "cert-manager")
}

// IsSelfSignedProvider returns true if the given cert provider string
// indicates self-signed certificates should be used (the default).
func IsSelfSignedProvider(provider string) bool {
	return provider == "" || strings.EqualFold(provider, "self-signed")
}
