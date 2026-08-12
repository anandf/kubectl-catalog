package certs

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildIssuerObject(t *testing.T) {
	issuer := buildIssuerObject("test-ns", "my-operator")

	if issuer.GetKind() != "Issuer" {
		t.Errorf("Kind = %q, want Issuer", issuer.GetKind())
	}
	if issuer.GetName() != "my-operator-selfsigned-issuer" {
		t.Errorf("Name = %q, want my-operator-selfsigned-issuer", issuer.GetName())
	}
	if issuer.GetNamespace() != "test-ns" {
		t.Errorf("Namespace = %q, want test-ns", issuer.GetNamespace())
	}

	labels := issuer.GetLabels()
	if labels["app.kubernetes.io/managed-by"] != "kubectl-catalog" {
		t.Error("missing managed-by label")
	}
	if labels["kubectl-catalog.io/package"] != "my-operator" {
		t.Error("missing package label")
	}

	_, found, _ := unstructured.NestedMap(issuer.Object, "spec", "selfSigned")
	if !found {
		t.Error("spec.selfSigned not found")
	}
}

func TestBuildCertificateObject(t *testing.T) {
	p := certProvision{secretName: "my-tls", serviceName: "my-svc"}
	cert := buildCertificateObject("test-ns", "my-operator", p)

	if cert.GetKind() != "Certificate" {
		t.Errorf("Kind = %q, want Certificate", cert.GetKind())
	}
	if cert.GetName() != "my-tls-cert" {
		t.Errorf("Name = %q, want my-tls-cert", cert.GetName())
	}
	if cert.GetNamespace() != "test-ns" {
		t.Errorf("Namespace = %q, want test-ns", cert.GetNamespace())
	}

	secretName, found, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	if !found || secretName != "my-tls" {
		t.Errorf("spec.secretName = %q, want my-tls", secretName)
	}

	issuerName, found, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
	if !found || issuerName != "my-operator-selfsigned-issuer" {
		t.Errorf("spec.issuerRef.name = %q, want my-operator-selfsigned-issuer", issuerName)
	}

	dnsNames, found, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	if !found {
		t.Fatal("spec.dnsNames not found")
	}
	expectedDNS := []string{
		"my-svc",
		"my-svc.test-ns",
		"my-svc.test-ns.svc",
		"my-svc.test-ns.svc.cluster.local",
	}
	if len(dnsNames) != len(expectedDNS) {
		t.Fatalf("dnsNames count = %d, want %d", len(dnsNames), len(expectedDNS))
	}
	for i, want := range expectedDNS {
		if dnsNames[i] != want {
			t.Errorf("dnsNames[%d] = %q, want %q", i, dnsNames[i], want)
		}
	}

	duration, found, _ := unstructured.NestedString(cert.Object, "spec", "duration")
	if !found || duration != "8760h" {
		t.Errorf("spec.duration = %q, want 8760h", duration)
	}
}

func TestAnnotateWebhooksForCertManager(t *testing.T) {
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
								"namespace": "test-ns",
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
								"namespace": "test-ns",
							},
						},
					},
				},
			},
		},
		{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]interface{}{"name": "unrelated"},
			},
		},
	}

	AnnotateWebhooksForCertManager(resources, "my-svc", "test-ns", "my-tls")

	ann := resources[0].GetAnnotations()
	expected := "test-ns/my-tls-cert"
	if ann[caInjectorAnnotation] != expected {
		t.Errorf("matching webhook annotation = %q, want %q", ann[caInjectorAnnotation], expected)
	}

	ann2 := resources[1].GetAnnotations()
	if _, ok := ann2[caInjectorAnnotation]; ok {
		t.Error("non-matching webhook should not be annotated")
	}

	ann3 := resources[2].GetAnnotations()
	if ann3 != nil {
		if _, ok := ann3[caInjectorAnnotation]; ok {
			t.Error("non-webhook resource should not be annotated")
		}
	}
}

func TestAnnotateWebhooksForCertManager_EmptyServiceName(t *testing.T) {
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

	AnnotateWebhooksForCertManager(resources, "", "ns", "my-tls")

	ann := resources[0].GetAnnotations()
	if ann[caInjectorAnnotation] != "ns/my-tls-cert" {
		t.Errorf("empty serviceName should match all webhooks, got annotation %q", ann[caInjectorAnnotation])
	}
}

func TestGenerateCertManagerResources(t *testing.T) {
	services := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Service",
				"metadata": map[string]interface{}{
					"name": "my-svc",
					"annotations": map[string]interface{}{
						servingCertAnnotation: "my-svc-tls",
					},
				},
			},
		},
	}
	webhooks := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "admissionregistration.k8s.io/v1",
				"kind":       "ValidatingWebhookConfiguration",
				"metadata":   map[string]interface{}{"name": "vwc"},
				"webhooks": []interface{}{
					map[string]interface{}{
						"name": "v.example.com",
						"clientConfig": map[string]interface{}{
							"service": map[string]interface{}{
								"name":      "my-svc",
								"namespace": "test-ns",
							},
						},
					},
				},
			},
		},
	}

	resources, err := GenerateCertManagerResources("test-ns", "my-operator", services, nil, webhooks)
	if err != nil {
		t.Fatalf("GenerateCertManagerResources() error: %v", err)
	}

	if len(resources) != 2 {
		t.Fatalf("expected 2 resources (Issuer + Certificate), got %d", len(resources))
	}

	if resources[0].GetKind() != "Issuer" {
		t.Errorf("first resource Kind = %q, want Issuer", resources[0].GetKind())
	}
	if resources[1].GetKind() != "Certificate" {
		t.Errorf("second resource Kind = %q, want Certificate", resources[1].GetKind())
	}

	ann := webhooks[0].GetAnnotations()
	if ann[caInjectorAnnotation] == "" {
		t.Error("webhook should have been annotated with cert-manager CA injection")
	}
}

func TestGenerateCertManagerResources_NoCerts(t *testing.T) {
	services := []*unstructured.Unstructured{
		{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Service",
				"metadata":   map[string]interface{}{"name": "plain-svc"},
			},
		},
	}

	resources, err := GenerateCertManagerResources("test-ns", "my-op", services, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("expected 0 resources for services without cert annotations, got %d", len(resources))
	}
}

func TestIsCertManagerProvider(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"cert-manager", true},
		{"Cert-Manager", true},
		{"CERT-MANAGER", true},
		{"self-signed", false},
		{"none", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsCertManagerProvider(tt.input); got != tt.want {
			t.Errorf("IsCertManagerProvider(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestIsSelfSignedProvider(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"self-signed", true},
		{"Self-Signed", true},
		{"", true},
		{"cert-manager", false},
		{"none", false},
	}
	for _, tt := range tests {
		if got := IsSelfSignedProvider(tt.input); got != tt.want {
			t.Errorf("IsSelfSignedProvider(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
