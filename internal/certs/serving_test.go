package certs

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGenerateServingCert(t *testing.T) {
	certPEM, keyPEM, caPEM, err := GenerateServingCert("my-service", "my-namespace")
	if err != nil {
		t.Fatalf("GenerateServingCert() error: %v", err)
	}

	// Verify CA PEM is valid
	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil {
		t.Fatal("failed to decode CA PEM")
	}
	if caBlock.Type != "CERTIFICATE" {
		t.Errorf("CA PEM type = %q, want CERTIFICATE", caBlock.Type)
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse CA certificate: %v", err)
	}
	if !caCert.IsCA {
		t.Error("CA cert should be a CA")
	}

	// Verify cert PEM is valid
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		t.Fatal("failed to decode cert PEM")
	}
	if certBlock.Type != "CERTIFICATE" {
		t.Errorf("cert PEM type = %q, want CERTIFICATE", certBlock.Type)
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}

	// Verify key PEM is valid
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("failed to decode key PEM")
	}
	if keyBlock.Type != "EC PRIVATE KEY" {
		t.Errorf("key PEM type = %q, want EC PRIVATE KEY", keyBlock.Type)
	}

	_, err = x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse EC private key: %v", err)
	}

	// Verify DNS SANs
	expectedSANs := []string{
		"my-service",
		"my-service.my-namespace",
		"my-service.my-namespace.svc",
		"my-service.my-namespace.svc.cluster.local",
	}
	if len(cert.DNSNames) != len(expectedSANs) {
		t.Fatalf("DNS SANs count = %d, want %d", len(cert.DNSNames), len(expectedSANs))
	}
	for i, want := range expectedSANs {
		if cert.DNSNames[i] != want {
			t.Errorf("DNS SAN[%d] = %q, want %q", i, cert.DNSNames[i], want)
		}
	}

	// Verify subject CN
	wantCN := "my-service.my-namespace.svc"
	if cert.Subject.CommonName != wantCN {
		t.Errorf("Subject.CN = %q, want %q", cert.Subject.CommonName, wantCN)
	}

	// Verify it's a server auth certificate
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Error("expected ExtKeyUsageServerAuth")
	}

	// Verify it's NOT a CA
	if cert.IsCA {
		t.Error("serving cert should not be a CA")
	}

	// Verify the key type is ECDSA P256
	pubKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatal("public key is not ECDSA")
	}
	if pubKey.Curve.Params().BitSize != 256 {
		t.Errorf("curve bit size = %d, want 256", pubKey.Curve.Params().BitSize)
	}
}

func TestGenerateServingCert_DifferentInputs(t *testing.T) {
	// Verify different service/namespace combinations produce valid certs
	cases := []struct {
		service   string
		namespace string
	}{
		{"metrics", "default"},
		{"webhook-service", "openshift-logging"},
		{"a", "b"},
	}

	for _, tc := range cases {
		t.Run(tc.service+"/"+tc.namespace, func(t *testing.T) {
			certPEM, keyPEM, caPEM, err := GenerateServingCert(tc.service, tc.namespace)
			if err != nil {
				t.Fatalf("GenerateServingCert(%q, %q) error: %v", tc.service, tc.namespace, err)
			}
			if len(certPEM) == 0 {
				t.Error("certPEM is empty")
			}
			if len(keyPEM) == 0 {
				t.Error("keyPEM is empty")
			}
			if len(caPEM) == 0 {
				t.Error("caPEM is empty")
			}
		})
	}
}

func TestIsCertMountPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/tmp/k8s-webhook-server/serving-certs", true},
		{"/tmp/k8s-webhook-server/serving-certs/", true},
		{"/apiserver.local.config/certificates", true},
		{"/tmp/serving-certs", true},
		{"/some/path/serving-cert-dir", true},
		{"/webhook-server/serving/certs", true},
		{"/opt/webhook-cert/tls", true},
		{"/var/log", false},
		{"/etc/config", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isCertMountPath(tt.path); got != tt.want {
				t.Errorf("isCertMountPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsCertSecretName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"serving-cert", true},
		{"webhook-server-cert", true},
		{"my-operator-serving-cert", true},
		{"tls-secret", true},
		{"my-webhook-cert", true},
		{"random-configmap", false},
		{"my-secret", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCertSecretName(tt.name); got != tt.want {
				t.Errorf("isCertSecretName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestFindWebhookCertSecrets(t *testing.T) {
	// Deployment with a cert volume mount at a known path
	dep := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "my-operator"},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "manager",
								"image": "my-operator:v1",
								"volumeMounts": []interface{}{
									map[string]interface{}{
										"name":      "cert-vol",
										"mountPath": "/tmp/k8s-webhook-server/serving-certs",
									},
								},
							},
						},
						"volumes": []interface{}{
							map[string]interface{}{
								"name": "cert-vol",
								"secret": map[string]interface{}{
									"secretName": "my-webhook-tls",
								},
							},
						},
					},
				},
			},
		},
	}

	secrets := FindWebhookCertSecrets(dep)
	if len(secrets) != 1 || secrets[0] != "my-webhook-tls" {
		t.Errorf("FindWebhookCertSecrets() = %v, want [my-webhook-tls]", secrets)
	}
}

func TestFindWebhookCertSecretsNoCertVolumes(t *testing.T) {
	dep := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "my-app"},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "app",
								"image": "app:v1",
								"volumeMounts": []interface{}{
									map[string]interface{}{
										"name":      "data",
										"mountPath": "/data",
									},
								},
							},
						},
						"volumes": []interface{}{
							map[string]interface{}{
								"name":     "data",
								"emptyDir": map[string]interface{}{},
							},
						},
					},
				},
			},
		},
	}

	secrets := FindWebhookCertSecrets(dep)
	if len(secrets) != 0 {
		t.Errorf("FindWebhookCertSecrets() = %v, want empty", secrets)
	}
}

func TestFindWebhookCertSecretsBySecretName(t *testing.T) {
	// Secret name pattern matches even without cert mount path
	dep := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "op"},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{},
						"volumes": []interface{}{
							map[string]interface{}{
								"name": "certs",
								"secret": map[string]interface{}{
									"secretName": "webhook-server-cert",
								},
							},
						},
					},
				},
			},
		},
	}

	secrets := FindWebhookCertSecrets(dep)
	if len(secrets) != 1 || secrets[0] != "webhook-server-cert" {
		t.Errorf("FindWebhookCertSecrets() = %v, want [webhook-server-cert]", secrets)
	}
}

func TestFindWebhookCertSecretsNoVolumes(t *testing.T) {
	dep := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "minimal"},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{},
				},
			},
		},
	}

	secrets := FindWebhookCertSecrets(dep)
	if len(secrets) != 0 {
		t.Errorf("FindWebhookCertSecrets() = %v, want empty", secrets)
	}
}

func TestBuildWebhookServiceMap(t *testing.T) {
	resources := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "ValidatingWebhookConfiguration",
				"metadata": map[string]interface{}{
					"name": "my-webhook",
					"annotations": map[string]interface{}{
						servingCertAnnotation: "my-cert-secret",
					},
				},
				"webhooks": []interface{}{
					map[string]interface{}{
						"name": "validate.example.com",
						"clientConfig": map[string]interface{}{
							"service": map[string]interface{}{
								"name":      "my-webhook-svc",
								"namespace": "operators",
							},
						},
					},
				},
			},
		},
		// Non-webhook resource should be skipped
		{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]interface{}{"name": "cm"},
			},
		},
	}

	result := BuildWebhookServiceMap(resources)

	// Should map the annotated secret
	if result["my-cert-secret"] != "my-webhook-svc" {
		t.Errorf("expected my-cert-secret -> my-webhook-svc, got %q", result["my-cert-secret"])
	}

	// Should map common naming conventions
	if result["my-webhook-svc-cert"] != "my-webhook-svc" {
		t.Errorf("expected my-webhook-svc-cert -> my-webhook-svc, got %q", result["my-webhook-svc-cert"])
	}
	if result["my-webhook-svc-tls"] != "my-webhook-svc" {
		t.Errorf("expected my-webhook-svc-tls -> my-webhook-svc, got %q", result["my-webhook-svc-tls"])
	}
}

