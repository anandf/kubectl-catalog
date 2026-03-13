package registry

import (
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

func TestPullSecretKeychainResolve(t *testing.T) {
	pullSecret := []byte(`{
		"auths": {
			"registry.example.com": {"auth": "dXNlcjpwYXNz"},
			"quay.io": {"auth": "cXVheTpzZWNyZXQ="}
		}
	}`)

	kc, err := newPullSecretKeychain(pullSecret)
	if err != nil {
		t.Fatalf("newPullSecretKeychain() error: %v", err)
	}

	// Should resolve registry.example.com
	reg, _ := name.NewRegistry("registry.example.com")
	auth, err := kc.Resolve(reg)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if auth == authn.Anonymous {
		t.Error("expected non-anonymous auth for registry.example.com")
	}

	// Should resolve quay.io
	reg, _ = name.NewRegistry("quay.io")
	auth, err = kc.Resolve(reg)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if auth == authn.Anonymous {
		t.Error("expected non-anonymous auth for quay.io")
	}

	// Unknown registry should return anonymous
	reg, _ = name.NewRegistry("docker.io")
	auth, err = kc.Resolve(reg)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if auth != authn.Anonymous {
		t.Error("expected anonymous auth for docker.io")
	}
}

func TestPullSecretKeychainWithHTTPSPrefix(t *testing.T) {
	pullSecret := []byte(`{
		"auths": {
			"https://registry.example.com": {"auth": "dXNlcjpwYXNz"}
		}
	}`)

	kc, err := newPullSecretKeychain(pullSecret)
	if err != nil {
		t.Fatalf("newPullSecretKeychain() error: %v", err)
	}

	reg, _ := name.NewRegistry("registry.example.com")
	auth, err := kc.Resolve(reg)
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if auth == authn.Anonymous {
		t.Error("expected non-anonymous auth for registry.example.com (key has https:// prefix)")
	}
}

func TestPullSecretKeychainInvalid(t *testing.T) {
	_, err := newPullSecretKeychain([]byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	_, err = newPullSecretKeychain([]byte(`{"no_auths": true}`))
	if err == nil {
		t.Error("expected error for missing auths field")
	}
}

func TestMatchesRegistry(t *testing.T) {
	tests := []struct {
		key      string
		registry string
		want     bool
	}{
		{"registry.example.com", "registry.example.com", true},
		{"https://registry.example.com", "registry.example.com", true},
		{"http://registry.example.com", "registry.example.com", true},
		{"https://registry.example.com/v2/", "registry.example.com", true},
		{"quay.io", "registry.example.com", false},
		{"registry.example.com", "quay.io", false},
	}

	for _, tt := range tests {
		t.Run(tt.key+"->"+tt.registry, func(t *testing.T) {
			got := matchesRegistry(tt.key, tt.registry)
			if got != tt.want {
				t.Errorf("matchesRegistry(%q, %q) = %v, want %v", tt.key, tt.registry, got, tt.want)
			}
		})
	}
}
