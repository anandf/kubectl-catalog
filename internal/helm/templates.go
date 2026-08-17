package helm

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

type templateFile struct {
	Name    string
	Content string
}

// Pod-spec level template blocks. These are appended after the marshaled
// deployment YAML by the builder. They are independent of each other
// and can be reordered freely.
const (
	envBlock = `env:
- name: WATCH_NAMESPACE
{{- if eq .Values.installMode "AllNamespaces" }}
  value: ""
{{- else }}
  value: {{ .Values.watchNamespace | default .Release.Namespace | quote }}
{{- end }}
{{- range $key, $value := .Values.env }}
- name: {{ $key }}
  value: {{ $value | quote }}
{{- end }}`

	envFromBlock = `{{- with .Values.envFrom }}
envFrom:
  {{- toYaml . | nindent 10 }}
{{- end }}`

	extraVolumeMountsBlock = `{{- with .Values.extraVolumeMounts }}
extraVolumeMounts:
  {{- toYaml . | nindent 10 }}
{{- end }}`

	imagePullSecretsBlock = `      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}`

	priorityClassBlock = `      {{- with .Values.priorityClassName }}
      priorityClassName: {{ . }}
      {{- end }}`

	extraVolumesBlock = `      {{- with .Values.extraVolumes }}
      extraVolumes:
        {{- toYaml . | nindent 8 }}
      {{- end }}`

	schedulingBlocks = `      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}`

	topologySpreadBlock = `      {{- with .Values.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- toYaml . | nindent 8 }}
      {{- end }}`
)

func generateTemplates(g *ChartGenerator) ([]templateFile, error) {
	chartName := sanitizeChartName(g.PackageName)
	images := extractImages(g.Manifests.Deployments)
	namespace := g.Namespace

	var files []templateFile

	if sas := filterByKind(g.Manifests.RBAC, "ServiceAccount"); len(sas) > 0 {
		content := renderServiceAccountTemplate(chartName, sas, namespace)
		files = append(files, templateFile{Name: "serviceaccount.yaml", Content: content})
	}

	clusterRoles := filterByKind(g.Manifests.RBAC, "ClusterRole")
	clusterRoleBindings := filterByKind(g.Manifests.RBAC, "ClusterRoleBinding")
	roles := filterByKind(g.Manifests.RBAC, "Role")
	roleBindings := filterByKind(g.Manifests.RBAC, "RoleBinding")

	if len(clusterRoles) > 0 || len(roles) > 0 {
		content := renderInstallModeAwareRBACTemplate(chartName, clusterRoles, roles, clusterRoleBindings, roleBindings, namespace)
		files = append(files, templateFile{Name: "rbac.yaml", Content: content})
	}

	if len(g.Manifests.Deployments) > 0 {
		content := renderDeploymentTemplate(chartName, g.Manifests.Deployments, images, namespace)
		files = append(files, templateFile{Name: "deployment.yaml", Content: content})
	}

	if len(g.Manifests.Services) > 0 {
		content := renderServiceTemplate(chartName, g.Manifests.Services, namespace)
		files = append(files, templateFile{Name: "service.yaml", Content: content})
	}

	webhooks, monitoring, extras := classifyOther(g.Manifests.Other)

	if len(webhooks) > 0 {
		content := renderWebhookTemplate(chartName, webhooks, namespace)
		files = append(files, templateFile{Name: "webhook-configs.yaml", Content: content})

		certContent := renderCertManagerTemplate(chartName, g.Manifests.Services, namespace)
		files = append(files, templateFile{Name: "cert-manager.yaml", Content: certContent})

		selfSignedContent := renderSelfSignedCertTemplate(chartName, g.Manifests.Services, namespace)
		files = append(files, templateFile{Name: "self-signed-certs.yaml", Content: selfSignedContent})
	}

	if len(monitoring) > 0 {
		content := renderSimpleTemplate(chartName, monitoring, namespace, "monitoring")
		files = append(files, templateFile{Name: "monitoring.yaml", Content: content})
	}

	if len(extras) > 0 {
		for _, tf := range classifyExtras(chartName, extras, namespace) {
			files = append(files, tf)
		}
	}

	return files, nil
}

