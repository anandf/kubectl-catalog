package util

import "testing"

func TestSanitizeRef(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"registry.example.com/catalog:v4.20", "registry.example.com_catalog_v4.20"},
		{"quay.io/operatorhubio/catalog:latest", "quay.io_operatorhubio_catalog_latest"},
		{"registry.redhat.io/redhat/redhat-operator-index:v4.20", "registry.redhat.io_redhat_redhat-operator-index_v4.20"},
		{"image@sha256:abc123", "image_sha256_abc123"},
		{"simple", "simple"},
		{"a/b/c:d@e", "a_b_c_d_e"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeRef(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeRef(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