func TestBuildWebhookServiceMapEmpty(t *testing.T) {
	result := BuildWebhookServiceMap(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestBuildWebhookServiceMapNoWebhooks(t *testing.T) {
	resources := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "ValidatingWebhookConfiguration",
				"metadata":   map[string]interface{}{"name": "empty"},
			},
		},
	}

	result := BuildWebhookServiceMap(resources)
	if len(result) != 0 {
		t.Errorf("expected empty map for webhook with no entries, got %v", result)
	}
}

func TestInjectCABundle(t *testing.T) {
	caPEM := []byte("test-ca-pem-data")
	expectedB64 := base64.StdEncoding.EncodeToString(caPEM)

	resources := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "ValidatingWebhookConfiguration",
				"metadata":   map[string]interface{}{"name": "my-vwc"},
				"webhooks": []interface{}{
					map[string]interface{}{
						"name": "validate.example.com",
						"clientConfig": map[string]interface{}{
							"service": map[string]interface{}{
								"name":      "my-svc",
								"namespace": "ns",
							},
						},
					},
				},
			},
		},
		{
			Object: map[string]interface{}{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "MutatingWebhookConfiguration",
				"metadata":   map[string]interface{}{"name": "my-mwc"},
				"webhooks": []interface{}{
					map[string]interface{}{
						"name": "mutate.example.com",
						"clientConfig": map[string]interface{}{
							"service": map[string]interface{}{
								"name":      "other-svc",
								"namespace": "ns",
							},
						},
					},
				},
			},
		},
	}

	InjectCABundle(resources, "my-svc", caPEM)

	// Check the matching webhook got the CA bundle
	webhooks, _, _ := unstructured.NestedSlice(resources[0].Object, "webhooks")
	wh := webhooks[0].(map[string]interface{})
	cc := wh["clientConfig"].(map[string]interface{})
	if cc["caBundle"] != expectedB64 {
		t.Errorf("caBundle = %v, want %q", cc["caBundle"], expectedB64)
	}

	// Check non-matching webhook did NOT get the CA bundle
	webhooks2, _, _ := unstructured.NestedSlice(resources[1].Object, "webhooks")
	wh2 := webhooks2[0].(map[string]interface{})
	cc2 := wh2["clientConfig"].(map[string]interface{})
	if _, ok := cc2["caBundle"]; ok {
		t.Error("caBundle should not be injected into non-matching service webhook")
	}
}

func TestInjectCABundleNonWebhookResources(t *testing.T) {
	resources := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]interface{}{"name": "cm"},
			},
		},
	}

	// Should be a no-op, no panic
	InjectCABundle(resources, "svc", []byte("ca-data"))
}

func TestInjectCABundleEmptyServiceName(t *testing.T) {
	caPEM := []byte("ca-data")
	expectedB64 := base64.StdEncoding.EncodeToString(caPEM)

	resources := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "ValidatingWebhookConfiguration",
				"metadata":   map[string]interface{}{"name": "vwc"},
				"webhooks": []interface{}{
					map[string]interface{}{
						"name": "w1",
						"clientConfig": map[string]interface{}{
							"service": map[string]interface{}{
								"name":      "any-svc",
								"namespace": "ns",
							},
						},
					},
				},
			},
		},
	}

	// Empty service name should match all webhooks
	InjectCABundle(resources, "", caPEM)

	webhooks, _, _ := unstructured.NestedSlice(resources[0].Object, "webhooks")
	wh := webhooks[0].(map[string]interface{})
	cc := wh["clientConfig"].(map[string]interface{})
	if cc["caBundle"] != expectedB64 {
		t.Errorf("caBundle should be injected when serviceName is empty, got %v", cc["caBundle"])
	}
}
