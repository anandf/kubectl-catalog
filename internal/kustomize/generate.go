package kustomize

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

type KustomizeGenerator struct {
	PackageName      string
	Version          string
	Channel          string
	CatalogRef       string
	Manifests        *bundle.Manifests
	CertProvider     string
	InstallMode      string
	Namespace        string
	PullSecretLabels map[string]string
	SkipCRDs         bool
	SkipTemplates    bool
	MirrorPrefix     string
	CheckImage       func(imageRef string) bool
}

func (g *KustomizeGenerator) Generate(outputDir string) error {
	baseDir := filepath.Join(outputDir, "base")
	overlayDir := filepath.Join(outputDir, "overlays", "default")

	dirs := []string{baseDir, overlayDir}
	if !g.SkipCRDs {
		dirs = append(dirs, filepath.Join(baseDir, "crds"))
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	// 1. Write CRDs to base/crds/
	var crdFiles []string
	if !g.SkipCRDs {
		crdsDir := filepath.Join(baseDir, "crds")
		crdNameCount := make(map[string]int)
		for _, crd := range g.Manifests.CRDs {
			name := crd.GetName()
			if name == "" {
				name = "unnamed-crd"
			}
			safeName := strings.ToLower(strings.ReplaceAll(name, "/", "-"))
			crdNameCount[safeName]++
			if crdNameCount[safeName] > 1 {
				safeName = fmt.Sprintf("%s-%d", safeName, crdNameCount[safeName])
			}
			filename := safeName + ".yaml"
			data, err := yaml.Marshal(crd.Object)
			if err != nil {
				return fmt.Errorf("marshaling CRD %s: %w", name, err)
			}
			if err := os.WriteFile(filepath.Join(crdsDir, filename), data, 0o644); err != nil {
				return fmt.Errorf("writing CRD %s: %w", name, err)
			}
			crdFiles = append(crdFiles, "crds/"+filename)
		}
	}

	// 2. Write all other resources to base/, one file per resource
	var resourceFiles []string
	if !g.SkipTemplates {
		fileCountByName := make(map[string]int)

		writeResources := func(resources []*unstructured.Unstructured) error {
			for _, obj := range resources {
				filename := resourceFilename(obj)
				fileCountByName[filename]++
				if fileCountByName[filename] > 1 {
					ext := filepath.Ext(filename)
					base := strings.TrimSuffix(filename, ext)
					filename = fmt.Sprintf("%s-%d%s", base, fileCountByName[filename], ext)
				}
				data, err := yaml.Marshal(obj.Object)
				if err != nil {
					return fmt.Errorf("marshaling %s/%s: %w", obj.GetKind(), obj.GetName(), err)
				}
				if err := os.WriteFile(filepath.Join(baseDir, filename), data, 0o644); err != nil {
					return fmt.Errorf("writing %s: %w", filename, err)
				}
				resourceFiles = append(resourceFiles, filename)
			}
			return nil
		}

		// 3. Generate pull secret and wire it into ServiceAccounts
		pullSecretName := strings.ToLower(strings.ReplaceAll(g.PackageName, "_", "-")) + "-pull-secret"
		pullSecretContent := generatePullSecret(pullSecretName, g.Namespace, g.PullSecretLabels)
		if err := os.WriteFile(filepath.Join(baseDir, "pull-secret.yaml"), []byte(pullSecretContent), 0o644); err != nil {
			return fmt.Errorf("writing pull-secret.yaml: %w", err)
		}
		resourceFiles = append(resourceFiles, "pull-secret.yaml")

		injectImagePullSecrets(g.Manifests.RBAC, pullSecretName)

		if err := writeResources(g.Manifests.RBAC); err != nil {
			return err
		}
		if g.MirrorPrefix != "" && g.CheckImage != nil {
			mirrorEnvImageVars(g.Manifests.Deployments, g.MirrorPrefix, g.CheckImage)
		}
		if err := writeResources(g.Manifests.Deployments); err != nil {
			return err
		}
		if err := writeResources(g.Manifests.Services); err != nil {
			return err
		}
		if err := writeResources(g.Manifests.Other); err != nil {
			return err
		}
	}

	// 4. Generate base/kustomization.yaml
	allFiles := append(crdFiles, resourceFiles...)
	baseKustomization := generateBaseKustomization(g.PackageName, allFiles)
	if err := os.WriteFile(filepath.Join(baseDir, "kustomization.yaml"), []byte(baseKustomization), 0o644); err != nil {
		return fmt.Errorf("writing base kustomization.yaml: %w", err)
	}

	// 5. Generate overlays/default/kustomization.yaml
	if !g.SkipTemplates {
		pullSecretName := strings.ToLower(strings.ReplaceAll(g.PackageName, "_", "-")) + "-pull-secret"
		overlayKustomization := generateOverlayKustomization(g, pullSecretName)
		if err := os.WriteFile(filepath.Join(overlayDir, "kustomization.yaml"), []byte(overlayKustomization), 0o644); err != nil {
			return fmt.Errorf("writing overlay kustomization.yaml: %w", err)
		}

		// 6. Generate overlays/default/resources.yaml (commented-out merge patch)
		resourcesPatch := generateResourcePatch(g)
		if resourcesPatch != "" {
			if err := os.WriteFile(filepath.Join(overlayDir, "resources.yaml"), []byte(resourcesPatch), 0o644); err != nil {
				return fmt.Errorf("writing resources.yaml: %w", err)
			}
		}
	} else {
		// Minimal overlay for CRDs-only
		overlayKustomization := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n\nresources:\n  - ../../base\n"
		if err := os.WriteFile(filepath.Join(overlayDir, "kustomization.yaml"), []byte(overlayKustomization), 0o644); err != nil {
			return fmt.Errorf("writing overlay kustomization.yaml: %w", err)
		}
	}

	fmt.Printf("  Kustomize manifests generated at %s\n", outputDir)
	fmt.Printf("    Base resources: %d\n", len(allFiles))
	if !g.SkipCRDs {
		fmt.Printf("    CRDs: %d\n", len(g.Manifests.CRDs))
	}

	return nil
}

func resourceFilename(obj *unstructured.Unstructured) string {
	kind := obj.GetKind()
	name := strings.ToLower(obj.GetName())

	switch kind {
	case "ServiceAccount":
		return "serviceaccount.yaml"
	case "ClusterRole":
		return "clusterrole.yaml"
	case "ClusterRoleBinding":
		return "clusterrolebinding.yaml"
	case "Role":
		return "role.yaml"
	case "RoleBinding":
		return "rolebinding.yaml"
	case "Deployment":
		return "deployment.yaml"
	case "Service":
		return "service.yaml"
	case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
		return "webhook-config.yaml"
	case "ServiceMonitor", "PodMonitor", "PrometheusRule":
		return "servicemonitor.yaml"
	case "ConfigMap":
		if strings.Contains(name, "config") || strings.Contains(name, "manager") {
			return "manager-config.yaml"
		}
		return "configmap.yaml"
	case "Secret":
		if strings.Contains(name, "token") || strings.Contains(name, "bearer") {
			return "serviceaccount-token.yaml"
		}
		if strings.Contains(name, "cert") || strings.Contains(name, "tls") {
			return "tls-secret.yaml"
		}
		return "secret.yaml"
	case "NetworkPolicy":
		return "networkpolicy.yaml"
	case "PodDisruptionBudget":
		return "pdb.yaml"
	case "HorizontalPodAutoscaler":
		return "hpa.yaml"
	case "PriorityClass":
		return "priorityclass.yaml"
	case "Ingress":
		return "ingress.yaml"
	default:
		safeName := strings.ToLower(fmt.Sprintf("%s-%s", kind, obj.GetName()))
		safeName = strings.ReplaceAll(safeName, "/", "-")
		safeName = strings.ReplaceAll(safeName, ":", "-")
		return safeName + ".yaml"
	}
}

func generateBaseKustomization(packageName string, resources []string) string {
	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n\n")

	safeName := strings.ToLower(strings.ReplaceAll(packageName, "_", "-"))
	b.WriteString("commonLabels:\n")
	b.WriteString(fmt.Sprintf("  app.kubernetes.io/name: %s\n", safeName))
	b.WriteString("  app.kubernetes.io/managed-by: kustomize\n\n")

	b.WriteString("resources:\n")
	for _, f := range resources {
		b.WriteString(fmt.Sprintf("  - %s\n", f))
	}

	return b.String()
}

func generateOverlayKustomization(g *KustomizeGenerator, pullSecretName string) string {
	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n\n")

	b.WriteString("resources:\n  - ../../base\n\n")

	// Namespace
	ns := g.Namespace
	if ns == "" {
		ns = "default"
	}
	b.WriteString(fmt.Sprintf("namespace: %s\n\n", ns))

	// Images transformer — container images
	images := collectAllImages(g.Manifests)
	if g.MirrorPrefix != "" && g.CheckImage != nil {
		for i, img := range images {
			fullRef := img.original
			if !g.CheckImage(fullRef) {
				mirrored := mirrorImageRef(fullRef, g.MirrorPrefix)
				repo, tag := splitImageRef(mirrored)
				images[i].repo = repo
				images[i].tag = tag
				if !g.CheckImage(mirrored) {
					fmt.Printf("    ⚠ %s → %s (WARNING: not available at mirror)\n", fullRef, mirrored)
				} else {
					fmt.Printf("    ✗ %s → %s\n", fullRef, mirrored)
				}
			} else {
				fmt.Printf("    ✓ %s (available)\n", fullRef)
			}
		}
	}
	if len(images) > 0 {
		b.WriteString("images:\n")
		for _, img := range images {
			b.WriteString(fmt.Sprintf("  - name: %s\n", img.original))
			b.WriteString(fmt.Sprintf("    newName: %s\n", img.repo))
			b.WriteString(fmt.Sprintf("    newTag: %q\n", img.tag))
		}
		b.WriteString("\n")
	}

	// Replicas
	for _, dep := range g.Manifests.Deployments {
		replicas, found, _ := unstructured.NestedInt64(dep.Object, "spec", "replicas")
		if !found {
			replicas = 1
		}
		b.WriteString("replicas:\n")
		b.WriteString(fmt.Sprintf("  - name: %s\n", dep.GetName()))
		b.WriteString(fmt.Sprintf("    count: %d\n", replicas))
		b.WriteString("\n")
	}

	// Pull secret — generate with secretGenerator so users provide
	// the actual .dockerconfigjson file at deploy time.
	b.WriteString("# To supply a pull secret for private registries, create a\n")
	b.WriteString("# .dockerconfigjson file and uncomment the secretGenerator below.\n")
	b.WriteString("# This replaces the placeholder pull-secret.yaml from the base.\n")
	b.WriteString("#\n")
	b.WriteString("# secretGenerator:\n")
	b.WriteString(fmt.Sprintf("#   - name: %s\n", pullSecretName))
	b.WriteString("#     type: kubernetes.io/dockerconfigjson\n")
	b.WriteString("#     files:\n")
	b.WriteString("#       - .dockerconfigjson=pull-secret.json\n")
	b.WriteString("#     behavior: replace\n")
	if len(g.PullSecretLabels) > 0 {
		b.WriteString("#     options:\n")
		b.WriteString("#       labels:\n")
		for k, v := range g.PullSecretLabels {
			b.WriteString(fmt.Sprintf("#         %s: %q\n", k, v))
		}
	}
	b.WriteString("\n")

	// Patches reference (only if we have deployments)
	if len(g.Manifests.Deployments) > 0 {
		dep := g.Manifests.Deployments[0]
		b.WriteString("# Uncomment to apply resource/scheduling overrides:\n")
		b.WriteString("# patches:\n")
		b.WriteString(fmt.Sprintf("#   - path: resources.yaml\n"))
		b.WriteString(fmt.Sprintf("#     target:\n"))
		b.WriteString(fmt.Sprintf("#       kind: Deployment\n"))
		b.WriteString(fmt.Sprintf("#       name: %s\n", dep.GetName()))
	}

	return b.String()
}

type imageEntry struct {
	original string
	repo     string
	tag      string
}

func collectAllImages(manifests *bundle.Manifests) []imageEntry {
	seen := make(map[string]bool)
	var images []imageEntry

	for _, dep := range manifests.Deployments {
		containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
		initContainers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "initContainers")
		allContainers := append(containers, initContainers...)

		for _, c := range allContainers {
			cMap, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			image, _ := cMap["image"].(string)
			if image == "" || seen[image] {
				continue
			}
			seen[image] = true
			repo, tag := splitImageRef(image)
			images = append(images, imageEntry{original: image, repo: repo, tag: tag})
		}
	}
	return images
}

