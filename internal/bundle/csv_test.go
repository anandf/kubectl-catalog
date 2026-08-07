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
		kind      string
		apiGroup  string
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

func newTestCSVWithWebhooks(t *testing.T, csvName string, deployments []interface{}, webhookDefs []interface{}) *unstructured.Unstructured {
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
				"spec": map[string]interface{}{
					"deployments": deployments,
				},
			},
		},
	}

	if webhookDefs != nil {
		obj["spec"].(map[string]interface{})["webhookdefinitions"] = webhookDefs
	}

	return &unstructured.Unstructured{Object: obj}
}

func TestExtractFromCSV_ValidatingAdmissionWebhook(t *testing.T) {
	deployments := []interface{}{
		map[string]interface{}{
			"name": "controller-manager",
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"control-plane": "controller-manager"},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"control-plane": "controller-manager",
						},
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "manager",
								"image": "quay.io/example/operator:v1",
							},
						},
					},
				},
			},
		},
	}

	webhookDefs := []interface{}{
		map[string]interface{}{
			"type":                    "ValidatingAdmissionWebhook",
			"generateName":            "vfoo.example.com",
			"deploymentName":          "controller-manager",
			"containerPort":           int64(9443),
			"sideEffects":             "None",
			"failurePolicy":           "Fail",
			"admissionReviewVersions": []interface{}{"v1", "v1beta1"},
			"webhookPath":             "/validate-v1-foo",
			"rules": []interface{}{
				map[string]interface{}{
					"apiGroups":   []interface{}{"example.com"},
					"apiVersions": []interface{}{"v1"},
					"operations":  []interface{}{"CREATE", "UPDATE"},
					"resources":   []interface{}{"foos"},
				},
			},
		},
	}

	csv := newTestCSVWithWebhooks(t, "my-operator.v1.0.0", deployments, webhookDefs)
	manifests, err := extractFromCSV(csv)
	if err != nil {
		t.Fatalf("extractFromCSV() error: %v", err)
	}

	// Should have generated a Service
	if len(manifests.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(manifests.Services))
	}
	svc := manifests.Services[0]
	if svc.GetName() != "controller-manager-webhook-service" {
		t.Errorf("service name = %q, want controller-manager-webhook-service", svc.GetName())
	}
	if svc.GetKind() != "Service" {
		t.Errorf("service kind = %q, want Service", svc.GetKind())
	}

	// Verify service selector
	selector, _, _ := unstructured.NestedStringMap(svc.Object, "spec", "selector")
	if selector["control-plane"] != "controller-manager" {
		t.Errorf("service selector = %v, want control-plane=controller-manager", selector)
	}

	// Verify service port
	ports, _, _ := unstructured.NestedSlice(svc.Object, "spec", "ports")
	if len(ports) != 1 {
		t.Fatalf("expected 1 port, got %d", len(ports))
	}
	portMap := ports[0].(map[string]interface{})
	portVal, _, _ := unstructured.NestedInt64(portMap, "port")
	if int32(portVal) != 9443 {
		t.Errorf("service port = %v, want 9443", portVal)
	}

	// Should have generated a ValidatingWebhookConfiguration in Other
	if len(manifests.Other) != 1 {
		t.Fatalf("expected 1 Other resource, got %d", len(manifests.Other))
	}
	whConfig := manifests.Other[0]
	if whConfig.GetKind() != "ValidatingWebhookConfiguration" {
		t.Errorf("webhook kind = %q, want ValidatingWebhookConfiguration", whConfig.GetKind())
	}
	if whConfig.GetName() != "vfoo.example.com" {
		t.Errorf("webhook name = %q, want vfoo.example.com", whConfig.GetName())
	}

	// Verify webhook entry
	webhooks, _, _ := unstructured.NestedSlice(whConfig.Object, "webhooks")
	if len(webhooks) != 1 {
		t.Fatalf("expected 1 webhook entry, got %d", len(webhooks))
	}
	wh := webhooks[0].(map[string]interface{})

	svcName, _, _ := unstructured.NestedString(wh, "clientConfig", "service", "name")
	if svcName != "controller-manager-webhook-service" {
		t.Errorf("webhook service name = %q, want controller-manager-webhook-service", svcName)
	}

	whPath, _, _ := unstructured.NestedString(wh, "clientConfig", "service", "path")
	if whPath != "/validate-v1-foo" {
		t.Errorf("webhook path = %q, want /validate-v1-foo", whPath)
	}

	sideEffects, _, _ := unstructured.NestedString(wh, "sideEffects")
	if sideEffects != "None" {
		t.Errorf("sideEffects = %q, want None", sideEffects)
	}

	failurePolicy, _, _ := unstructured.NestedString(wh, "failurePolicy")
	if failurePolicy != "Fail" {
		t.Errorf("failurePolicy = %q, want Fail", failurePolicy)
	}
}

