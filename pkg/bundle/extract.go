package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

const (
	// Standard OLM bundle directories
	manifestsDir = "manifests"
	metadataDir  = "metadata"
)

// Manifests holds the extracted and transformed Kubernetes resources from a bundle.
type Manifests struct {
	CRDs        []*unstructured.Unstructured
	Deployments []*unstructured.Unstructured
	RBAC        []*unstructured.Unstructured
	Services    []*unstructured.Unstructured
	Other       []*unstructured.Unstructured
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
			extracted, err := extractFromCSV(obj)
			if err != nil {
				return fmt.Errorf("extracting from CSV in %s: %w", path, err)
			}
			manifests.Deployments = append(manifests.Deployments, extracted.Deployments...)
			manifests.RBAC = append(manifests.RBAC, extracted.RBAC...)
			manifests.Services = append(manifests.Services, extracted.Services...)
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
