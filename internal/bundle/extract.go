package bundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const (
	// Standard OLM bundle directories
	manifestsDir = "manifests"
)

// InstallMode represents a supported OLM install mode.
type InstallMode struct {
	Type      string // AllNamespaces, OwnNamespace, SingleNamespace, MultiNamespace
	Supported bool
}

// ConversionWebhookInfo holds the information needed to patch a CRD with
// conversion webhook configuration. This is extracted from the CSV's
// spec.webhookdefinitions entries of type ConversionWebhook.
type ConversionWebhookInfo struct {
	CRDName                  string
	ServiceName              string
	WebhookPath              string
	ContainerPort            int32
	ConversionReviewVersions []string
}

// Manifests holds the extracted and transformed Kubernetes resources from a bundle.
type Manifests struct {
	CRDs        []*unstructured.Unstructured
	Deployments []*unstructured.Unstructured
	RBAC        []*unstructured.Unstructured
	Services    []*unstructured.Unstructured
	Other       []*unstructured.Unstructured

	// SuggestedNamespace is the namespace suggested by the CSV's
	// operatorframework.io/suggested-namespace annotation, if present.
	SuggestedNamespace string

	// InstallModes lists the install modes declared in the CSV's spec.installModes.
	InstallModes []InstallMode

	// ConversionWebhooks holds conversion webhook info extracted from the CSV's
	// spec.webhookdefinitions. These are used to patch CRDs with conversion config.
	ConversionWebhooks []ConversionWebhookInfo
}

// SupportsInstallMode returns true if the bundle supports the given install mode type.
func (m *Manifests) SupportsInstallMode(mode string) bool {
	for _, im := range m.InstallModes {
		if im.Type == mode && im.Supported {
			return true
		}
	}
	return false
}

// DefaultInstallMode returns the best default install mode based on the CSV's
// declared install modes. Priority: AllNamespaces > SingleNamespace > OwnNamespace.
// Returns "AllNamespaces" if no install modes are declared.
func (m *Manifests) DefaultInstallMode() string {
	if len(m.InstallModes) == 0 {
		return "AllNamespaces"
	}
	// Prefer AllNamespaces if supported
	for _, priority := range []string{"AllNamespaces", "SingleNamespace", "OwnNamespace"} {
		if m.SupportsInstallMode(priority) {
			return priority
		}
	}
	// Fallback — shouldn't happen if CSV is well-formed
	return "AllNamespaces"
}

// AllResources returns all manifests in the correct apply order:
// CRDs first, then RBAC, then Deployments, Services, and others.
func (m *Manifests) AllResources() []*unstructured.Unstructured {
	var all []*unstructured.Unstructured
	all = append(all, m.CRDs...)
	all = append(all, m.RBAC...)
	all = append(all, m.Deployments...)
	all = append(all, m.Services...)
	all = append(all, m.Other...)
	return all
}

