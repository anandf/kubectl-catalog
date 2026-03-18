package state

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func makeResource(kind, name, version, channel string) unstructured.Unstructured {
	obj := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       kind,
			"metadata": map[string]interface{}{
				"name": name,
				"annotations": map[string]interface{}{
					AnnVersion: version,
					AnnChannel: channel,
					AnnBundle:  "test-bundle",
				},
				"labels": map[string]interface{}{
					LabelManagedBy: ManagedByValue,
					LabelPackage:   "test-pkg",
				},
			},
		},
	}
	return obj
}

func TestBestMetadataResource(t *testing.T) {
	tests := []struct {
		name      string
		kinds     []string
		wantKind  string
	}{
		{
			name:     "deployment preferred over RBAC",
			kinds:    []string{"ServiceAccount", "ClusterRole", "Deployment"},
			wantKind: "Deployment",
		},
		{
			name:     "CRD preferred over RBAC",
			kinds:    []string{"Role", "CustomResourceDefinition", "RoleBinding"},
			wantKind: "CustomResourceDefinition",
		},
		{
			name:     "deployment preferred over CRD",
			kinds:    []string{"CustomResourceDefinition", "Deployment"},
			wantKind: "Deployment",
		},
		{
			name:     "single resource",
			kinds:    []string{"ConfigMap"},
			wantKind: "ConfigMap",
		},
		{
			name:     "RBAC only picks ClusterRole over ServiceAccount",
			kinds:    []string{"ServiceAccount", "ClusterRole"},
			wantKind: "ClusterRole",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resources []unstructured.Unstructured
			for _, kind := range tt.kinds {
				resources = append(resources, makeResource(kind, "r-"+kind, "1.0", "stable"))
			}
			best := bestMetadataResource(resources)
			if best.GetKind() != tt.wantKind {
				t.Errorf("bestMetadataResource() kind = %q, want %q", best.GetKind(), tt.wantKind)
			}
		})
	}
}

func TestDetectInconsistencies(t *testing.T) {
	t.Run("consistent", func(t *testing.T) {
		resources := []unstructured.Unstructured{
			makeResource("Deployment", "dep", "1.0", "stable"),
			makeResource("ServiceAccount", "sa", "1.0", "stable"),
		}
		warning := detectInconsistencies(resources, "1.0", "stable")
		if warning != "" {
			t.Errorf("expected no warning, got %q", warning)
		}
	})

	t.Run("version mismatch", func(t *testing.T) {
		resources := []unstructured.Unstructured{
			makeResource("Deployment", "dep", "2.0", "stable"),
			makeResource("ServiceAccount", "sa", "1.0", "stable"),
		}
		warning := detectInconsistencies(resources, "2.0", "stable")
		if warning == "" {
			t.Error("expected warning for version mismatch")
		}
	})

	t.Run("channel mismatch", func(t *testing.T) {
		resources := []unstructured.Unstructured{
			makeResource("Deployment", "dep", "1.0", "stable"),
			makeResource("ClusterRole", "cr", "1.0", "preview"),
		}
		warning := detectInconsistencies(resources, "1.0", "stable")
		if warning == "" {
			t.Error("expected warning for channel mismatch")
		}
	})

	t.Run("empty annotations ignored", func(t *testing.T) {
		r1 := makeResource("Deployment", "dep", "1.0", "stable")
		r2 := unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata":   map[string]interface{}{"name": "cm"},
			},
		}
		resources := []unstructured.Unstructured{r1, r2}
		warning := detectInconsistencies(resources, "1.0", "stable")
		if warning != "" {
			t.Errorf("expected no warning for empty annotations, got %q", warning)
		}
	})
}

func TestKindPriority(t *testing.T) {
	// Deployment should have highest priority (lowest number)
	if kindPriority("Deployment") >= kindPriority("CustomResourceDefinition") {
		t.Error("Deployment should have higher priority than CRD")
	}
	if kindPriority("CustomResourceDefinition") >= kindPriority("ClusterRole") {
		t.Error("CRD should have higher priority than ClusterRole")
	}
	if kindPriority("ClusterRole") >= kindPriority("ServiceAccount") {
		t.Error("ClusterRole should have higher priority than ServiceAccount")
	}
	if kindPriority("ServiceAccount") >= kindPriority("ConfigMap") {
		t.Error("ServiceAccount should have higher priority than ConfigMap")
	}
}