func filterByKind(resources []*unstructured.Unstructured, kind string) []*unstructured.Unstructured {
	var result []*unstructured.Unstructured
	for _, r := range resources {
		if r.GetKind() == kind {
			result = append(result, r)
		}
	}
	return result
}

func classifyOther(resources []*unstructured.Unstructured) (webhooks, monitoring, extras []*unstructured.Unstructured) {
	for _, obj := range resources {
		kind := obj.GetKind()
		gvk := obj.GroupVersionKind()

		switch {
		case kind == "ValidatingWebhookConfiguration" || kind == "MutatingWebhookConfiguration":
			webhooks = append(webhooks, obj)
		case strings.HasSuffix(gvk.Group, "monitoring.coreos.com") || gvk.Group == "monitoring.coreos.com":
			monitoring = append(monitoring, obj)
		default:
			extras = append(extras, obj)
		}
	}
	return
}

// classifyExtras splits miscellaneous resources into semantically named
// template files instead of dumping everything into extras.yaml.
func classifyExtras(chartName string, resources []*unstructured.Unstructured, namespace string) []templateFile {
	buckets := make(map[string][]*unstructured.Unstructured)

	for _, obj := range resources {
		filename := extrasFilename(obj)
		buckets[filename] = append(buckets[filename], obj)
	}

	var files []templateFile
	for filename, objs := range buckets {
		content := renderSimpleTemplate(chartName, objs, namespace, "")
		files = append(files, templateFile{Name: filename, Content: content})
	}
	return files
}

func extrasFilename(obj *unstructured.Unstructured) string {
	kind := obj.GetKind()
	name := strings.ToLower(obj.GetName())

	switch kind {
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

	case "ServiceMonitor", "PodMonitor", "PrometheusRule":
		return "servicemonitor.yaml"

	case "PriorityClass":
		return "priorityclass.yaml"

	case "NetworkPolicy":
		return "networkpolicy.yaml"

	case "PodDisruptionBudget":
		return "pdb.yaml"

	case "HorizontalPodAutoscaler":
		return "hpa.yaml"

	case "Ingress":
		return "ingress.yaml"

	default:
		return "extras.yaml"
	}
}

// --- Renderers using templateBuilder ---

func renderServiceAccountTemplate(chartName string, sas []*unstructured.Unstructured, namespace string) string {
	var b strings.Builder
	b.WriteString("{{- if .Values.serviceAccount.create }}\n")

	for i, sa := range sas {
		if i > 0 {
			b.WriteString("---\n")
		}
		tb := newTemplateBuilder(chartName, sa.Object, namespace)
		tb.ReplaceNamespace()
		tb.SetValue([]string{"metadata", "name"},
			fmt.Sprintf(`{{ include "%s.serviceAccountName" . }}`, chartName))
		b.WriteString(tb.Build())
	}

	b.WriteString("{{- end }}\n")
	return b.String()
}

func renderRBACResource(chartName string, obj *unstructured.Unstructured, namespace string) string {
	tb := newTemplateBuilder(chartName, obj.Object, namespace)
	tb.ReplaceNamespace()
	tb.ReplaceSubjectSANames()
	return tb.Build()
}

