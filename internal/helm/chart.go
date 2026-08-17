package helm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anandf/kubectl-catalog/internal/bundle"
	"sigs.k8s.io/yaml"
)

// ChartGenerator holds the context needed to generate a Helm chart from
// extracted OLM bundle manifests.
type ChartGenerator struct {
	PackageName  string
	Version      string
	Channel      string
	CatalogRef   string
	Manifests    *bundle.Manifests
	CertProvider string
	InstallMode  string
	Namespace    string
}

// Generate creates a complete Helm chart directory at outputDir.
func (g *ChartGenerator) Generate(outputDir string) error {
	chartName := sanitizeChartName(g.PackageName)

	crdsDir := filepath.Join(outputDir, "crds")
	templatesDir := filepath.Join(outputDir, "templates")

	if err := os.MkdirAll(crdsDir, 0o755); err != nil {
		return fmt.Errorf("creating crds directory: %w", err)
	}
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		return fmt.Errorf("creating templates directory: %w", err)
	}

	// 1. Chart.yaml
	chartData, err := generateChartYAML(g)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "Chart.yaml"), chartData, 0o644); err != nil {
		return fmt.Errorf("writing Chart.yaml: %w", err)
	}

	// 2. values.yaml
	valuesContent := generateValuesYAML(g)
	if err := os.WriteFile(filepath.Join(outputDir, "values.yaml"), []byte(valuesContent), 0o644); err != nil {
		return fmt.Errorf("writing values.yaml: %w", err)
	}

	// 3. CRDs (raw, untemplatized)
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
	}

	// 4. _helpers.tpl
	helpersContent := generateHelpers(chartName)
	if err := os.WriteFile(filepath.Join(templatesDir, "_helpers.tpl"), []byte(helpersContent), 0o644); err != nil {
		return fmt.Errorf("writing _helpers.tpl: %w", err)
	}

	// 5. Template files
	templateFiles, err := generateTemplates(g)
	if err != nil {
		return fmt.Errorf("generating templates: %w", err)
	}
	for _, tf := range templateFiles {
		if err := os.WriteFile(filepath.Join(templatesDir, tf.Name), []byte(tf.Content), 0o644); err != nil {
			return fmt.Errorf("writing template %s: %w", tf.Name, err)
		}
	}

	// 6. NOTES.txt
	notesContent := generateNotes(g)
	if err := os.WriteFile(filepath.Join(templatesDir, "NOTES.txt"), []byte(notesContent), 0o644); err != nil {
		return fmt.Errorf("writing NOTES.txt: %w", err)
	}

	// 7. .helmignore
	helmignoreContent := generateHelmignore()
	if err := os.WriteFile(filepath.Join(outputDir, ".helmignore"), []byte(helmignoreContent), 0o644); err != nil {
		return fmt.Errorf("writing .helmignore: %w", err)
	}

	fmt.Printf("  Helm chart generated at %s\n", outputDir)
	fmt.Printf("    Chart: %s v%s\n", chartName, g.Version)
	fmt.Printf("    CRDs: %d\n", len(g.Manifests.CRDs))
	fmt.Printf("    Templates: %d\n", len(templateFiles))

	return nil
}

func generateHelmignore() string {
	return `# Patterns to ignore when building packages.
.git
.gitignore
.DS_Store
*.swp
*.bak
*.tmp
*.orig
`
}
