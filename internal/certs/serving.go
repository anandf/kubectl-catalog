package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// servingCertAnnotation is the OpenShift annotation that triggers
	// automatic TLS secret provisioning via the service-ca-operator.
	servingCertAnnotation = "service.beta.openshift.io/serving-cert-secret-name"
	fieldManager          = "kubectl-catalog"
)

type certProvision struct {
	secretName  string
	serviceName string
}

// EnsureServingCerts scans services for the OpenShift serving-cert annotation
// and Deployments for webhook cert volume mounts, then creates self-signed
// CA + serving certificate secrets for each one.
// This replaces the service-ca-operator functionality on vanilla Kubernetes.
func EnsureServingCerts(ctx context.Context, kubeconfig, namespace, packageName string, services, deployments, webhooksAndOther []*unstructured.Unstructured) error {
	seen := make(map[string]bool)
	var toProvision []certProvision

	// 1. Services with the OpenShift serving-cert annotation
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

	// 2. Deployment volumes referencing Secrets that look like webhook/serving certs.
	//    On OpenShift these are provisioned by the service-ca-operator; on vanilla k8s
	//    they don't exist and the pod fails with "no such file or directory".
	webhookServiceMap := BuildWebhookServiceMap(webhooksAndOther)
	for _, dep := range deployments {
		for _, secretName := range FindWebhookCertSecrets(dep) {
			if seen[secretName] {
				continue
			}
			seen[secretName] = true

			// Try to find the webhook service name for proper DNS SANs
			serviceName := webhookServiceMap[secretName]
			if serviceName == "" {
				// Fall back: use the deployment name as a reasonable DNS name
				serviceName = dep.GetName()
			}
			toProvision = append(toProvision, certProvision{
				secretName:  secretName,
				serviceName: serviceName,
			})
		}
	}

	fmt.Printf("  Serving cert detection: %d service(s), %d deployment(s), %d webhook/other resource(s)\n",
		len(services), len(deployments), len(webhooksAndOther))
	fmt.Printf("  Serving certs to provision: %d\n", len(toProvision))
	for _, p := range toProvision {
		fmt.Printf("    - secret %q for service %q\n", p.secretName, p.serviceName)
	}

	if len(toProvision) == 0 {
		return nil
	}

	client, err := newDynamicClient(kubeconfig)
	if err != nil {
		return err
	}

	for _, p := range toProvision {
		// Check if the secret already exists
		secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
		_, err := client.Resource(secretGVR).Namespace(namespace).Get(ctx, p.secretName, metav1.GetOptions{})
		if err == nil {
			fmt.Printf("    Serving cert secret %q already exists, skipping\n", p.secretName)
			continue
		}

		fmt.Printf("    Generating self-signed serving certificate for service %q (secret: %q)\n", p.serviceName, p.secretName)

		certPEM, keyPEM, caPEM, err := GenerateServingCert(p.serviceName, namespace)
		if err != nil {
			return fmt.Errorf("generating serving cert for %q: %w", p.serviceName, err)
		}

		if err := createTLSSecretWithTracking(ctx, client, namespace, p.secretName, certPEM, keyPEM, packageName); err != nil {
			return fmt.Errorf("creating secret %q: %w", p.secretName, err)
		}

		// Inject CA bundle into webhook configurations that reference this service
		InjectCABundle(webhooksAndOther, p.serviceName, caPEM)
	}

	return nil
}

// EnsureWebhookCert creates a self-signed TLS secret for webhook serving certs.
// It determines the webhook service name from webhook configurations, then
// generates a cert with the correct DNS SANs for that service.
func EnsureWebhookCert(ctx context.Context, kubeconfig, namespace, secretName, packageName string, services, webhooksAndOther []*unstructured.Unstructured) error {
	client, err := newDynamicClient(kubeconfig)
	if err != nil {
		return err
	}

	// Check if the secret already exists
	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	_, err = client.Resource(secretGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		fmt.Printf("    Webhook cert secret %q already exists, skipping\n", secretName)
		return nil
	}

	// Find the webhook service name for proper DNS SANs
	serviceName := ""
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
				serviceName = svcName
				break
			}
		}
		if serviceName != "" {
			break
		}
	}

	// Fall back to first service name if no webhook service found
	if serviceName == "" {
		for _, svc := range services {
			serviceName = svc.GetName()
			break
		}
	}
	if serviceName == "" {
		serviceName = "webhook-service"
	}

	fmt.Printf("    Generating self-signed webhook certificate for service %q (secret: %q)\n", serviceName, secretName)

	certPEM, keyPEM, caPEM, err := GenerateServingCert(serviceName, namespace)
	if err != nil {
		return fmt.Errorf("generating webhook cert for %q: %w", serviceName, err)
	}

	if err := createTLSSecretWithTracking(ctx, client, namespace, secretName, certPEM, keyPEM, packageName); err != nil {
		return err
	}

	// Inject CA bundle into webhook configurations that reference this service
	InjectCABundle(webhooksAndOther, serviceName, caPEM)

	return nil
}