func renderInstallModeAwareRBACTemplate(chartName string, clusterRoles, roles, clusterRoleBindings, roleBindings []*unstructured.Unstructured, namespace string) string {
	var b strings.Builder
	b.WriteString("{{- if .Values.rbac.create }}\n")

	// AllNamespaces: emit ClusterRole + ClusterRoleBinding
	b.WriteString(`{{- if eq .Values.installMode "AllNamespaces" }}`)
	b.WriteString("\n")
	for i, obj := range clusterRoles {
		if i > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(renderRBACResource(chartName, obj, namespace))
	}
	for _, obj := range clusterRoleBindings {
		b.WriteString("---\n")
		b.WriteString(renderRBACResource(chartName, obj, namespace))
	}

	// SingleNamespace/OwnNamespace: downgrade to Role/RoleBinding
	b.WriteString("{{- else }}\n")
	written := 0
	for _, obj := range clusterRoles {
		if written > 0 {
			b.WriteString("---\n")
		}
		downgraded := obj.DeepCopy()
		downgraded.SetKind("Role")
		downgraded.Object["apiVersion"] = "rbac.authorization.k8s.io/v1"
		tb := newTemplateBuilder(chartName, downgraded.Object, namespace)
		tb.ReplaceNamespace()
		if !nestedFieldExists(tb.obj, []string{"metadata", "namespace"}) {
			tb.SetValue([]string{"metadata", "namespace"}, "{{ .Release.Namespace }}")
		}
		b.WriteString(tb.Build())
		written++
	}
	for _, obj := range clusterRoleBindings {
		if written > 0 {
			b.WriteString("---\n")
		}
		downgraded := obj.DeepCopy()
		downgraded.SetKind("RoleBinding")
		downgraded.Object["apiVersion"] = "rbac.authorization.k8s.io/v1"
		if roleRef, ok := downgraded.Object["roleRef"].(map[string]interface{}); ok {
			roleRef["kind"] = "Role"
		}
		tb := newTemplateBuilder(chartName, downgraded.Object, namespace)
		tb.ReplaceNamespace()
		tb.ReplaceSubjectSANames()
		if !nestedFieldExists(tb.obj, []string{"metadata", "namespace"}) {
			tb.SetValue([]string{"metadata", "namespace"}, "{{ .Release.Namespace }}")
		}
		b.WriteString(tb.Build())
		written++
	}
	b.WriteString("{{- end }}\n")

	// Bundle-native Roles/RoleBindings are always emitted
	for _, obj := range roles {
		b.WriteString("---\n")
		b.WriteString(renderRBACResource(chartName, obj, namespace))
	}
	for _, obj := range roleBindings {
		b.WriteString("---\n")
		b.WriteString(renderRBACResource(chartName, obj, namespace))
	}

	b.WriteString("{{- end }}\n")
	return b.String()
}

func renderDeploymentTemplate(chartName string, deployments []*unstructured.Unstructured, images []imageMapping, namespace string) string {
	var b strings.Builder

	for i, dep := range deployments {
		if i > 0 {
			b.WriteString("---\n")
		}
		tb := newTemplateBuilder(chartName, dep.Object, namespace)

		// Metadata
		tb.ReplaceNamespace()

		// Spec level
		tb.SetValue([]string{"spec", "replicas"}, "{{ .Values.replicaCount }}")
		tb.SetValue([]string{"spec", "revisionHistoryLimit"}, "{{ .Values.revisionHistoryLimit }}")
		tb.WrapConditional([]string{"spec", "strategy"}, ".Values.strategy")

		// Pod spec
		tb.SetValue([]string{"spec", "template", "spec", "serviceAccountName"},
			fmt.Sprintf(`{{ include "%s.serviceAccountName" . }}`, chartName))
		tb.WrapConditional([]string{"spec", "template", "spec", "securityContext"}, ".Values.podSecurityContext")
		tb.RemoveField([]string{"spec", "template", "spec", "priorityClassName"})
		tb.RemoveField([]string{"spec", "template", "spec", "nodeSelector"})
		tb.RemoveField([]string{"spec", "template", "spec", "tolerations"})
		tb.RemoveField([]string{"spec", "template", "spec", "affinity"})
		tb.RemoveField([]string{"spec", "template", "spec", "topologySpreadConstraints"})
		tb.RemoveField([]string{"spec", "template", "spec", "imagePullSecrets"})

		// Replace image-referencing env vars (RELATED_IMAGE_*, etc.) with
		// template expressions so they can be overridden via values.yaml
		tb.TemplateImageEnvVars()

		// Strip hardcoded WATCH_NAMESPACE — the Helm template uses a dynamic
		// expression driven by .Values.installMode instead
		tb.StripHardcodedWatchNamespace()

		// Per-container field wrapping
		for ci := range tb.GetContainers() {
			tb.ReplaceContainerImage(ci, images)
			tb.WrapContainerField(ci, "resources", ".Values.resources")
			tb.WrapContainerField(ci, "livenessProbe", ".Values.livenessProbe")
			tb.WrapContainerField(ci, "readinessProbe", ".Values.readinessProbe")
			tb.WrapContainerField(ci, "args", ".Values.args")
			tb.WrapContainerField(ci, "securityContext", ".Values.securityContext")
			tb.InjectContainerBlock(ci, "env", envBlock)
			tb.InjectContainerBlock(ci, "envfrom", envFromBlock)
			tb.InjectContainerBlock(ci, "extramounts", extraVolumeMountsBlock)
		}

		// TLS secret references
		tb.ReplaceTLSSecretRefs()

		// Pod-spec appends (order-independent)
		tb.AppendToPodSpec(imagePullSecretsBlock)
		tb.AppendToPodSpec(priorityClassBlock)
		tb.AppendToPodSpec(extraVolumesBlock)
		tb.AppendToPodSpec(schedulingBlocks)
		tb.AppendToPodSpec(topologySpreadBlock)

		b.WriteString(tb.Build())
	}

	return b.String()
}