func splitImageRef(ref string) (string, string) {
	if idx := strings.LastIndex(ref, "@"); idx >= 0 {
		return ref[:idx], ref[idx+1:]
	}
	if idx := strings.LastIndex(ref, ":"); idx >= 0 {
		afterColon := ref[idx+1:]
		if !strings.Contains(afterColon, "/") {
			return ref[:idx], afterColon
		}
	}
	return ref, "latest"
}

func looksLikeImageRef(s string) bool {
	return strings.Contains(s, "/") && (strings.Contains(s, ":") || strings.Contains(s, "@"))
}

func mirrorEnvImageVars(deployments []*unstructured.Unstructured, mirrorPrefix string, checkImage func(string) bool) {
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
				value, _ := eMap["value"].(string)
				if value == "" || !looksLikeImageRef(value) {
					continue
				}
				if !checkImage(value) {
					mirrored := mirrorImageRef(value, mirrorPrefix)
					eMap["value"] = mirrored
					if !checkImage(mirrored) {
						fmt.Printf("    ⚠ %s → %s (WARNING: not available at mirror)\n", value, mirrored)
					} else {
						fmt.Printf("    ✗ %s → %s\n", value, mirrored)
					}
				}
			}
		}
	}
}

func mirrorImageRef(imageRef, mirrorPrefix string) string {
	tag := ""
	repo := imageRef

	if idx := strings.LastIndex(imageRef, "@"); idx >= 0 {
		repo = imageRef[:idx]
		tag = imageRef[idx:]
	} else if idx := strings.LastIndex(imageRef, ":"); idx >= 0 {
		afterColon := imageRef[idx+1:]
		if !strings.Contains(afterColon, "/") {
			repo = imageRef[:idx]
			tag = imageRef[idx:]
		}
	}

	imageName := repo
	if idx := strings.LastIndex(repo, "/"); idx >= 0 {
		imageName = repo[idx+1:]
	}

	return mirrorPrefix + "/" + imageName + tag
}