func TestExtractFromCSV_MutatingAdmissionWebhook(t *testing.T) {
	deployments := []interface{}{
		map[string]interface{}{
			"name": "controller-manager",
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"app": "operator"},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{"app": "operator"},
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "manager",
								"image": "quay.io/example/operator:v1",
							},
						},
					},
				},
			},
		},
	}

	webhookDefs := []interface{}{
		map[string]interface{}{
			"type":                    "MutatingAdmissionWebhook",
			"generateName":            "mfoo.example.com",
			"deploymentName":          "controller-manager",
			"containerPort":           int64(9443),
			"sideEffects":             "None",
			"failurePolicy":           "Ignore",
			"admissionReviewVersions": []interface{}{"v1"},
			"webhookPath":             "/mutate-v1-foo",
			"reinvocationPolicy":      "IfNeeded",
			"timeoutSeconds":          int64(15),
			"rules": []interface{}{
				map[string]interface{}{
					"apiGroups":   []interface{}{"example.com"},
					"apiVersions": []interface{}{"v1"},
					"operations":  []interface{}{"CREATE"},
					"resources":   []interface{}{"foos"},
				},
			},
		},
	}

	csv := newTestCSVWithWebhooks(t, "my-operator.v1.0.0", deployments, webhookDefs)
	manifests, err := extractFromCSV(csv)
	if err != nil {
		t.Fatalf("extractFromCSV() error: %v", err)
	}

	if len(manifests.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(manifests.Services))
	}

	if len(manifests.Other) != 1 {
		t.Fatalf("expected 1 Other resource, got %d", len(manifests.Other))
	}
	whConfig := manifests.Other[0]
	if whConfig.GetKind() != "MutatingWebhookConfiguration" {
		t.Errorf("webhook kind = %q, want MutatingWebhookConfiguration", whConfig.GetKind())
	}
	if whConfig.GetName() != "mfoo.example.com" {
		t.Errorf("webhook name = %q, want mfoo.example.com", whConfig.GetName())
	}

	// Verify reinvocationPolicy and timeoutSeconds
	webhooks, _, _ := unstructured.NestedSlice(whConfig.Object, "webhooks")
	if len(webhooks) != 1 {
		t.Fatalf("expected 1 webhook entry, got %d", len(webhooks))
	}
	wh := webhooks[0].(map[string]interface{})

	reinvocation, _, _ := unstructured.NestedString(wh, "reinvocationPolicy")
	if reinvocation != "IfNeeded" {
		t.Errorf("reinvocationPolicy = %q, want IfNeeded", reinvocation)
	}

	failurePolicy, _, _ := unstructured.NestedString(wh, "failurePolicy")
	if failurePolicy != "Ignore" {
		t.Errorf("failurePolicy = %q, want Ignore", failurePolicy)
	}

	timeout, found, _ := unstructured.NestedInt64(wh, "timeoutSeconds")
	if !found || timeout != 15 {
		t.Errorf("timeoutSeconds = %d, want 15", timeout)
	}
}

func TestExtractFromCSV_ConversionWebhook(t *testing.T) {
	deployments := []interface{}{
		map[string]interface{}{
			"name": "controller-manager",
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"app": "operator"},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{"app": "operator"},
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "manager",
								"image": "quay.io/example/operator:v1",
							},
						},
					},
				},
			},
		},
	}

	webhookDefs := []interface{}{
		map[string]interface{}{
			"type":                    "ConversionWebhook",
			"generateName":            "cfoo.example.com",
			"deploymentName":          "controller-manager",
			"containerPort":           int64(9443),
			"sideEffects":             "None",
			"admissionReviewVersions": []interface{}{"v1", "v1beta1"},
			"webhookPath":             "/convert",
			"conversionCRDs":          []interface{}{"foos.example.com", "bars.example.com"},
		},
	}

	csv := newTestCSVWithWebhooks(t, "my-operator.v1.0.0", deployments, webhookDefs)
	manifests, err := extractFromCSV(csv)
	if err != nil {
		t.Fatalf("extractFromCSV() error: %v", err)
	}

	// Should have generated a Service
	if len(manifests.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(manifests.Services))
	}

	// Should NOT have generated any webhook configurations (conversion uses CRD patching)
	if len(manifests.Other) != 0 {
		t.Errorf("expected 0 Other resources for conversion webhook, got %d", len(manifests.Other))
	}

	// Should have 2 conversion webhook entries
	if len(manifests.ConversionWebhooks) != 2 {
		t.Fatalf("expected 2 ConversionWebhooks, got %d", len(manifests.ConversionWebhooks))
	}

	cw := manifests.ConversionWebhooks[0]
	if cw.CRDName != "foos.example.com" {
		t.Errorf("CRDName = %q, want foos.example.com", cw.CRDName)
	}
	if cw.ServiceName != "controller-manager-webhook-service" {
		t.Errorf("ServiceName = %q, want controller-manager-webhook-service", cw.ServiceName)
	}
	if cw.WebhookPath != "/convert" {
		t.Errorf("WebhookPath = %q, want /convert", cw.WebhookPath)
	}
	if cw.ContainerPort != 9443 {
		t.Errorf("ContainerPort = %d, want 9443", cw.ContainerPort)
	}
	if len(cw.ConversionReviewVersions) != 2 || cw.ConversionReviewVersions[0] != "v1" {
		t.Errorf("ConversionReviewVersions = %v, want [v1 v1beta1]", cw.ConversionReviewVersions)
	}
}