func renderServiceTemplate(chartName string, services []*unstructured.Unstructured, namespace string) string {
	var b strings.Builder

	for i, svc := range services {
		if i > 0 {
			b.WriteString("---\n")
		}
		tb := newTemplateBuilder(chartName, svc.Object, namespace)
		tb.ReplaceNamespace()
		tb.SetValue([]string{"spec", "type"}, `{{ .Values.service.type | default "ClusterIP" }}`)
		b.WriteString(tb.Build())
	}

	return b.String()
}

func renderWebhookTemplate(chartName string, webhooks []*unstructured.Unstructured, namespace string) string {
	var b strings.Builder
	b.WriteString("{{- if .Values.webhooks.enabled }}\n")

	for i, wh := range webhooks {
		if i > 0 {
			b.WriteString("---\n")
		}
		tb := newTemplateBuilder(chartName, wh.Object, namespace)
		tb.ReplaceNamespace()

		// Strip hardcoded caBundle and inject cert-manager annotation
		stripCABundleFromObj(tb.obj)
		injectCertManagerAnnotationToObj(tb.obj, chartName)

		out := tb.Build()
		// Inject caBundle conditional block after each clientConfig: line
		out = injectCABundleBlock(out, chartName)
		b.WriteString(out)
	}

	b.WriteString("{{- end }}\n")
	return b.String()
}

func renderSimpleTemplate(chartName string, resources []*unstructured.Unstructured, namespace, conditionalField string) string {
	var b strings.Builder

	if conditionalField != "" {
		fmt.Fprintf(&b, "{{- if .Values.%s.enabled }}\n", conditionalField)
	}

	for i, obj := range resources {
		if i > 0 {
			b.WriteString("---\n")
		}
		tb := newTemplateBuilder(chartName, obj.Object, namespace)
		tb.ReplaceNamespace()
		b.WriteString(tb.Build())
	}

	if conditionalField != "" {
		b.WriteString("{{- end }}\n")
	}

	return b.String()
}

// --- Webhook helpers (operate on Go objects, not strings) ---

func stripCABundleFromObj(obj map[string]interface{}) {
	webhooks, ok := obj["webhooks"].([]interface{})
	if !ok {
		return
	}
	for _, wh := range webhooks {
		whMap, ok := wh.(map[string]interface{})
		if !ok {
			continue
		}
		cc, ok := whMap["clientConfig"].(map[string]interface{})
		if !ok {
			continue
		}
		delete(cc, "caBundle")
	}
}

func injectCertManagerAnnotationToObj(obj map[string]interface{}, chartName string) {
	meta, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		return
	}
	ann, ok := meta["annotations"].(map[string]interface{})
	if !ok {
		ann = make(map[string]interface{})
		meta["annotations"] = ann
	}
	// Use a marker that will be replaced after marshaling
	ann["cert-manager.io/inject-ca-from"] = fmt.Sprintf(
		"CERTMGR_INJECT_%s", chartName)
}

