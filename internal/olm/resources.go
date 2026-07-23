package olm

import (
	"crypto/sha256"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CatalogSourceName returns a deterministic name for a CatalogSource.
// When catalogType is a known type (redhat, community, etc.), the name is
// "kubectl-catalog-<type>". For custom catalog images, a hash-based name is used.
func CatalogSourceName(catalogType, catalogImage string) string {
	knownTypes := map[string]bool{
		"redhat": true, "community": true, "certified": true, "operatorhub": true,
	}
	if knownTypes[catalogType] {
		return "kubectl-catalog-" + catalogType
	}
	hash := sha256.Sum256([]byte(catalogImage))
	return fmt.Sprintf("kubectl-catalog-%x", hash[:4])
}

// NewCatalogSource builds an unstructured CatalogSource resource.
func NewCatalogSource(name, namespace, image, displayName, pullSecretName string) *unstructured.Unstructured {
	spec := map[string]interface{}{
		"sourceType":  "grpc",
		"image":       image,
		"displayName": displayName,
		"publisher":   "kubectl-catalog",
	}

	if pullSecretName != "" {
		spec["secrets"] = []interface{}{pullSecretName}
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "CatalogSource",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
				},
			},
			"spec": spec,
		},
	}
}

// NewOperatorGroup builds an unstructured OperatorGroup resource.
// An empty targetNamespaces slice creates an AllNamespaces OperatorGroup.
func NewOperatorGroup(name, namespace string, targetNamespaces []string) *unstructured.Unstructured {
	spec := map[string]interface{}{}
	if len(targetNamespaces) > 0 {
		targets := make([]interface{}, len(targetNamespaces))
		for i, ns := range targetNamespaces {
			targets[i] = ns
		}
		spec["targetNamespaces"] = targets
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1",
			"kind":       "OperatorGroup",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
				},
			},
			"spec": spec,
		},
	}
}

// NewSubscription builds an unstructured Subscription resource.
// startingCSV is optional — pass empty string to let OLM pick the channel head.
func NewSubscription(packageName, namespace, channel, source, sourceNamespace, startingCSV string) *unstructured.Unstructured {
	spec := map[string]interface{}{
		"channel":             channel,
		"name":                packageName,
		"source":              source,
		"sourceNamespace":     sourceNamespace,
		"installPlanApproval": "Automatic",
	}

	if startingCSV != "" {
		spec["startingCSV"] = startingCSV
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]interface{}{
				"name":      packageName,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "kubectl-catalog",
					"kubectl-catalog.io/package":   packageName,
				},
			},
			"spec": spec,
		},
	}
}

func SubscriptionGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "subscriptions",
	}
}

func CatalogSourceGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "catalogsources",
	}
}

func OperatorGroupGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1",
		Resource: "operatorgroups",
	}
}

func CSVGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "clusterserviceversions",
	}
}
