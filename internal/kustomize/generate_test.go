package kustomize_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	kustomizegen "github.com/anandf/kubectl-catalog/internal/kustomize"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestKustomizeGenerate(t *testing.T) {
	dir := "/tmp/kustomize-test"
	os.RemoveAll(dir)

	g := &kustomizegen.KustomizeGenerator{
		PackageName: "my-operator", Version: "1.0.0", Channel: "stable",
		CatalogRef: "reg/cat:v1", CertProvider: "self-signed",
		InstallMode: "AllNamespaces", Namespace: "operator-system",
		Manifests: &bundle.Manifests{
			CRDs: []*unstructured.Unstructured{
				{Object: map[string]interface{}{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]interface{}{"name": "widgets.example.com"}}},
			},
			Deployments: []*unstructured.Unstructured{
				{Object: map[string]interface{}{
					"apiVersion": "apps/v1", "kind": "Deployment",
					"metadata": map[string]interface{}{"name": "controller-manager", "namespace": "operator-system"},
					"spec": map[string]interface{}{
						"replicas": int64(1),
						"template": map[string]interface{}{
							"spec": map[string]interface{}{
								"serviceAccountName": "controller-manager",
								"containers": []interface{}{
									map[string]interface{}{
										"name": "manager", "image": "quay.io/example/operator:v1.0.0",
										"env": []interface{}{
											map[string]interface{}{"name": "RELATED_IMAGE_PROXY", "value": "quay.io/example/kube-rbac-proxy:v0.15"},
										},
									},
									map[string]interface{}{
										"name": "kube-rbac-proxy", "image": "quay.io/example/kube-rbac-proxy:v0.15",
									},
								},
							},
						},
					},
				}},
			},
			RBAC: []*unstructured.Unstructured{
				{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]interface{}{"name": "controller-manager", "namespace": "operator-system"}}},
				{Object: map[string]interface{}{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": map[string]interface{}{"name": "manager-role"}}},
			},
			Services: []*unstructured.Unstructured{
				{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Service", "metadata": map[string]interface{}{"name": "metrics-service", "namespace": "operator-system"}}},
			},
			Other: []*unstructured.Unstructured{
				{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "manager-config", "namespace": "operator-system"}, "data": map[string]interface{}{"key": "val"}}},
				{Object: map[string]interface{}{"apiVersion": "monitoring.coreos.com/v1", "kind": "ServiceMonitor", "metadata": map[string]interface{}{"name": "metrics-monitor", "namespace": "operator-system"}}},
			},
		},
	}

	if err := g.Generate(dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Walk and print all files
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() { return nil }
		rel, _ := filepath.Rel(dir, path)
		data, _ := os.ReadFile(path)
		fmt.Printf("\n=== %s ===\n%s\n", rel, string(data))
		return nil
	})
}
