package helm

import (
	"fmt"
	"strings"
)

func generateNotes(g *ChartGenerator) string {
	var b strings.Builder
	b.WriteString("{{ .Chart.Name }} has been installed.\n\n")
	b.WriteString("Release:   {{ .Release.Name }}\n")
	b.WriteString("Namespace: {{ .Release.Namespace }}\n")
	b.WriteString("Version:   {{ .Chart.AppVersion }}\n\n")

	if len(g.Manifests.CRDs) > 0 {
		b.WriteString("CRDs installed:\n")
		for _, crd := range g.Manifests.CRDs {
			fmt.Fprintf(&b, "  - %s\n", crd.GetName())
		}
		b.WriteString("\n")
		b.WriteString("NOTE: CRDs are placed in the crds/ directory and are installed on\n")
		b.WriteString("first 'helm install' but NOT updated on 'helm upgrade'.\n")
		b.WriteString("To update CRDs manually:\n")
		b.WriteString("  kubectl apply -f <chart-dir>/crds/\n\n")
	}

	b.WriteString("Check operator status:\n")
	b.WriteString("  kubectl get deployment -l app.kubernetes.io/instance={{ .Release.Name }} -n {{ .Release.Namespace }}\n\n")

	b.WriteString("View operator logs:\n")
	b.WriteString("  kubectl logs -l app.kubernetes.io/instance={{ .Release.Name }} -n {{ .Release.Namespace }} -f\n")

	if meta := g.Manifests.CSVMetadata; meta != nil && len(meta.Links) > 0 {
		b.WriteString("\nUseful links:\n")
		for _, l := range meta.Links {
			if l.URL != "" {
				label := l.Name
				if label == "" {
					label = "Link"
				}
				fmt.Fprintf(&b, "  %s: %s\n", label, l.URL)
			}
		}
	}

	return b.String()
}
