package helm

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

type chartYAML struct {
	APIVersion  string            `json:"apiVersion"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type"`
	Version     string            `json:"version"`
	AppVersion  string            `json:"appVersion"`
	KubeVersion string            `json:"kubeVersion,omitempty"`
	Home        string            `json:"home,omitempty"`
	Sources     []string          `json:"sources,omitempty"`
	Keywords    []string          `json:"keywords,omitempty"`
	Maintainers []chartMaintainer `json:"maintainers,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type chartMaintainer struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

func generateChartYAML(g *ChartGenerator) ([]byte, error) {
	chart := chartYAML{
		APIVersion: "v2",
		Name:       sanitizeChartName(g.PackageName),
		Type:       "application",
		Version:    g.Version,
		AppVersion: g.Version,
	}

	annotations := map[string]string{
		"kubectl-catalog.io/catalog-ref": g.CatalogRef,
		"kubectl-catalog.io/channel":     g.Channel,
	}

	if meta := g.Manifests.CSVMetadata; meta != nil {
		if meta.Description != "" {
			chart.Description = meta.Description
		}
		if meta.MinKubeVersion != "" {
			chart.KubeVersion = fmt.Sprintf(">= %s", meta.MinKubeVersion)
		}
		if len(meta.Keywords) > 0 {
			chart.Keywords = meta.Keywords
		}
		for _, m := range meta.Maintainers {
			if m.Name != "" {
				chart.Maintainers = append(chart.Maintainers, chartMaintainer{
					Name:  m.Name,
					Email: m.Email,
				})
			}
		}
		if meta.Provider.URL != "" {
			chart.Home = meta.Provider.URL
		}
		for _, l := range meta.Links {
			if l.URL != "" {
				chart.Sources = append(chart.Sources, l.URL)
			}
		}
		if meta.Maturity != "" {
			annotations["kubectl-catalog.io/maturity"] = meta.Maturity
		}
		if categories, ok := meta.Annotations["categories"]; ok {
			annotations["kubectl-catalog.io/category"] = categories
		}
	}

	chart.Annotations = annotations

	data, err := yaml.Marshal(chart)
	if err != nil {
		return nil, fmt.Errorf("marshaling Chart.yaml: %w", err)
	}
	return data, nil
}

func sanitizeChartName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, ".", "-")
	if len(name) > 63 {
		name = name[:63]
	}
	name = strings.TrimRight(name, "-")
	if name == "" {
		name = "chart"
	}
	return name
}