// FindWebhookCertSecrets inspects a Deployment's pod template for volume mounts
// that look like webhook/serving certificate mounts. These are Secret volumes
// mounted at paths like /tmp/k8s-webhook-server/serving-certs/ that the
// controller-runtime webhook server expects.
func FindWebhookCertSecrets(dep *unstructured.Unstructured) []string {
	volumes, found, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "volumes")
	if !found {
		return nil
	}

	// Collect all volume mount paths from containers to identify cert mounts
	certMountVolumes := make(map[string]bool)
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		container, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		mounts, _ := container["volumeMounts"].([]interface{})
		for _, m := range mounts {
			mount, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			mountPath, _ := mount["mountPath"].(string)
			volName, _ := mount["name"].(string)
			// controller-runtime default and common webhook cert paths
			if isCertMountPath(mountPath) && volName != "" {
				certMountVolumes[volName] = true
			}
		}
	}

	var secretNames []string
	for _, v := range volumes {
		vol, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		volName, _ := vol["name"].(string)
		secret, ok := vol["secret"].(map[string]interface{})
		if !ok {
			continue
		}
		secretName, _ := secret["secretName"].(string)
		if secretName == "" {
			continue
		}
		// Include if the volume is mounted at a cert path, or if the secret
		// name suggests it's a cert/TLS secret
		if certMountVolumes[volName] || isCertSecretName(secretName) {
			secretNames = append(secretNames, secretName)
		}
	}
	return secretNames
}

// isCertMountPath returns true if the path looks like a webhook/serving cert mount.
func isCertMountPath(path string) bool {
	// Normalize: strip trailing slash
	path = strings.TrimRight(path, "/")

	certPaths := []string{
		"/tmp/k8s-webhook-server/serving-certs",
		"/apiserver.local.config/certificates",
		"/tmp/serving-certs",
	}
	for _, cp := range certPaths {
		if path == cp {
			return true
		}
	}

	// Also match paths that contain common cert-related keywords
	return strings.Contains(path, "serving-cert") ||
		strings.Contains(path, "webhook-server/serving") ||
		strings.Contains(path, "webhook-cert")
}

// isCertSecretName returns true if the secret name suggests it holds TLS certs.
func isCertSecretName(name string) bool {
	certPatterns := []string{
		"serving-cert", "webhook-server-cert", "webhook-cert",
		"tls-cert", "serving-certs-ca-bundle", "tls-secret",
	}
	for _, p := range certPatterns {
		if name == p || strings.HasSuffix(name, "-"+p) || strings.HasPrefix(name, p+"-") {
			return true
		}
	}
	// Also match names containing "serving-cert" or "webhook" + "cert"
	return strings.Contains(name, "serving-cert") ||
		(strings.Contains(name, "webhook") && strings.Contains(name, "cert"))
}

// buildWebhookServiceMap extracts the service name from ValidatingWebhookConfiguration
// and MutatingWebhookConfiguration resources, mapping them to their caBundle secret
// names (derived from the annotation or volume mount convention).
// Returns a map of secretName -> serviceName.
func BuildWebhookServiceMap(resources []*unstructured.Unstructured) map[string]string {
	result := make(map[string]string)

	for _, obj := range resources {
		kind := obj.GetKind()
		if kind != "ValidatingWebhookConfiguration" && kind != "MutatingWebhookConfiguration" {
			continue
		}

		// Check for the inject-cabundle annotation which tells us the cert secret
		annotations := obj.GetAnnotations()
		certSecretName := ""
		if annotations != nil {
			certSecretName = annotations[servingCertAnnotation]
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
			if !found || svcName == "" {
				continue
			}

			if certSecretName != "" {
				result[certSecretName] = svcName
			}
			// Also map common naming conventions
			result[svcName+"-cert"] = svcName
			result[svcName+"-tls"] = svcName
			result["webhook-server-cert"] = svcName
			result["serving-cert"] = svcName
		}
	}

	return result
}

