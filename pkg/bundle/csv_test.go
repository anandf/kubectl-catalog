package bundle

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func newTestCSV(t *testing.T, csvName string, deployments, clusterPerms, perms interface{}) *unstructured.Unstructured {
	t.Helper()
	obj := map[string]interface{}{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "ClusterServiceVersion",
		"metadata": map[string]interface{}{
			"name": csvName,
		},
		"spec": map[string]interface{}{
			"install": map[string]interface{}{
				"strategy": "deployment",
				"spec":     map[string]interface{}{},
			},
		},
	}

	installSpec := obj["spec"].(map[string]interface{})["install"].(map[string]interface{})["spec"].(map[string]interface{})

	if deployments != nil {
		installSpec["deployments"] = deployments
	}
	if clusterPerms != nil {
		installSpec["clusterPermissions"] = clusterPerms
	}
	if perms != nil {
		installSpec["permissions"] = perms
	}

	return &unstructured.Unstructured{Object: obj}
}

func TestExtractFromCSVDeployments(t *testing.T) {
	deployments := []interface{}{
		map[string]interface{}{
			"name": "my-operator",
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app": "my-operator",
					},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"app": "my-operator",
						},
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "operator",
								"image": "quay.io/example/operator:v1",
							},
						},
					},
				},
			},
		},
	}

	csv := newTestCSV(t, "my-operator.v1.0.0", deployments, nil, nil)
	manifests, err := extractFromCSV(csv)
	if err != nil {
		t.Fatalf("extractFromCSV() error: %v", err)
	}

	if len(manifests.Deployments) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(manifests.Deployments))
	}

	dep := manifests.Deployments[0]
	if dep.GetName() != "my-operator" {
		t.Errorf("expected deployment name my-operator, got %s", dep.GetName())
	}
	if dep.GetKind() != "Deployment" {
		t.Errorf("expected kind Deployment, got %s", dep.GetKind())
	}
}

func TestExtractFromCSVClusterRBAC(t *testing.T) {
	clusterPerms := []interface{}{
		map[string]interface{}{
			"serviceAccountName": "my-sa",
			"rules": []interface{}{
				map[string]interface{}{
					"apiGroups": []interface{}{""},
					"resources": []interface{}{"pods"},
					"verbs":     []interface{}{"get", "list"},
				},
			},
		},
	}

	csv := newTestCSV(t, "my-csv", nil, clusterPerms, nil)
	manifests, err := extractFromCSV(csv)
	if err != nil {
		t.Fatalf("extractFromCSV() error: %v", err)
	}

	// Should produce: ServiceAccount, ClusterRole, ClusterRoleBinding
	if len(manifests.RBAC) != 3 {
		t.Fatalf("expected 3 RBAC resources, got %d", len(manifests.RBAC))
	}

	kinds := make(map[string]bool)
	for _, r := range manifests.RBAC {
		kinds[r.GetKind()] = true
	}

	for _, expected := range []string{"ServiceAccount", "ClusterRole", "ClusterRoleBinding"} {
		if !kinds[expected] {
			t.Errorf("expected %s in RBAC resources", expected)
		}
	}
}

func TestExtractFromCSVDeduplicateServiceAccounts(t *testing.T) {
	sameSA := "shared-sa"

	clusterPerms := []interface{}{
		map[string]interface{}{
			"serviceAccountName": sameSA,
			"rules": []interface{}{
				map[string]interface{}{
					"apiGroups": []interface{}{""},
					"resources": []interface{}{"nodes"},
					"verbs":     []interface{}{"get"},
				},
			},
		},
	}

	perms := []interface{}{
		map[string]interface{}{
			"serviceAccountName": sameSA,
			"rules": []interface{}{
				map[string]interface{}{
					"apiGroups": []interface{}{""},
					"resources": []interface{}{"pods"},
					"verbs":     []interface{}{"get"},
				},
			},
		},
	}

	csv := newTestCSV(t, "dedup-csv", nil, clusterPerms, perms)
	manifests, err := extractFromCSV(csv)
	if err != nil {
		t.Fatalf("extractFromCSV() error: %v", err)
	}

	// Count ServiceAccount resources
	saCount := 0
	for _, r := range manifests.RBAC {
		if r.GetKind() == "ServiceAccount" {
			saCount++
		}
	}

	if saCount != 1 {
		t.Errorf("expected 1 ServiceAccount (deduplicated), got %d", saCount)
	}

	// Should still have all other RBAC: ClusterRole, ClusterRoleBinding, Role, RoleBinding
	// Total: 1 SA + 2 cluster (CR + CRB) + 2 namespaced (Role + RB) = 5
	if len(manifests.RBAC) != 5 {
		t.Errorf("expected 5 RBAC resources total, got %d", len(manifests.RBAC))
	}
}

func TestExtractFromCSVEmpty(t *testing.T) {
	csv := newTestCSV(t, "empty-csv", nil, nil, nil)
	manifests, err := extractFromCSV(csv)
	if err != nil {
		t.Fatalf("extractFromCSV() error: %v", err)
	}

	if len(manifests.Deployments) != 0 {
		t.Errorf("expected 0 deployments, got %d", len(manifests.Deployments))
	}
	if len(manifests.RBAC) != 0 {
		t.Errorf("expected 0 RBAC, got %d", len(manifests.RBAC))
	}
}

func TestClassifyAndAdd(t *testing.T) {
	tests := []struct {
		kind     string
		apiGroup string
		wantField string
	}{
		{"CustomResourceDefinition", "apiextensions.k8s.io/v1", "CRDs"},
		{"ClusterRole", "rbac.authorization.k8s.io/v1", "RBAC"},
		{"Role", "rbac.authorization.k8s.io/v1", "RBAC"},
		{"ServiceAccount", "v1", "RBAC"},
		{"Deployment", "apps/v1", "Deployments"},
		{"Service", "v1", "Services"},
		{"ConfigMap", "v1", "Other"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			m := &Manifests{}
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": tt.apiGroup,
					"kind":       tt.kind,
					"metadata":   map[string]interface{}{"name": "test"},
				},
			}

			classifyAndAdd(m, obj)

			var count int
			switch tt.wantField {
			case "CRDs":
				count = len(m.CRDs)
			case "RBAC":
				count = len(m.RBAC)
			case "Deployments":
				count = len(m.Deployments)
			case "Services":
				count = len(m.Services)
			case "Other":
				count = len(m.Other)
			}

			if count != 1 {
				t.Errorf("%s should be classified as %s", tt.kind, tt.wantField)
			}
		})
	}
}

func TestAllResourcesOrder(t *testing.T) {
	m := &Manifests{
		CRDs:        []*unstructured.Unstructured{makeObj("CRD")},
		RBAC:        []*unstructured.Unstructured{makeObj("SA")},
		Deployments: []*unstructured.Unstructured{makeObj("Deploy")},
		Services:    []*unstructured.Unstructured{makeObj("Svc")},
		Other:       []*unstructured.Unstructured{makeObj("CM")},
	}

	all := m.AllResources()
	if len(all) != 5 {
		t.Fatalf("expected 5 resources, got %d", len(all))
	}

	expected := []string{"CRD", "SA", "Deploy", "Svc", "CM"}
	for i, name := range expected {
		if all[i].GetName() != name {
			t.Errorf("position %d: expected %s, got %s", i, name, all[i].GetName())
		}
	}
}

func makeObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Test",
			"metadata":   map[string]interface{}{"name": name},
		},
	}
}

// Ensure json import is used (for mustMarshal-style helpers if needed)
var _ = json.Marshal
