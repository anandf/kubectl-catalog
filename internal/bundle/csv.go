package bundle

import (
	"encoding/json"
	"fmt"
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// extractFromCSV extracts Kubernetes resources from a ClusterServiceVersion.
// CSVs contain embedded deployment specs, RBAC rules, and other resources
// that need to be extracted and converted to standalone K8s objects.
func extractFromCSV(csv *unstructured.Unstructured) (*Manifests, error) {
	manifests := &Manifests{}
	csvName := csv.GetName()

	// Extract suggested namespace from CSV annotation
	annotations := csv.GetAnnotations()
	if annotations != nil {
		if ns, ok := annotations["operatorframework.io/suggested-namespace"]; ok && ns != "" {
			manifests.SuggestedNamespace = ns
		}
	}

	// Extract install modes from spec.installModes
	installModes, found, _ := unstructured.NestedSlice(csv.Object, "spec", "installModes")
	if found {
		for _, im := range installModes {
			imMap, ok := im.(map[string]interface{})
			if !ok {
				continue
			}
			modeType, _ := imMap["type"].(string)
			supported, _ := imMap["supported"].(bool)
			manifests.InstallModes = append(manifests.InstallModes, InstallMode{
				Type:      modeType,
				Supported: supported,
			})
		}
	}

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

	// Extract webhook definitions and generate Service + webhook configuration resources
	webhookDefs, found, err := unstructured.NestedSlice(csv.Object, "spec", "webhookdefinitions")
	if err != nil {
		return nil, fmt.Errorf("reading webhookdefinitions from CSV: %w", err)
	}
	if found && len(webhookDefs) > 0 {
		if err := extractWebhookDefinitions(webhookDefs, manifests); err != nil {
			return nil, fmt.Errorf("converting webhookdefinitions: %w", err)
		}
	}

	manifests.CSVMetadata = extractCSVMetadata(csv)

	return manifests, nil
}

func extractCSVMetadata(csv *unstructured.Unstructured) *CSVMetadata {
	meta := &CSVMetadata{}

	meta.DisplayName, _, _ = unstructured.NestedString(csv.Object, "spec", "displayName")
	meta.Description, _, _ = unstructured.NestedString(csv.Object, "spec", "description")
	meta.Version, _, _ = unstructured.NestedString(csv.Object, "spec", "version")
	meta.Maturity, _, _ = unstructured.NestedString(csv.Object, "spec", "maturity")
	meta.MinKubeVersion, _, _ = unstructured.NestedString(csv.Object, "spec", "minKubeVersion")

	if kw, found, _ := unstructured.NestedStringSlice(csv.Object, "spec", "keywords"); found {
		meta.Keywords = kw
	}

	if maintainers, found, _ := unstructured.NestedSlice(csv.Object, "spec", "maintainers"); found {
		for _, m := range maintainers {
			mMap, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := mMap["name"].(string)
			email, _ := mMap["email"].(string)
			if name != "" || email != "" {
				meta.Maintainers = append(meta.Maintainers, CSVMaintainer{Name: name, Email: email})
			}
		}
	}

	if providerMap, found, _ := unstructured.NestedMap(csv.Object, "spec", "provider"); found {
		meta.Provider.Name, _ = providerMap["name"].(string)
		meta.Provider.URL, _ = providerMap["url"].(string)
	}

	if links, found, _ := unstructured.NestedSlice(csv.Object, "spec", "links"); found {
		for _, l := range links {
			lMap, ok := l.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := lMap["name"].(string)
			url, _ := lMap["url"].(string)
			if name != "" || url != "" {
				meta.Links = append(meta.Links, CSVLink{Name: name, URL: url})
			}
		}
	}

	if icons, found, _ := unstructured.NestedSlice(csv.Object, "spec", "icon"); found && len(icons) > 0 {
		if iconMap, ok := icons[0].(map[string]interface{}); ok {
			data, _ := iconMap["base64data"].(string)
			mediaType, _ := iconMap["mediatype"].(string)
			if data != "" {
				meta.Icon = &CSVIcon{Data: data, MediaType: mediaType}
			}
		}
	}

	csvAnnotations := csv.GetAnnotations()
	if len(csvAnnotations) > 0 {
		meta.Annotations = make(map[string]string, len(csvAnnotations))
		for k, v := range csvAnnotations {
			meta.Annotations[k] = v
		}
	}

	return meta
}

func convertToDeployment(depMap map[string]interface{}) (*unstructured.Unstructured, error) {
	name, ok := depMap["name"].(string)
	if !ok || name == "" {
		return nil, fmt.Errorf("deployment entry is missing required 'name' field")
	}
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
		saName, ok := permMap["serviceAccountName"].(string)
		if !ok || saName == "" {
			return nil, fmt.Errorf("clusterPermissions entry is missing required 'serviceAccountName' field")
		}
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
		rulesData, err := json.Marshal(rules)
		if err != nil {
			return nil, fmt.Errorf("marshaling RBAC rules for %q: %w", saName, err)
		}
		if err := json.Unmarshal(rulesData, &policyRules); err != nil {
			return nil, fmt.Errorf("unmarshaling RBAC rules for %q: %w", saName, err)
		}

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
		saName, ok := permMap["serviceAccountName"].(string)
		if !ok || saName == "" {
			return nil, fmt.Errorf("permissions entry is missing required 'serviceAccountName' field")
		}
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
		rulesData, err := json.Marshal(rules)
		if err != nil {
			return nil, fmt.Errorf("marshaling RBAC rules for %q: %w", saName, err)
		}
		if err := json.Unmarshal(rulesData, &policyRules); err != nil {
			return nil, fmt.Errorf("unmarshaling RBAC rules for %q: %w", saName, err)
		}

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

// extractWebhookDefinitions processes the CSV's spec.webhookdefinitions array
// and generates the equivalent Kubernetes resources:
//   - Service resources to route traffic to operator deployments
//   - ValidatingWebhookConfiguration / MutatingWebhookConfiguration resources
//   - ConversionWebhookInfo entries for CRD patching
//
// This replicates what OLM does when it processes webhookdefinitions.
func extractWebhookDefinitions(webhookDefs []interface{}, manifests *Manifests) error {
	// Build a map of deploymentName -> pod template labels from CSV deployments
	// so generated Services can select the correct pods.
	deploymentLabels := make(map[string]map[string]string)
	for _, dep := range manifests.Deployments {
		name := dep.GetName()
		labels, _, _ := unstructured.NestedStringMap(dep.Object, "spec", "template", "metadata", "labels")
		if len(labels) > 0 {
			deploymentLabels[name] = labels
		}
	}

	// Track generated services by deployment name to avoid duplicates
	generatedServices := make(map[string]string) // deploymentName -> serviceName

	for _, wd := range webhookDefs {
		wdMap, ok := wd.(map[string]interface{})
		if !ok {
			continue
		}

		whType, _ := wdMap["type"].(string)
		deploymentName, _ := wdMap["deploymentName"].(string)
		if deploymentName == "" {
			return fmt.Errorf("webhookdefinition is missing required 'deploymentName' field")
		}

		containerPort := toInt32(wdMap["containerPort"], 443)
		serviceName := deploymentName + "-webhook-service"

		// Generate the Service if not already created for this deployment
		if _, exists := generatedServices[deploymentName]; !exists {
			selector := deploymentLabels[deploymentName]
			if len(selector) == 0 {
				selector = map[string]string{"app": deploymentName}
			}

			svc, err := buildWebhookService(serviceName, containerPort, selector)
			if err != nil {
				return fmt.Errorf("building service for deployment %q: %w", deploymentName, err)
			}
			manifests.Services = append(manifests.Services, svc)
			generatedServices[deploymentName] = serviceName
		}

		switch whType {
		case "ValidatingAdmissionWebhook":
			whConfig, err := buildAdmissionWebhookConfig(wdMap, serviceName, containerPort, false)
			if err != nil {
				return fmt.Errorf("building validating webhook config: %w", err)
			}
			manifests.Other = append(manifests.Other, whConfig)

		case "MutatingAdmissionWebhook":
			whConfig, err := buildAdmissionWebhookConfig(wdMap, serviceName, containerPort, true)
			if err != nil {
				return fmt.Errorf("building mutating webhook config: %w", err)
			}
			manifests.Other = append(manifests.Other, whConfig)

		case "ConversionWebhook":
			conversionCRDs, _ := wdMap["conversionCRDs"].([]interface{})
			webhookPath, _ := wdMap["webhookPath"].(string)
			reviewVersions := toStringSlice(wdMap["admissionReviewVersions"])

			for _, crd := range conversionCRDs {
				crdName, ok := crd.(string)
				if !ok || crdName == "" {
					continue
				}
				manifests.ConversionWebhooks = append(manifests.ConversionWebhooks, ConversionWebhookInfo{
					CRDName:                  crdName,
					ServiceName:              serviceName,
					WebhookPath:              webhookPath,
					ContainerPort:            containerPort,
					ConversionReviewVersions: reviewVersions,
				})
			}

		default:
			return fmt.Errorf("unknown webhook type %q", whType)
		}
	}

	return nil
}

func buildWebhookService(name string, port int32, selector map[string]string) (*unstructured.Unstructured, error) {
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:       port,
					TargetPort: intstr.FromInt32(port),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Selector: selector,
		},
	}
	return toUnstructured(svc)
}