// InjectCABundle patches all ValidatingWebhookConfiguration and MutatingWebhookConfiguration
// resources in the given list, setting the caBundle field on webhooks whose clientConfig
// references the specified service name. This is required on vanilla Kubernetes because
// the API server needs the CA bundle to verify the self-signed serving certificate.
// On OpenShift, the service-ca-operator handles this injection automatically.
func InjectCABundle(resources []*unstructured.Unstructured, serviceName string, caPEM []byte) {
	caB64 := base64.StdEncoding.EncodeToString(caPEM)

	for _, obj := range resources {
		kind := obj.GetKind()
		if kind != "ValidatingWebhookConfiguration" && kind != "MutatingWebhookConfiguration" {
			continue
		}

		webhooks, found, _ := unstructured.NestedSlice(obj.Object, "webhooks")
		if !found {
			continue
		}

		modified := false
		for i, wh := range webhooks {
			whMap, ok := wh.(map[string]interface{})
			if !ok {
				continue
			}

			// Only inject into webhooks that reference the matching service
			svcName, found, _ := unstructured.NestedString(whMap, "clientConfig", "service", "name")
			if !found {
				continue
			}
			// Match by exact service name, or inject into all webhooks if serviceName is empty
			if serviceName != "" && svcName != serviceName {
				continue
			}

			clientConfig, ok := whMap["clientConfig"].(map[string]interface{})
			if !ok {
				continue
			}
			clientConfig["caBundle"] = caB64
			whMap["clientConfig"] = clientConfig
			webhooks[i] = whMap
			modified = true
			fmt.Printf("    Injected CA bundle into %s webhook %q (service: %s)\n",
				kind, whMap["name"], svcName)
		}

		if modified {
			unstructured.SetNestedSlice(obj.Object, webhooks, "webhooks")
		}
	}
}

// GenerateServingCert creates a self-signed CA and signs a serving certificate
// for the given service. The serving cert includes DNS SANs for the service's
// in-cluster DNS names. Returns the serving cert PEM, private key PEM, and CA cert PEM.
func GenerateServingCert(serviceName, namespace string) (certPEM, keyPEM, caPEM []byte, err error) {
	// Generate CA key and certificate
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating CA key: %w", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("kubectl-catalog-ca-%s", serviceName),
			Organization: []string{"kubectl-catalog"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating CA certificate: %w", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	// Generate serving key and certificate
	servingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating serving key: %w", err)
	}

	servingTemplate := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("%s.%s.svc", serviceName, namespace),
			Organization: []string{"kubectl-catalog"},
		},
		DNSNames: []string{
			serviceName,
			fmt.Sprintf("%s.%s", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc", serviceName, namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace),
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}

	servingCertDER, err := x509.CreateCertificate(rand.Reader, servingTemplate, caCert, &servingKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating serving certificate: %w", err)
	}

	// Encode to PEM
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: servingCertDER})
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})

	keyDER, err := x509.MarshalECPrivateKey(servingKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshaling serving key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, caPEM, nil
}

// createTLSSecret creates a TLS Secret with the given cert and key, optionally
// stamped with a package tracking label for uninstall cleanup.
func createTLSSecret(ctx context.Context, client dynamic.Interface, namespace, name string, certPEM, keyPEM []byte) error {
	return createTLSSecretWithTracking(ctx, client, namespace, name, certPEM, keyPEM, "")
}

func createTLSSecretWithTracking(ctx context.Context, client dynamic.Interface, namespace, name string, certPEM, keyPEM []byte, packageName string) error {
	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

	labels := map[string]interface{}{
		"app.kubernetes.io/managed-by": "kubectl-catalog",
	}
	if packageName != "" {
		labels["kubectl-catalog.io/package"] = packageName
	}

	secret := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels":    labels,
				"annotations": map[string]interface{}{
					"kubectl-catalog.io/self-signed": "true",
				},
			},
			"type": "kubernetes.io/tls",
			"data": map[string]interface{}{
				"tls.crt": certPEM,
				"tls.key": keyPEM,
			},
		},
	}

	data, err := secret.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling TLS secret: %w", err)
	}

	_, err = client.Resource(secretGVR).Namespace(namespace).Patch(
		ctx, name, types.ApplyPatchType, data,
		metav1.PatchOptions{FieldManager: fieldManager},
	)
	return err
}

func newSerial() *big.Int {
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	return serial
}

func newDynamicClient(kubeconfig string) (dynamic.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}

	return dynamic.NewForConfig(config)
}