func injectCABundleBlock(content, chartName string) string {
	caBundleBlock := fmt.Sprintf(`    {{- if .Values.certManager.enabled }}
    caBundle: ""
    {{- else }}
    {{- $secretName := printf "%%s-webhook-server-cert" (include "%s.fullname" .) }}
    {{- $secret := lookup "v1" "Secret" .Release.Namespace $secretName }}
    {{- if $secret }}
    caBundle: {{ index $secret.data "ca.crt" }}
    {{- end }}
    {{- end }}`, chartName)

	// Replace the cert-manager annotation marker with the real conditional
	certMgrMarker := fmt.Sprintf("CERTMGR_INJECT_%s", chartName)
	certMgrAnnotation := fmt.Sprintf(`{{- if .Values.certManager.enabled }}
    cert-manager.io/inject-ca-from: {{ .Release.Namespace }}/{{ include "%s.fullname" . }}-serving-cert
    {{- end }}`, chartName)
	content = strings.ReplaceAll(content,
		fmt.Sprintf("cert-manager.io/inject-ca-from: %s", certMgrMarker),
		certMgrAnnotation)
	// Also handle quoted form
	content = strings.ReplaceAll(content,
		fmt.Sprintf(`cert-manager.io/inject-ca-from: "%s"`, certMgrMarker),
		certMgrAnnotation)

	// Inject caBundle into each clientConfig block
	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		result = append(result, line)
		if strings.TrimSpace(line) == "clientConfig:" {
			result = append(result, caBundleBlock)
		}
	}
	return strings.Join(result, "\n")
}

// --- Cert-manager and self-signed cert templates (built from scratch, not from objects) ---

func renderCertManagerTemplate(chartName string, services []*unstructured.Unstructured, namespace string) string {
	var b strings.Builder
	b.WriteString("{{- if and .Values.certManager.enabled .Values.webhooks.enabled }}\n")

	b.WriteString("apiVersion: cert-manager.io/v1\n")
	b.WriteString("kind: {{ .Values.certManager.issuer.kind | default \"Issuer\" }}\n")
	b.WriteString("metadata:\n")
	fmt.Fprintf(&b, "  name: {{ include \"%s.fullname\" . }}-selfsigned-issuer\n", chartName)
	b.WriteString("  namespace: {{ .Release.Namespace }}\n")
	b.WriteString("  labels:\n")
	fmt.Fprintf(&b, "    {{- include \"%s.labels\" . | nindent 4 }}\n", chartName)
	b.WriteString("spec:\n")
	b.WriteString("  selfSigned: {}\n")
	b.WriteString("---\n")

	svcName := "webhook-service"
	if len(services) > 0 {
		svcName = services[0].GetName()
	}

	b.WriteString("apiVersion: cert-manager.io/v1\n")
	b.WriteString("kind: Certificate\n")
	b.WriteString("metadata:\n")
	fmt.Fprintf(&b, "  name: {{ include \"%s.fullname\" . }}-serving-cert\n", chartName)
	b.WriteString("  namespace: {{ .Release.Namespace }}\n")
	b.WriteString("  labels:\n")
	fmt.Fprintf(&b, "    {{- include \"%s.labels\" . | nindent 4 }}\n", chartName)
	b.WriteString("spec:\n")
	fmt.Fprintf(&b, "  secretName: {{ include \"%s.fullname\" . }}-webhook-server-cert\n", chartName)
	b.WriteString("  issuerRef:\n")
	fmt.Fprintf(&b, "    name: {{ default (printf \"%%s-selfsigned-issuer\" (include \"%s.fullname\" .)) .Values.certManager.issuer.name }}\n", chartName)
	b.WriteString("    kind: {{ .Values.certManager.issuer.kind | default \"Issuer\" }}\n")
	b.WriteString("  duration: {{ .Values.certManager.duration | default \"8760h\" }}\n")
	b.WriteString("  renewBefore: {{ .Values.certManager.renewBefore | default \"720h\" }}\n")
	b.WriteString("  dnsNames:\n")
	fmt.Fprintf(&b, "    - %s\n", svcName)
	fmt.Fprintf(&b, "    - %s.{{ .Release.Namespace }}\n", svcName)
	fmt.Fprintf(&b, "    - %s.{{ .Release.Namespace }}.svc\n", svcName)
	fmt.Fprintf(&b, "    - %s.{{ .Release.Namespace }}.svc.cluster.local\n", svcName)
	b.WriteString("  usages:\n")
	b.WriteString("    - server auth\n")

	b.WriteString("{{- end }}\n")
	return b.String()
}