func generatePullSecret(name, namespace string, labels map[string]string) string {
	var b strings.Builder
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Secret\n")
	b.WriteString("metadata:\n")
	b.WriteString(fmt.Sprintf("  name: %s\n", name))
	if namespace != "" {
		b.WriteString(fmt.Sprintf("  namespace: %s\n", namespace))
	}
	if len(labels) > 0 {
		b.WriteString("  labels:\n")
		for k, v := range labels {
			b.WriteString(fmt.Sprintf("    %s: %q\n", k, v))
		}
	}
	b.WriteString("type: kubernetes.io/dockerconfigjson\n")
	b.WriteString("data:\n")
	b.WriteString("  .dockerconfigjson: e30=\n")
	return b.String()
}

func injectImagePullSecrets(rbac []*unstructured.Unstructured, pullSecretName string) {
	for _, obj := range rbac {
		if obj.GetKind() != "ServiceAccount" {
			continue
		}
		existing, _, _ := unstructured.NestedSlice(obj.Object, "imagePullSecrets")
		entry := map[string]interface{}{"name": pullSecretName}
		existing = append(existing, entry)
		_ = unstructured.SetNestedSlice(obj.Object, existing, "imagePullSecrets")
	}
}

func generateResourcePatch(g *KustomizeGenerator) string {
	if len(g.Manifests.Deployments) == 0 {
		return ""
	}

	dep := g.Manifests.Deployments[0]
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")

	var b strings.Builder
	b.WriteString("# Strategic merge patch for Deployment resource overrides.\n")
	b.WriteString("# Uncomment the sections you want to customize.\n")
	b.WriteString("#\n")
	b.WriteString("# To activate, also uncomment the 'patches:' block in kustomization.yaml.\n\n")

	b.WriteString("apiVersion: apps/v1\n")
	b.WriteString("kind: Deployment\n")
	b.WriteString("metadata:\n")
	b.WriteString(fmt.Sprintf("  name: %s\n", dep.GetName()))
	b.WriteString("spec:\n")
	b.WriteString("  template:\n")
	b.WriteString("    spec:\n")
	b.WriteString("      containers:\n")

	for _, c := range containers {
		cMap, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := cMap["name"].(string)
		if name == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("        - name: %s\n", name))
		b.WriteString("          # resources:\n")
		b.WriteString("          #   limits:\n")
		b.WriteString("          #     cpu: 500m\n")
		b.WriteString("          #     memory: 256Mi\n")
		b.WriteString("          #   requests:\n")
		b.WriteString("          #     cpu: 10m\n")
		b.WriteString("          #     memory: 64Mi\n")
	}

	b.WriteString("      # nodeSelector:\n")
	b.WriteString("      #   kubernetes.io/os: linux\n")
	b.WriteString("      # tolerations:\n")
	b.WriteString("      #   - key: node-role.kubernetes.io/infra\n")
	b.WriteString("      #     effect: NoSchedule\n")

	return b.String()
}