// Extract reads a bundle directory and extracts Kubernetes manifests.
// It handles CSVs by converting them to their constituent Deployments, RBAC, etc.
// YAML files may contain multiple documents separated by "---".
func Extract(bundleDir string) (*Manifests, error) {
	manifestDir := filepath.Join(bundleDir, manifestsDir)
	if _, err := os.Stat(manifestDir); os.IsNotExist(err) {
		// Some bundles put manifests at the root
		manifestDir = bundleDir
	}

	manifests := &Manifests{}
	csvSeen := false

	err := filepath.Walk(manifestDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		// Split multi-document YAML files on "---" boundaries
		docs := splitYAMLDocuments(data)

		for docIdx, doc := range docs {
			obj := &unstructured.Unstructured{}
			jsonData, err := yaml.YAMLToJSON(doc)
			if err != nil {
				return fmt.Errorf("converting YAML to JSON in %s (document %d): %w", path, docIdx+1, err)
			}
			if err := json.Unmarshal(jsonData, &obj.Object); err != nil {
				return fmt.Errorf("unmarshaling %s (document %d): %w", path, docIdx+1, err)
			}
			if len(obj.Object) == 0 {
				continue // skip empty documents
			}

			gvk := obj.GroupVersionKind()

			// Handle ClusterServiceVersion specially - extract deployments and RBAC from it
			if gvk.Kind == "ClusterServiceVersion" {
				if csvSeen {
					fmt.Printf("  Warning: multiple CSVs found in bundle; using CSV from %s\n", path)
				}
				csvSeen = true
				extracted, err := extractFromCSV(obj)
				if err != nil {
					return fmt.Errorf("extracting from CSV in %s: %w", path, err)
				}
				manifests.Deployments = append(manifests.Deployments, extracted.Deployments...)
				manifests.RBAC = append(manifests.RBAC, extracted.RBAC...)
				manifests.Services = append(manifests.Services, extracted.Services...)
				manifests.Other = append(manifests.Other, extracted.Other...)
				manifests.ConversionWebhooks = append(manifests.ConversionWebhooks, extracted.ConversionWebhooks...)
				if extracted.SuggestedNamespace != "" {
					manifests.SuggestedNamespace = extracted.SuggestedNamespace
				}
				if len(extracted.InstallModes) > 0 {
					manifests.InstallModes = extracted.InstallModes
				}
				continue
			}

			classifyAndAdd(manifests, obj)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("walking bundle directory %s: %w", manifestDir, err)
	}

	// Patch CRDs with conversion webhook configuration from CSV webhookdefinitions
	err = patchCRDsForConversionWebhooks(manifests)
	if err != nil {
		return nil, fmt.Errorf("patching CRDs for conversion webhooks: %w", err)
	}

	return manifests, nil
}

// patchCRDsForConversionWebhooks sets spec.conversion on CRDs that are
// referenced by ConversionWebhook entries in the CSV's webhookdefinitions.
// CRDs that already have spec.conversion.webhook configured are left untouched.
func patchCRDsForConversionWebhooks(m *Manifests) error {
	if len(m.ConversionWebhooks) == 0 {
		return nil
	}

	// Index conversion webhooks by CRD name
	convByName := make(map[string]ConversionWebhookInfo)
	for _, cw := range m.ConversionWebhooks {
		convByName[cw.CRDName] = cw
	}

	for _, crd := range m.CRDs {
		cw, ok := convByName[crd.GetName()]
		if !ok {
			continue
		}

		// Skip CRDs that already have conversion webhook configured
		_, exists, _ := unstructured.NestedMap(crd.Object, "spec", "conversion", "webhook")
		if exists {
			continue
		}

		reviewVersions := make([]interface{}, len(cw.ConversionReviewVersions))
		for i, v := range cw.ConversionReviewVersions {
			reviewVersions[i] = v
		}
		if len(reviewVersions) == 0 {
			reviewVersions = []interface{}{"v1"}
		}

		conversion := map[string]interface{}{
			"strategy": "Webhook",
			"webhook": map[string]interface{}{
				"clientConfig": map[string]interface{}{
					"service": map[string]interface{}{
						"name": cw.ServiceName,
						"path": cw.WebhookPath,
						"port": int64(cw.ContainerPort),
					},
				},
				"conversionReviewVersions": reviewVersions,
			},
		}
		err := unstructured.SetNestedField(crd.Object, conversion, "spec", "conversion")
		if err != nil {
			return err
		}
	}
	return nil
}

// splitYAMLDocuments splits raw YAML data on "---" document separators.
// Returns at least one document. Empty documents (whitespace/comments only) are
// included but will be skipped during unmarshaling when obj.Object is empty.
func splitYAMLDocuments(data []byte) [][]byte {
	docs := bytes.Split(data, []byte("\n---"))
	var result [][]byte
	for _, doc := range docs {
		trimmed := bytes.TrimSpace(doc)
		if len(trimmed) == 0 {
			continue
		}
		// Skip documents that are only comments
		allComments := true
		for _, line := range bytes.Split(trimmed, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) > 0 && line[0] != '#' {
				allComments = false
				break
			}
		}
		if allComments {
			continue
		}
		result = append(result, doc)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// SetWatchNamespace injects the WATCH_NAMESPACE environment variable into all
// containers of all Deployments. For AllNamespaces mode, the value is "" (empty).
// For SingleNamespace/OwnNamespace mode, the value is the target namespace.
func (m *Manifests) SetWatchNamespace(watchNS string) error {
	for _, dep := range m.Deployments {
		containers, found, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
		if !found {
			continue
		}
		for i, c := range containers {
			container, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			env, _ := container["env"].([]interface{})
			// Remove existing WATCH_NAMESPACE if present
			var filtered []interface{}
			for _, e := range env {
				eMap, ok := e.(map[string]interface{})
				if !ok {
					filtered = append(filtered, e)
					continue
				}
				if eMap["name"] != "WATCH_NAMESPACE" {
					filtered = append(filtered, e)
				}
			}
			// Add the new WATCH_NAMESPACE entry
			filtered = append(filtered, map[string]interface{}{
				"name":  "WATCH_NAMESPACE",
				"value": watchNS,
			})
			container["env"] = filtered
			containers[i] = container
		}
		err := unstructured.SetNestedSlice(dep.Object, containers, "spec", "template", "spec", "containers")
		if err != nil {
			return err
		}
	}
	return nil
}

// SetEnvVars injects the given environment variables into all containers of all
// Deployments. If a variable with the same name already exists, its value is
// replaced. This mirrors the OLM Subscription spec.config.env behaviour.
func (m *Manifests) SetEnvVars(envVars map[string]string) error {
	if len(envVars) == 0 {
		return nil
	}
	for _, dep := range m.Deployments {
		containers, found, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
		if !found {
			continue
		}
		for i, c := range containers {
			container, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			env, _ := container["env"].([]interface{})

			// Build a set of keys we're injecting so we can remove existing entries
			for key, value := range envVars {
				var filtered []interface{}
				for _, e := range env {
					eMap, ok := e.(map[string]interface{})
					if !ok {
						filtered = append(filtered, e)
						continue
					}
					if eMap["name"] != key {
						filtered = append(filtered, e)
					}
				}
				filtered = append(filtered, map[string]interface{}{
					"name":  key,
					"value": value,
				})
				env = filtered
			}

			container["env"] = env
			containers[i] = container
		}
		err := unstructured.SetNestedSlice(dep.Object, containers, "spec", "template", "spec", "containers")
		if err != nil {
			return err
		}
	}
	return nil
}

// SetImagePullSecrets injects an imagePullSecrets entry into the pod template
// spec of all Deployments. This ensures pods have registry credentials from
// the moment they are created, avoiding ImagePullBackOff race conditions.
func (m *Manifests) SetImagePullSecrets(secretName string) error {
	for _, dep := range m.Deployments {
		pullSecrets, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "imagePullSecrets")

		// Check if already present
		alreadyPresent := false
		for _, ps := range pullSecrets {
			if psMap, ok := ps.(map[string]interface{}); ok {
				if psMap["name"] == secretName {
					alreadyPresent = true
					break
				}
			}
		}

		if !alreadyPresent {
			pullSecrets = append(pullSecrets, map[string]interface{}{
				"name": secretName,
			})
			err := unstructured.SetNestedSlice(dep.Object, pullSecrets, "spec", "template", "spec", "imagePullSecrets")
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// WebhookCertSecretName is the conventional name for the webhook serving cert
// secret injected by kubectl-catalog on vanilla Kubernetes.
const WebhookCertSecretName = "webhook-server-cert"

// WebhookCertMountPath is the default path where controller-runtime's webhook
// server looks for serving certificates.
const WebhookCertMountPath = "/tmp/k8s-webhook-server/serving-certs"

// InjectWebhookCertVolumes detects if the operator needs webhook serving certs
// and injects a volume + volumeMount into the Deployment to mount the TLS secret
// at the controller-runtime default path.
// On OpenShift, OLM handles this injection. On vanilla k8s, we do it ourselves.
//
// Detection is based on multiple signals:
//  1. Webhook configurations in the bundle (ValidatingWebhookConfiguration/MutatingWebhookConfiguration)
//  2. Container ports matching controller-runtime's default webhook port (9443)
//  3. Container args/command containing "webhook" keywords
//  4. Environment variables referencing webhook or cert paths
func (m *Manifests) InjectWebhookCertVolumes(secretName string) (bool, error) {
	if len(m.Deployments) == 0 {
		return false, nil
	}

	// Check for explicit webhook configurations in the bundle
	hasWebhookConfigs := false
	for _, obj := range m.Other {
		kind := obj.GetKind()
		if kind == "ValidatingWebhookConfiguration" || kind == "MutatingWebhookConfiguration" {
			hasWebhookConfigs = true
			break
		}
	}

	injected := false
	volumeName := "webhook-server-cert"

	for _, dep := range m.Deployments {
		// Skip if already has a volume mount at the webhook cert path
		if hasVolumeMount(dep, WebhookCertMountPath) {
			continue
		}

		// Determine if this Deployment needs webhook certs
		if !hasWebhookConfigs && !deploymentUsesWebhooks(dep) {
			continue
		}

		// Add the volume
		volumes, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "volumes")
		volumes = append(volumes, map[string]interface{}{
			"name": volumeName,
			"secret": map[string]interface{}{
				"secretName":  secretName,
				"defaultMode": int64(420), // 0644
			},
		})
		err := unstructured.SetNestedSlice(dep.Object, volumes, "spec", "template", "spec", "volumes")
		if err != nil {
			return false, err
		}

		// Add the volume mount to the manager container
		containers, found, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
		if !found || len(containers) == 0 {
			continue
		}

		// Find the manager container (first non-kube-rbac-proxy container)
		managerIdx := 0
		for i, c := range containers {
			container, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := container["name"].(string)
			if name != "kube-rbac-proxy" {
				managerIdx = i
				break
			}
		}

		container, ok := containers[managerIdx].(map[string]interface{})
		if !ok {
			continue
		}

		mounts, _ := container["volumeMounts"].([]interface{})
		mounts = append(mounts, map[string]interface{}{
			"name":      volumeName,
			"mountPath": WebhookCertMountPath,
			"readOnly":  true,
		})
		container["volumeMounts"] = mounts
		containers[managerIdx] = container
		err = unstructured.SetNestedSlice(dep.Object, containers, "spec", "template", "spec", "containers")
		if err != nil {
			return false, err
		}
		injected = true
	}

	return injected, nil
}

// deploymentUsesWebhooks inspects a Deployment for signs that it runs a webhook server:
//   - Container port 9443 (controller-runtime default webhook port)
//   - Args containing "webhook", "enable-webhooks", or cert path references
//   - Environment variables referencing webhook cert paths
func deploymentUsesWebhooks(dep *unstructured.Unstructured) bool {
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		container, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := container["name"].(string)
		if name == "kube-rbac-proxy" {
			continue // skip sidecar
		}

		// Check container ports for webhook port (9443)
		ports, _ := container["ports"].([]interface{})
		for _, p := range ports {
			port, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			// controller-runtime default webhook port
			if portNum, ok := port["containerPort"].(int64); ok && portNum == 9443 {
				return true
			}
			// Also check float64 since JSON unmarshaling may use it
			if portNum, ok := port["containerPort"].(float64); ok && portNum == 9443 {
				return true
			}
		}

		// Check args for webhook-related flags
		args, _ := container["args"].([]interface{})
		for _, a := range args {
			arg, ok := a.(string)
			if !ok {
				continue
			}
			if containsWebhookKeyword(arg) {
				return true
			}
		}

		// Check command for webhook-related keywords
		command, _ := container["command"].([]interface{})
		for _, c := range command {
			cmd, ok := c.(string)
			if !ok {
				continue
			}
			if containsWebhookKeyword(cmd) {
				return true
			}
		}

		// Check env vars for webhook cert path references
		env, _ := container["env"].([]interface{})
		for _, e := range env {
			envVar, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			val, _ := envVar["value"].(string)
			if containsWebhookKeyword(val) {
				return true
			}
		}
	}
	return false
}

// containsWebhookKeyword checks if a string contains webhook-related keywords.
func containsWebhookKeyword(s string) bool {
	keywords := []string{
		"webhook", "serving-cert", "k8s-webhook-server",
	}
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// hasVolumeMount checks if a Deployment already has a volume mount at the given path.
func hasVolumeMount(dep *unstructured.Unstructured, mountPath string) bool {
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		container, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		mounts, _ := container["volumeMounts"].([]interface{})
		for _, m := range mounts {
			mount, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			if path, _ := mount["mountPath"].(string); path == mountPath {
				return true
			}
		}
	}
	return false
}

func classifyAndAdd(m *Manifests, obj *unstructured.Unstructured) {
	gvk := obj.GroupVersionKind()
	switch gvk.Kind {
	case "CustomResourceDefinition":
		m.CRDs = append(m.CRDs, obj)
	case "ClusterRole":
	case "ClusterRoleBinding":
	case "Role":
	case "RoleBinding":
	case "ServiceAccount":
		m.RBAC = append(m.RBAC, obj)
	case "Deployment":
		m.Deployments = append(m.Deployments, obj)
	case "Service":
		m.Services = append(m.Services, obj)
	default:
		m.Other = append(m.Other, obj)
	}
}
