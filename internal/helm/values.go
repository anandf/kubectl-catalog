package helm

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// imageConfig represents a decomposed container image reference.
type imageConfig struct {
	Registry   string `json:"registry,omitempty"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	PullPolicy string `json:"pullPolicy"`
}

// imageMapping tracks the mapping from original image string to values key.
type imageMapping struct {
	Key      string
	Original string
	Config   imageConfig
}

func generateValuesYAML(g *ChartGenerator) string {
	var b strings.Builder

	b.WriteString("# Default values for " + sanitizeChartName(g.PackageName) + ".\n\n")

	b.WriteString("nameOverride: \"\"\nfullnameOverride: \"\"\n\n")

	// Replicas
	replicas := extractReplicaCount(g.Manifests.Deployments)
	b.WriteString(fmt.Sprintf("replicaCount: %d\n\n", replicas))

	// Images
	images := extractImages(g.Manifests.Deployments)
	b.WriteString("image:\n")
	if len(images) == 0 {
		b.WriteString("  manager:\n    repository: \"\"\n    tag: \"\"\n    pullPolicy: IfNotPresent\n")
	} else {
		for _, img := range images {
			b.WriteString(fmt.Sprintf("  %s:\n", img.Key))
			if img.Config.Registry != "" {
				b.WriteString(fmt.Sprintf("    registry: %s\n", img.Config.Registry))
			}
			b.WriteString(fmt.Sprintf("    repository: %s\n", img.Config.Repository))
			b.WriteString(fmt.Sprintf("    tag: %q\n", img.Config.Tag))
			b.WriteString(fmt.Sprintf("    pullPolicy: %s\n", img.Config.PullPolicy))
		}
	}

	b.WriteString("\nimagePullSecrets: []\n\n")

	// ServiceAccount
	b.WriteString("serviceAccount:\n  create: true\n  name: \"\"\n  annotations: {}\n\n")

	// RBAC
	b.WriteString("rbac:\n  create: true\n\n")

	// Resources
	b.WriteString("# resources:\n")
	b.WriteString("#   limits:\n#     cpu: 500m\n#     memory: 128Mi\n")
	b.WriteString("#   requests:\n#     cpu: 10m\n#     memory: 64Mi\n")
	b.WriteString("resources: {}\n\n")

	// Probes
	b.WriteString("# livenessProbe:\n#   httpGet:\n#     path: /healthz\n#     port: 8081\n#   initialDelaySeconds: 15\n#   periodSeconds: 20\n")
	b.WriteString("livenessProbe: {}\n\n")
	b.WriteString("# readinessProbe:\n#   httpGet:\n#     path: /readyz\n#     port: 8081\n#   initialDelaySeconds: 5\n#   periodSeconds: 10\n")
	b.WriteString("readinessProbe: {}\n\n")

	// Container args
	b.WriteString("# args:\n#   - --leader-elect\n#   - --health-probe-bind-address=:8081\n")
	b.WriteString("args: []\n\n")

	// Service
	b.WriteString("service:\n")
	b.WriteString("  type: ClusterIP\n")
	b.WriteString("  annotations: {}\n\n")

	// Scheduling
	b.WriteString("nodeSelector: {}\n\ntolerations: []\n\naffinity: {}\n\n")

	// Topology spread constraints
	b.WriteString("topologySpreadConstraints: []\n\n")

	// Security contexts
	b.WriteString("podSecurityContext: {}\nsecurityContext: {}\n\n")

	// Labels and annotations
	b.WriteString("commonLabels: {}\ncommonAnnotations: {}\n")
	b.WriteString("podAnnotations: {}\n\n")

	// Priority class
	b.WriteString("priorityClassName: \"\"\n\n")

	// Deployment strategy
	b.WriteString("# strategy:\n#   type: RollingUpdate\n#   rollingUpdate:\n#     maxSurge: 1\n#     maxUnavailable: 0\n")
	b.WriteString("strategy: {}\n\n")

	// Revision history
	revisionHistoryLimit := extractRevisionHistoryLimit(g.Manifests.Deployments)
	b.WriteString(fmt.Sprintf("revisionHistoryLimit: %d\n\n", revisionHistoryLimit))

	// Extra volumes and mounts
	b.WriteString("extraVolumes: []\nextraVolumeMounts: []\n\n")

	// envFrom
	b.WriteString("# envFrom:\n#   - configMapRef:\n#       name: my-config\n#   - secretRef:\n#       name: my-secret\n")
	b.WriteString("envFrom: []\n\n")

	// Cert-manager
	certManagerEnabled := strings.EqualFold(g.CertProvider, "cert-manager")
	b.WriteString("certManager:\n")
	b.WriteString(fmt.Sprintf("  enabled: %t\n", certManagerEnabled))
	b.WriteString("  issuer:\n    kind: Issuer\n    name: \"\"\n")
	b.WriteString("  duration: \"8760h\"\n  renewBefore: \"720h\"\n\n")

	// Monitoring
	hasMonitoring := hasMonitoringResources(g.Manifests.Other)
	b.WriteString("monitoring:\n")
	b.WriteString(fmt.Sprintf("  enabled: %t\n\n", hasMonitoring))

	// Webhooks
	hasWebhooks := hasWebhookResources(g.Manifests.Other)
	b.WriteString("webhooks:\n")
	b.WriteString(fmt.Sprintf("  enabled: %t\n\n", hasWebhooks))

	// Operator image env vars (RELATED_IMAGE_*, etc.)
	envImages := extractEnvImageVars(g.Manifests.Deployments)
	if len(envImages) > 0 {
		b.WriteString("# Operator image environment variables.\n")
		b.WriteString("# Override these to use custom builds or mirrors.\n")
		b.WriteString("operatorImageEnv:\n")
		for _, ei := range envImages {
			b.WriteString(fmt.Sprintf("  %s: %q\n", ei.Key, ei.Value))
		}
		b.WriteString("\n")
	}

	// Extra env vars (user-defined)
	b.WriteString("# env:\n#   LOG_LEVEL: info\n")
	b.WriteString("env: {}\n\n")

	// Install mode
	mode := g.InstallMode
	if mode == "" {
		mode = "AllNamespaces"
	}
	b.WriteString(fmt.Sprintf("installMode: %q\nwatchNamespace: \"\"\n", mode))

	return b.String()
}

func extractReplicaCount(deployments []*unstructured.Unstructured) int64 {
	for _, dep := range deployments {
		replicas, found, _ := unstructured.NestedInt64(dep.Object, "spec", "replicas")
		if found && replicas > 0 {
			return replicas
		}
	}
	return 1
}

func extractRevisionHistoryLimit(deployments []*unstructured.Unstructured) int64 {
	for _, dep := range deployments {
		limit, found, _ := unstructured.NestedInt64(dep.Object, "spec", "revisionHistoryLimit")
		if found && limit > 0 {
			return limit
		}
	}
	return 10
}

func extractImages(deployments []*unstructured.Unstructured) []imageMapping {
	seen := make(map[string]bool)
	var result []imageMapping

	for _, dep := range deployments {
		containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
		for _, c := range containers {
			cMap, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			image, _ := cMap["image"].(string)
			name, _ := cMap["name"].(string)
			if image == "" || name == "" {
				continue
			}

			key := toCamelCase(name)
			if seen[key] {
				depName := dep.GetName()
				key = toCamelCase(depName) + strings.ToUpper(key[:1]) + key[1:]
			}
			if seen[key] {
				continue
			}
			seen[key] = true

			pullPolicy, _ := cMap["imagePullPolicy"].(string)
			if pullPolicy == "" {
				pullPolicy = "IfNotPresent"
			}

			result = append(result, imageMapping{
				Key:      key,
				Original: image,
				Config:   decomposeImage(image, pullPolicy),
			})
		}
	}
	return result
}

func decomposeImage(ref, pullPolicy string) imageConfig {
	tag := "latest"
	repo := ref

	if idx := strings.LastIndex(ref, "@"); idx >= 0 {
		repo = ref[:idx]
		tag = ref[idx+1:]
	} else if idx := strings.LastIndex(ref, ":"); idx >= 0 {
		afterColon := ref[idx+1:]
		if !strings.Contains(afterColon, "/") {
			repo = ref[:idx]
			tag = afterColon
		}
	}

	// Split registry from repository
	registry := ""
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) == 2 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		registry = parts[0]
		repo = parts[1]
	}

	return imageConfig{
		Registry:   registry,
		Repository: repo,
		Tag:        tag,
		PullPolicy: pullPolicy,
	}
}

var nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func toCamelCase(s string) string {
	parts := nonAlphaNum.Split(s, -1)
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	result := strings.Join(parts, "")
	if len(result) > 0 {
		result = strings.ToLower(result[:1]) + result[1:]
	}
	return result
}

// envImageVar represents a canonical image env var entry for values.yaml.
// Multiple env vars (e.g. ARGOCD_IMAGE and RELATED_IMAGE_ARGOCD_IMAGE)
// map to the same canonical key.
type envImageVar struct {
	Key   string // canonical values key (e.g. ARGOCD_IMAGE)
	Value string // default image reference
}

// canonicalEnvImageName strips the RELATED_IMAGE_ prefix to produce a
// canonical key. Both RELATED_IMAGE_ARGOCD_IMAGE and ARGOCD_IMAGE map
// to ARGOCD_IMAGE.
func canonicalEnvImageName(name string) string {
	return strings.TrimPrefix(name, "RELATED_IMAGE_")
}

// extractEnvImageVars finds all env vars across all containers in all deployments
// whose values look like container image references, deduplicated by canonical name.
func extractEnvImageVars(deployments []*unstructured.Unstructured) []envImageVar {
	canonical := make(map[string]string) // canonical key → image value
	var order []string                   // preserve insertion order

	for _, dep := range deployments {
		containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
		for _, c := range containers {
			cMap, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			env, ok := cMap["env"].([]interface{})
			if !ok {
				continue
			}
			for _, e := range env {
				eMap, ok := e.(map[string]interface{})
				if !ok {
					continue
				}
				name, _ := eMap["name"].(string)
				value, _ := eMap["value"].(string)
				if name == "" || value == "" {
					continue
				}
				if !looksLikeImageRef(value) {
					continue
				}
				key := canonicalEnvImageName(name)
				if _, exists := canonical[key]; !exists {
					canonical[key] = value
					order = append(order, key)
				}
			}
		}
	}

	var result []envImageVar
	for _, key := range order {
		result = append(result, envImageVar{Key: key, Value: canonical[key]})
	}
	return result
}

func looksLikeImageRef(s string) bool {
	// Must contain a slash (registry/repo or repo/image)
	if !strings.Contains(s, "/") {
		return false
	}
	// Must contain a tag separator (:) or digest separator (@)
	return strings.Contains(s, ":") || strings.Contains(s, "@")
}

func hasMonitoringResources(resources []*unstructured.Unstructured) bool {
	for _, obj := range resources {
		gvk := obj.GroupVersionKind()
		if strings.HasSuffix(gvk.Group, "monitoring.coreos.com") || gvk.Group == "monitoring.coreos.com" {
			return true
		}
	}
	return false
}

func hasWebhookResources(resources []*unstructured.Unstructured) bool {
	for _, obj := range resources {
		kind := obj.GetKind()
		if kind == "ValidatingWebhookConfiguration" || kind == "MutatingWebhookConfiguration" {
			return true
		}
	}
	return false
}