func TestExtractFromCSV_ServiceDeduplication(t *testing.T) {
	deployments := []interface{}{
		map[string]interface{}{
			"name": "controller-manager",
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"app": "operator"},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{"app": "operator"},
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "manager",
								"image": "quay.io/example/operator:v1",
							},
						},
					},
				},
			},
		},
	}

	webhookDefs := []interface{}{
		map[string]interface{}{
			"type":                    "ValidatingAdmissionWebhook",
			"generateName":            "vfoo.example.com",
			"deploymentName":          "controller-manager",
			"containerPort":           int64(9443),
			"sideEffects":             "None",
			"failurePolicy":           "Fail",
			"admissionReviewVersions": []interface{}{"v1"},
			"webhookPath":             "/validate-v1-foo",
			"rules": []interface{}{
				map[string]interface{}{
					"apiGroups":   []interface{}{"example.com"},
					"apiVersions": []interface{}{"v1"},
					"operations":  []interface{}{"CREATE"},
					"resources":   []interface{}{"foos"},
				},
			},
		},
		map[string]interface{}{
			"type":                    "MutatingAdmissionWebhook",
			"generateName":            "mfoo.example.com",
			"deploymentName":          "controller-manager",
			"containerPort":           int64(9443),
			"sideEffects":             "None",
			"failurePolicy":           "Fail",
			"admissionReviewVersions": []interface{}{"v1"},
			"webhookPath":             "/mutate-v1-foo",
			"rules": []interface{}{
				map[string]interface{}{
					"apiGroups":   []interface{}{"example.com"},
					"apiVersions": []interface{}{"v1"},
					"operations":  []interface{}{"CREATE"},
					"resources":   []interface{}{"foos"},
				},
			},
		},
	}

	csv := newTestCSVWithWebhooks(t, "my-operator.v1.0.0", deployments, webhookDefs)
	manifests, err := extractFromCSV(csv)
	if err != nil {
		t.Fatalf("extractFromCSV() error: %v", err)
	}

	// Both webhooks target the same deployment, so only 1 Service should be generated
	if len(manifests.Services) != 1 {
		t.Errorf("expected 1 service (deduplicated), got %d", len(manifests.Services))
	}

	// Should have 2 webhook configurations
	if len(manifests.Other) != 2 {
		t.Errorf("expected 2 webhook configs, got %d", len(manifests.Other))
	}

	kinds := make(map[string]int)
	for _, obj := range manifests.Other {
		kinds[obj.GetKind()]++
	}
	if kinds["ValidatingWebhookConfiguration"] != 1 {
		t.Errorf("expected 1 ValidatingWebhookConfiguration, got %d", kinds["ValidatingWebhookConfiguration"])
	}
	if kinds["MutatingWebhookConfiguration"] != 1 {
		t.Errorf("expected 1 MutatingWebhookConfiguration, got %d", kinds["MutatingWebhookConfiguration"])
	}
}

func TestExtractFromCSV_NoWebhookDefinitions(t *testing.T) {
	deployments := []interface{}{
		map[string]interface{}{
			"name": "controller-manager",
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"app": "operator"},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{"app": "operator"},
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "manager",
								"image": "quay.io/example/operator:v1",
							},
						},
					},
				},
			},
		},
	}

	csv := newTestCSVWithWebhooks(t, "my-operator.v1.0.0", deployments, nil)
	manifests, err := extractFromCSV(csv)
	if err != nil {
		t.Fatalf("extractFromCSV() error: %v", err)
	}

	if len(manifests.Services) != 0 {
		t.Errorf("expected 0 services, got %d", len(manifests.Services))
	}
	if len(manifests.Other) != 0 {
		t.Errorf("expected 0 Other, got %d", len(manifests.Other))
	}
	if len(manifests.ConversionWebhooks) != 0 {
		t.Errorf("expected 0 ConversionWebhooks, got %d", len(manifests.ConversionWebhooks))
	}
}

// Ensure json import is used (for mustMarshal-style helpers if needed)
var _ = json.Marshal
