package bundle

import (
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
	metadataDir  = "metadata"
)

// InstallMode represents a supported OLM install mode.
type InstallMode struct {
	Type      string // AllNamespaces, OwnNamespace, SingleNamespace, MultiNamespace
	Supported bool
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

		obj := &unstructured.Unstructured{}
		jsonData, err := yaml.YAMLToJSON(data)
		if err != nil {
			return fmt.Errorf("converting YAML to JSON in %s: %w", path, err)
		}
		if err := json.Unmarshal(jsonData, &obj.Object); err != nil {
			return fmt.Errorf("unmarshaling %s: %w", path, err)
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
			if extracted.SuggestedNamespace != "" {
				manifests.SuggestedNamespace = extracted.SuggestedNamespace
			}
			if len(extracted.InstallModes) > 0 {
				manifests.InstallModes = extracted.InstallModes
			}
			return nil
		}

		classifyAndAdd(manifests, obj)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("walking bundle directory %s: %w", manifestDir, err)
	}

	return manifests, nil
}

// SetWatchNamespace injects the WATCH_NAMESPACE environment variable into all
// containers of all Deployments. For AllNamespaces mode, the value is "" (empty).
// For SingleNamespace/OwnNamespace mode, the value is the target namespace.
func (m *Manifests) SetWatchNamespace(watchNS string) {
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
		unstructured.SetNestedSlice(dep.Object, containers, "spec", "template", "spec", "containers")
	}
}

// SetEnvVars injects the given environment variables into all containers of all
// Deployments. If a variable with the same name already exists, its value is
// replaced. This mirrors the OLM Subscription spec.config.env behaviour.
func (m *Manifests) SetEnvVars(envVars map[string]string) {
	if len(envVars) == 0 {
		return
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
		unstructured.SetNestedSlice(dep.Object, containers, "spec", "template", "spec", "containers")
	}
}

// SetImagePullSecrets injects an imagePullSecrets entry into the pod template
// spec of all Deployments. This ensures pods have registry credentials from
// the moment they are created, avoiding ImagePullBackOff race conditions.
func (m *Manifests) SetImagePullSecrets(secretName string) {
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
			unstructured.SetNestedSlice(dep.Object, pullSecrets, "spec", "template", "spec", "imagePullSecrets")
		}
	}
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
func (m *Manifests) InjectWebhookCertVolumes(secretName string) bool {
	if len(m.Deployments) == 0 {
		return false
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
		unstructured.SetNestedSlice(dep.Object, volumes, "spec", "template", "spec", "volumes")

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
		unstructured.SetNestedSlice(dep.Object, containers, "spec", "template", "spec", "containers")
		injected = true
	}

	return injected
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
	switch {
	case gvk.Kind == "CustomResourceDefinition":
		m.CRDs = append(m.CRDs, obj)
	case gvk.Kind == "ClusterRole" || gvk.Kind == "ClusterRoleBinding" ||
		gvk.Kind == "Role" || gvk.Kind == "RoleBinding" ||
		gvk.Kind == "ServiceAccount":
		m.RBAC = append(m.RBAC, obj)
	case gvk.Kind == "Deployment":
		m.Deployments = append(m.Deployments, obj)
	case gvk.Kind == "Service":
		m.Services = append(m.Services, obj)
	default:
		m.Other = append(m.Other, obj)
	}
}
