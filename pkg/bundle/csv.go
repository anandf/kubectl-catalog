package bundle

import (
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// extractFromCSV extracts Kubernetes resources from a ClusterServiceVersion.
// CSVs contain embedded deployment specs, RBAC rules, and other resources
// that need to be extracted and converted to standalone K8s objects.
func extractFromCSV(csv *unstructured.Unstructured) (*Manifests, error) {
	manifests := &Manifests{}
	csvName := csv.GetName()

	// Track which ServiceAccounts we've already created to avoid duplicates
	// (the same SA name often appears in both clusterPermissions and permissions)
	createdSAs := make(map[string]bool)

	// Extract deployments from spec.install.spec.deployments
	deployments, found, err := unstructured.NestedSlice(csv.Object, "spec", "install", "spec", "deployments")
	if err != nil {
		return nil, fmt.Errorf("reading deployments from CSV: %w", err)
	}
	if found {
		for _, d := range deployments {
			depMap, ok := d.(map[string]interface{})
			if !ok {
				continue
			}
			dep, err := convertToDeployment(depMap)
			if err != nil {
				return nil, fmt.Errorf("converting deployment: %w", err)
			}
			manifests.Deployments = append(manifests.Deployments, dep)
		}
	}

	// Extract cluster permissions (ClusterRoles/ClusterRoleBindings)
	clusterPermissions, found, err := unstructured.NestedSlice(csv.Object, "spec", "install", "spec", "clusterPermissions")
	if err != nil {
		return nil, fmt.Errorf("reading cluster permissions from CSV: %w", err)
	}
	if found {
		rbac, err := convertToClusterRBAC(clusterPermissions, csvName, createdSAs)
		if err != nil {
			return nil, fmt.Errorf("converting cluster permissions: %w", err)
		}
		manifests.RBAC = append(manifests.RBAC, rbac...)
	}

	// Extract namespace permissions (Roles/RoleBindings)
	permissions, found, err := unstructured.NestedSlice(csv.Object, "spec", "install", "spec", "permissions")
	if err != nil {
		return nil, fmt.Errorf("reading permissions from CSV: %w", err)
	}
	if found {
		rbac, err := convertToNamespacedRBAC(permissions, csvName, createdSAs)
		if err != nil {
			return nil, fmt.Errorf("converting permissions: %w", err)
		}
		manifests.RBAC = append(manifests.RBAC, rbac...)
	}

	return manifests, nil
}

func convertToDeployment(depMap map[string]interface{}) (*unstructured.Unstructured, error) {
	name, _ := depMap["name"].(string)
	spec, ok := depMap["spec"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("deployment %q has no spec", name)
	}

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	specData, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshaling deployment spec: %w", err)
	}
	if err := json.Unmarshal(specData, &deployment.Spec); err != nil {
		return nil, fmt.Errorf("unmarshaling deployment spec: %w", err)
	}

	return toUnstructured(deployment)
}

func convertToClusterRBAC(permissions []interface{}, csvName string, createdSAs map[string]bool) ([]*unstructured.Unstructured, error) {
	var result []*unstructured.Unstructured

	for _, perm := range permissions {
		permMap, ok := perm.(map[string]interface{})
		if !ok {
			continue
		}
		saName, _ := permMap["serviceAccountName"].(string)
		rules, _ := permMap["rules"].([]interface{})

		// Create ServiceAccount (only if not already created)
		if !createdSAs[saName] {
			sa := &corev1.ServiceAccount{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ServiceAccount",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: saName,
				},
			}
			saObj, err := toUnstructured(sa)
			if err != nil {
				return nil, err
			}
			result = append(result, saObj)
			createdSAs[saName] = true
		}

		// Create ClusterRole
		roleName := fmt.Sprintf("%s-%s", csvName, saName)
		var policyRules []rbacv1.PolicyRule
		rulesData, _ := json.Marshal(rules)
		_ = json.Unmarshal(rulesData, &policyRules)

		cr := &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: roleName,
			},
			Rules: policyRules,
		}
		crObj, err := toUnstructured(cr)
		if err != nil {
			return nil, err
		}
		result = append(result, crObj)

		// Create ClusterRoleBinding
		crb := &rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: roleName,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     roleName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "ServiceAccount",
					Name: saName,
				},
			},
		}
		crbObj, err := toUnstructured(crb)
		if err != nil {
			return nil, err
		}
		result = append(result, crbObj)
	}

	return result, nil
}

func convertToNamespacedRBAC(permissions []interface{}, csvName string, createdSAs map[string]bool) ([]*unstructured.Unstructured, error) {
	var result []*unstructured.Unstructured

	for _, perm := range permissions {
		permMap, ok := perm.(map[string]interface{})
		if !ok {
			continue
		}
		saName, _ := permMap["serviceAccountName"].(string)
		rules, _ := permMap["rules"].([]interface{})

		// Create ServiceAccount (only if not already created)
		if !createdSAs[saName] {
			sa := &corev1.ServiceAccount{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ServiceAccount",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: saName,
				},
			}
			saObj, err := toUnstructured(sa)
			if err != nil {
				return nil, err
			}
			result = append(result, saObj)
			createdSAs[saName] = true
		}

		// Create Role
		roleName := fmt.Sprintf("%s-%s", csvName, saName)
		var policyRules []rbacv1.PolicyRule
		rulesData, _ := json.Marshal(rules)
		_ = json.Unmarshal(rulesData, &policyRules)

		role := &rbacv1.Role{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "Role",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: roleName,
			},
			Rules: policyRules,
		}
		roleObj, err := toUnstructured(role)
		if err != nil {
			return nil, err
		}
		result = append(result, roleObj)

		// Create RoleBinding
		rb := &rbacv1.RoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "RoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: roleName,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     roleName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "ServiceAccount",
					Name: saName,
				},
			},
		}
		rbObj, err := toUnstructured(rb)
		if err != nil {
			return nil, err
		}
		result = append(result, rbObj)
	}

	return result, nil
}

func toUnstructured(obj interface{}) (*unstructured.Unstructured, error) {
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("converting to unstructured: %w", err)
	}
	return &unstructured.Unstructured{Object: data}, nil
}