func buildAdmissionWebhookConfig(wdMap map[string]interface{}, serviceName string, port int32, isMutating bool) (*unstructured.Unstructured, error) {
	generateName, _ := wdMap["generateName"].(string)
	name := strings.TrimRight(generateName, ".")
	if name == "" {
		return nil, fmt.Errorf("webhookdefinition is missing required 'generateName' field")
	}

	webhookPath, _ := wdMap["webhookPath"].(string)
	sideEffects := toSideEffectClass(wdMap["sideEffects"])
	failurePolicy := toFailurePolicy(wdMap["failurePolicy"])
	reviewVersions := toStringSlice(wdMap["admissionReviewVersions"])
	if len(reviewVersions) == 0 {
		reviewVersions = []string{"v1"}
	}

	// Build rules
	var rules []admissionregistrationv1.RuleWithOperations
	if rawRules, ok := wdMap["rules"].([]interface{}); ok {
		rulesData, err := json.Marshal(rawRules)
		if err != nil {
			return nil, fmt.Errorf("marshaling webhook rules: %w", err)
		}
		if err := json.Unmarshal(rulesData, &rules); err != nil {
			return nil, fmt.Errorf("unmarshaling webhook rules: %w", err)
		}
	}

	webhook := admissionregistrationv1.WebhookClientConfig{
		Service: &admissionregistrationv1.ServiceReference{
			Name: serviceName,
			Path: &webhookPath,
			Port: &port,
		},
	}

	if isMutating {
		reinvocationPolicy := admissionregistrationv1.NeverReinvocationPolicy
		if rp, ok := wdMap["reinvocationPolicy"].(string); ok && rp != "" {
			reinvocationPolicy = admissionregistrationv1.ReinvocationPolicyType(rp)
		}

		matchPolicy := admissionregistrationv1.Equivalent
		if mp, ok := wdMap["matchPolicy"].(string); ok && mp != "" {
			matchPolicy = admissionregistrationv1.MatchPolicyType(mp)
		}

		whEntry := admissionregistrationv1.MutatingWebhook{
			Name:                    generateName,
			ClientConfig:            webhook,
			Rules:                   rules,
			FailurePolicy:           &failurePolicy,
			SideEffects:             &sideEffects,
			AdmissionReviewVersions: reviewVersions,
			ReinvocationPolicy:      &reinvocationPolicy,
			MatchPolicy:             &matchPolicy,
		}

		if timeout := toInt32Ptr(wdMap["timeoutSeconds"]); timeout != nil {
			whEntry.TimeoutSeconds = timeout
		}
		if objSel := toLabelSelector(wdMap["objectSelector"]); objSel != nil {
			whEntry.ObjectSelector = objSel
		}

		cfg := &admissionregistrationv1.MutatingWebhookConfiguration{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "admissionregistration.k8s.io/v1",
				Kind:       "MutatingWebhookConfiguration",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Webhooks: []admissionregistrationv1.MutatingWebhook{whEntry},
		}
		return toUnstructured(cfg)
	}

	// Validating
	matchPolicy := admissionregistrationv1.Equivalent
	if mp, ok := wdMap["matchPolicy"].(string); ok && mp != "" {
		matchPolicy = admissionregistrationv1.MatchPolicyType(mp)
	}

	whEntry := admissionregistrationv1.ValidatingWebhook{
		Name:                    generateName,
		ClientConfig:            webhook,
		Rules:                   rules,
		FailurePolicy:           &failurePolicy,
		SideEffects:             &sideEffects,
		AdmissionReviewVersions: reviewVersions,
		MatchPolicy:             &matchPolicy,
	}

	if timeout := toInt32Ptr(wdMap["timeoutSeconds"]); timeout != nil {
		whEntry.TimeoutSeconds = timeout
	}
	if objSel := toLabelSelector(wdMap["objectSelector"]); objSel != nil {
		whEntry.ObjectSelector = objSel
	}

	cfg := &admissionregistrationv1.ValidatingWebhookConfiguration{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admissionregistration.k8s.io/v1",
			Kind:       "ValidatingWebhookConfiguration",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{whEntry},
	}
	return toUnstructured(cfg)
}