func renderSelfSignedCertTemplate(chartName string, services []*unstructured.Unstructured, namespace string) string {
	svcName := "webhook-service"
	if len(services) > 0 {
		svcName = services[0].GetName()
	}

	var b strings.Builder
	b.WriteString("{{- if and (not .Values.certManager.enabled) .Values.webhooks.enabled }}\n")
	fmt.Fprintf(&b, "{{- $secretName := printf \"%%s-webhook-server-cert\" (include \"%s.fullname\" .) }}\n", chartName)
	b.WriteString("{{- $existingSecret := lookup \"v1\" \"Secret\" .Release.Namespace $secretName }}\n")
	b.WriteString("{{- if $existingSecret }}\n")
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Secret\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: {{ $secretName }}\n")
	b.WriteString("  namespace: {{ .Release.Namespace }}\n")
	b.WriteString("  labels:\n")
	fmt.Fprintf(&b, "    {{- include \"%s.labels\" . | nindent 4 }}\n", chartName)
	b.WriteString("type: kubernetes.io/tls\n")
	b.WriteString("data:\n")
	b.WriteString("  tls.crt: {{ index $existingSecret.data \"tls.crt\" }}\n")
	b.WriteString("  tls.key: {{ index $existingSecret.data \"tls.key\" }}\n")
	b.WriteString("  ca.crt: {{ index $existingSecret.data \"ca.crt\" }}\n")
	b.WriteString("{{- else }}\n")
	fmt.Fprintf(&b, "{{- $cn := printf \"%s.%%s.svc\" .Release.Namespace }}\n", svcName)
	fmt.Fprintf(&b, "{{- $ca := genCA \"%s-ca\" 3650 }}\n", chartName)
	fmt.Fprintf(&b, "{{- $cert := genSignedCert $cn nil (list \"%s\" (printf \"%s.%%s\" .Release.Namespace) (printf \"%s.%%s.svc\" .Release.Namespace) (printf \"%s.%%s.svc.cluster.local\" .Release.Namespace)) 3650 $ca }}\n",
		svcName, svcName, svcName, svcName)
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Secret\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: {{ $secretName }}\n")
	b.WriteString("  namespace: {{ .Release.Namespace }}\n")
	b.WriteString("  labels:\n")
	fmt.Fprintf(&b, "    {{- include \"%s.labels\" . | nindent 4 }}\n", chartName)
	b.WriteString("type: kubernetes.io/tls\n")
	b.WriteString("data:\n")
	b.WriteString("  tls.crt: {{ $cert.Cert | b64enc }}\n")
	b.WriteString("  tls.key: {{ $cert.Key | b64enc }}\n")
	b.WriteString("  ca.crt: {{ $ca.Cert | b64enc }}\n")
	b.WriteString("{{- end }}\n")
	b.WriteString("{{- end }}\n")
	return b.String()
}

// --- Security: escape template delimiters from bundle content ---

func escapeTemplateDelimiters(content string) string {
	var b strings.Builder
	b.Grow(len(content))
	for i := 0; i < len(content); i++ {
		if i+1 < len(content) {
			pair := content[i : i+2]
			if pair == "{{" {
				b.WriteString("{{`{{`}}")
				i++
				continue
			}
			if pair == "}}" {
				b.WriteString("{{`}}`}}")
				i++
				continue
			}
		}
		b.WriteByte(content[i])
	}
	return b.String()
}

// --- Helpers for marshaling (used by renderSimpleTemplate and tests) ---

func marshalResource(obj *unstructured.Unstructured) string {
	data, err := yaml.Marshal(obj.Object)
	if err != nil {
		return fmt.Sprintf("# Error marshaling %s/%s: %v\n", obj.GetKind(), obj.GetName(), err)
	}
	return escapeTemplateDelimiters(string(data))
}
