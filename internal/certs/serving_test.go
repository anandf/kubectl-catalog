package certs

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
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