func toInt32(v interface{}, defaultVal int32) int32 {
	switch n := v.(type) {
	case int64:
		return int32(n)
	case float64:
		return int32(n)
	case int32:
		return n
	case int:
		return int32(n)
	default:
		return defaultVal
	}
}

func toInt32Ptr(v interface{}) *int32 {
	switch n := v.(type) {
	case int64:
		val := int32(n)
		return &val
	case float64:
		val := int32(n)
		return &val
	default:
		return nil
	}
}

func toStringSlice(v interface{}) []string {
	items, ok := v.([]interface{})
	if !ok {
		return nil
	}
	var result []string
	for _, item := range items {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func toSideEffectClass(v interface{}) admissionregistrationv1.SideEffectClass {
	if s, ok := v.(string); ok && s != "" {
		return admissionregistrationv1.SideEffectClass(s)
	}
	return admissionregistrationv1.SideEffectClassNone
}

func toFailurePolicy(v interface{}) admissionregistrationv1.FailurePolicyType {
	if s, ok := v.(string); ok && s != "" {
		return admissionregistrationv1.FailurePolicyType(s)
	}
	return admissionregistrationv1.Fail
}

func toLabelSelector(v interface{}) *metav1.LabelSelector {
	m, ok := v.(map[string]interface{})
	if !ok || len(m) == 0 {
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	sel := &metav1.LabelSelector{}
	if err := json.Unmarshal(data, sel); err != nil {
		return nil
	}
	return sel
}
