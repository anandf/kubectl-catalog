package helm

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

type replaceStyle int

const (
	valueReplace replaceStyle = iota
	conditionalWrap
	rawBlockReplace
)

type markerReplacement struct {
	marker    string
	tplExpr   string
	fieldName string
	style     replaceStyle
}

type templateBuilder struct {
	chartName    string
	obj          map[string]interface{}
	namespace    string
	replacements []markerReplacement
	podAppends   []string
	specAppends  []string
	nextID       int
}

func newTemplateBuilder(chartName string, obj map[string]interface{}, namespace string) *templateBuilder {
	cp := deepCopyMap(obj)
	return &templateBuilder{
		chartName: chartName,
		obj:       cp,
		namespace: namespace,
	}
}

func (b *templateBuilder) nextMarker() string {
	m := fmt.Sprintf("HELMTPL_%d_END", b.nextID)
	b.nextID++
	return m
}

// SetValue replaces a field's value with a Helm template expression.
// No-op if the path does not exist.
func (b *templateBuilder) SetValue(path []string, expr string) {
	if !nestedFieldExists(b.obj, path) {
		return
	}
	marker := b.nextMarker()
	setNestedField(b.obj, path, marker)
	b.replacements = append(b.replacements, markerReplacement{
		marker:  marker,
		tplExpr: expr,
		style:   valueReplace,
	})
}

// WrapConditional removes a field's sub-tree and replaces it with a
// {{- with .Values.X }} block after marshaling.
func (b *templateBuilder) WrapConditional(path []string, valuesPath string) {
	if !nestedFieldExists(b.obj, path) {
		return
	}
	marker := b.nextMarker()
	fieldName := path[len(path)-1]
	setNestedField(b.obj, path, marker)
	b.replacements = append(b.replacements, markerReplacement{
		marker:    marker,
		tplExpr:   valuesPath,
		fieldName: fieldName,
		style:     conditionalWrap,
	})
}

// RemoveField deletes a field from the object. No-op if missing.
func (b *templateBuilder) RemoveField(path []string) {
	removeNestedField(b.obj, path)
}

// ReplaceNamespace replaces all occurrences of the hardcoded namespace with
// {{ .Release.Namespace }} by setting the namespace field to a marker.
func (b *templateBuilder) ReplaceNamespace() {
	if b.namespace == "" {
		return
	}
	// metadata.namespace
	b.SetValue([]string{"metadata", "namespace"}, "{{ .Release.Namespace }}")
	// Also handle nested namespace references in subjects, service refs, etc.
	replaceNamespaceRecursive(b.obj, b.namespace, b.nextMarker, &b.replacements, &b.nextID)
}

// InjectLabels records that labels should be injected after marshaling.
// This is handled specially in Build() because it appends to the labels dict.
func (b *templateBuilder) InjectLabels() {
	// Handled in Build() via post-processing — the only string operation we keep
}

// InjectPodAnnotations records that pod annotations should be injected.
func (b *templateBuilder) InjectPodAnnotations() {
	// Handled in Build()
}

// GetContainers returns the containers slice for iteration.
func (b *templateBuilder) GetContainers() []map[string]interface{} {
	containers, ok := nestedSlice(b.obj, "spec", "template", "spec", "containers")
	if !ok {
		return nil
	}
	var result []map[string]interface{}
	for _, c := range containers {
		if cm, ok := c.(map[string]interface{}); ok {
			result = append(result, cm)
		}
	}
	return result
}

// ReplaceContainerImage replaces the image field in the given container.
func (b *templateBuilder) ReplaceContainerImage(containerIdx int, images []imageMapping) {
	containers := b.GetContainers()
	if containerIdx >= len(containers) {
		return
	}
	c := containers[containerIdx]
	image, _ := c["image"].(string)
	if image == "" {
		return
	}
	for _, img := range images {
		if img.Original == image {
			marker := b.nextMarker()
			c["image"] = marker
			b.replacements = append(b.replacements, markerReplacement{
				marker:  marker,
				tplExpr: fmt.Sprintf(`{{ include "%s.image" .Values.image.%s }}`, b.chartName, img.Key),
				style:   valueReplace,
			})
			break
		}
	}
}

// WrapContainerField wraps a container-level field with a conditional.
func (b *templateBuilder) WrapContainerField(containerIdx int, field, valuesPath string) {
	containers := b.GetContainers()
	if containerIdx >= len(containers) {
		return
	}
	c := containers[containerIdx]
	if _, exists := c[field]; !exists {
		return
	}
	marker := b.nextMarker()
	c[field] = marker
	b.replacements = append(b.replacements, markerReplacement{
		marker:    marker,
		tplExpr:   valuesPath,
		fieldName: field,
		style:     conditionalWrap,
	})
}

// TemplateImageEnvVars replaces env var values that are container image references
// with Helm template expressions referencing .Values.operatorImageEnv.<canonicalKey>.
// Both RELATED_IMAGE_X and X map to the same .Values.operatorImageEnv.X key.
func (b *templateBuilder) TemplateImageEnvVars() {
	for _, c := range b.GetContainers() {
		env, ok := c["env"].([]interface{})
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
			if looksLikeImageRef(value) {
				key := canonicalEnvImageName(name)
				marker := b.nextMarker()
				eMap["value"] = marker
				b.replacements = append(b.replacements, markerReplacement{
					marker:  marker,
					tplExpr: fmt.Sprintf("{{ .Values.operatorImageEnv.%s }}", key),
					style:   valueReplace,
				})
			}
		}
	}
}

// TemplateEnvVarOverrides replaces non-image env var values with Helm template
// expressions that check .Values.env for an override, falling back to the
// bundle default. Returns a map from container index to the list of env var
// names that were templated, so the envBlock range can skip them.
func (b *templateBuilder) TemplateEnvVarOverrides() map[int][]string {
	result := make(map[int][]string)
	for ci, c := range b.GetContainers() {
		env, ok := c["env"].([]interface{})
		if !ok {
			continue
		}
		for _, e := range env {
			eMap, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := eMap["name"].(string)
			if name == "" {
				continue
			}

			value, _ := eMap["value"].(string)
			if strings.HasPrefix(value, "HELMTPL_") {
				continue
			}
			if _, hasValueFrom := eMap["valueFrom"]; hasValueFrom {
				continue
			}

			marker := b.nextMarker()
			eMap["value"] = marker

			tplExpr := fmt.Sprintf(
				`{{ if hasKey .Values.env %q }}{{ index .Values.env %q | quote }}{{ else }}%s{{ end }}`,
				name, name, fmt.Sprintf("%q", value),
			)

			b.replacements = append(b.replacements, markerReplacement{
				marker:  marker,
				tplExpr: tplExpr,
				style:   valueReplace,
			})

			result[ci] = append(result[ci], name)
		}
	}
	return result
}

// StripHardcodedWatchNamespace removes any WATCH_NAMESPACE env var that was
// baked into the deployment by applyInstallMode. The Helm template uses a
// dynamic expression driven by .Values.installMode instead.
func (b *templateBuilder) StripHardcodedWatchNamespace() {
	for _, c := range b.GetContainers() {
		env, ok := c["env"].([]interface{})
		if !ok {
			continue
		}
		var filtered []interface{}
		for _, e := range env {
			eMap, ok := e.(map[string]interface{})
			if !ok {
				filtered = append(filtered, e)
				continue
			}
			if eMap["name"] == "WATCH_NAMESPACE" {
				continue
			}
			filtered = append(filtered, e)
		}
		if len(filtered) == 0 {
			delete(c, "env")
		} else {
			c["env"] = filtered
		}
	}
}

// InjectContainerBlock sets a marker in a container that will be replaced
// with a raw template block after marshaling. The field name is prefixed with
// "zzz" to sort last in the YAML output.
func (b *templateBuilder) InjectContainerBlock(containerIdx int, fieldSuffix, block string) {
	containers := b.GetContainers()
	if containerIdx >= len(containers) {
		return
	}
	c := containers[containerIdx]
	marker := b.nextMarker()
	field := "zzz_" + fieldSuffix
	c[field] = marker
	b.replacements = append(b.replacements, markerReplacement{
		marker:    marker,
		tplExpr:   block,
		fieldName: field,
		style:     rawBlockReplace,
	})
}

// RemoveContainerField deletes a field from the given container. No-op if missing.
func (b *templateBuilder) RemoveContainerField(containerIdx int, field string) {
	containers := b.GetContainers()
	if containerIdx >= len(containers) {
		return
	}
	delete(containers[containerIdx], field)
}

// AppendToContainerEnv adds a sentinel entry to the container's env list.
// After marshaling the sentinel line is replaced with the supplied template
// block, keeping all entries in a single env: list.
func (b *templateBuilder) AppendToContainerEnv(containerIdx int, block string) {
	containers := b.GetContainers()
	if containerIdx >= len(containers) {
		return
	}
	c := containers[containerIdx]

	env, _ := c["env"].([]interface{})
	if env == nil {
		env = []interface{}{}
	}

	marker := b.nextMarker()
	env = append(env, map[string]interface{}{"name": marker})
	c["env"] = env

	b.replacements = append(b.replacements, markerReplacement{
		marker:    marker,
		tplExpr:   block,
		fieldName: "env_append",
		style:     rawBlockReplace,
	})
}

// AppendToContainerVolumeMounts adds a sentinel to the container's
// volumeMounts list. After marshaling the sentinel is replaced with the
// supplied template block, appending entries to the existing list.
func (b *templateBuilder) AppendToContainerVolumeMounts(containerIdx int, block string) {
	containers := b.GetContainers()
	if containerIdx >= len(containers) {
		return
	}
	c := containers[containerIdx]

	mounts, _ := c["volumeMounts"].([]interface{})
	if mounts == nil {
		mounts = []interface{}{}
	}

	marker := b.nextMarker()
	mounts = append(mounts, map[string]interface{}{"mountPath": marker})
	c["volumeMounts"] = mounts

	b.replacements = append(b.replacements, markerReplacement{
		marker:    marker,
		tplExpr:   block,
		fieldName: "volumemounts_append",
		style:     rawBlockReplace,
	})
}

// AppendToPodVolumes adds a sentinel to the pod-spec volumes list.
// After marshaling the sentinel is replaced with the supplied template
// block, appending entries to the existing list.
func (b *templateBuilder) AppendToPodVolumes(block string) {
	path := []string{"spec", "template", "spec", "volumes"}
	volumes, ok := nestedSlice(b.obj, path...)
	if !ok {
		volumes = []interface{}{}
		setNestedField(b.obj, path, volumes)
	}

	marker := b.nextMarker()
	volumes = append(volumes, map[string]interface{}{"name": marker})
	setNestedField(b.obj, path, volumes)

	b.replacements = append(b.replacements, markerReplacement{
		marker:    marker,
		tplExpr:   block,
		fieldName: "volumes_append",
		style:     rawBlockReplace,
	})
}

// ConditionalizeSecretVolumes removes secret-backed volumes whose secret
// name contains "cert", "tls", or "webhook" (and their matching container
// volumeMounts), replacing them with blocks conditional on
// .Values.webhooks.enabled.
func (b *templateBuilder) ConditionalizeSecretVolumes() {
	volumes, ok := nestedSlice(b.obj, "spec", "template", "spec", "volumes")
	if !ok {
		return
	}

	templateSecretName := fmt.Sprintf(`{{ include "%s.fullname" . }}-webhook-server-cert`, b.chartName)

	var certVolumeNames []string
	var remaining []interface{}
	var volumeBlockParts []string

	for _, v := range volumes {
		vol, ok := v.(map[string]interface{})
		if !ok {
			remaining = append(remaining, v)
			continue
		}
		secret, ok := vol["secret"].(map[string]interface{})
		if !ok {
			remaining = append(remaining, v)
			continue
		}
		secretName, _ := secret["secretName"].(string)
		if secretName == "" {
			remaining = append(remaining, v)
			continue
		}
		lower := strings.ToLower(secretName)
		if !(strings.Contains(lower, "cert") || strings.Contains(lower, "tls") || strings.Contains(lower, "webhook")) {
			remaining = append(remaining, v)
			continue
		}

		volName, _ := vol["name"].(string)
		certVolumeNames = append(certVolumeNames, volName)

		var vb strings.Builder
		fmt.Fprintf(&vb, "- name: %s\n", volName)
		vb.WriteString("  secret:\n")
		fmt.Fprintf(&vb, "    secretName: %s", templateSecretName)
		if dm, ok := secret["defaultMode"]; ok {
			fmt.Fprintf(&vb, "\n    defaultMode: %v", dm)
		}
		volumeBlockParts = append(volumeBlockParts, vb.String())
	}

	if len(certVolumeNames) == 0 {
		return
	}

	// Add sentinel to the volumes list
	marker := b.nextMarker()
	remaining = append(remaining, map[string]interface{}{"name": marker})
	setNestedField(b.obj, []string{"spec", "template", "spec", "volumes"}, remaining)

	var volBlock strings.Builder
	volBlock.WriteString("{{- if .Values.webhooks.enabled }}\n")
	for i, part := range volumeBlockParts {
		if i > 0 {
			volBlock.WriteString("\n")
		}
		volBlock.WriteString(part)
	}
	volBlock.WriteString("\n{{- end }}")

	b.replacements = append(b.replacements, markerReplacement{
		marker:    marker,
		tplExpr:   volBlock.String(),
		fieldName: "volumes_cert",
		style:     rawBlockReplace,
	})

	// Remove matching volumeMounts from containers and add conditional sentinels
	for _, c := range b.GetContainers() {
		mounts, ok := c["volumeMounts"].([]interface{})
		if !ok {
			continue
		}
		var remainingMounts []interface{}
		var mountBlockParts []string
		for _, m := range mounts {
			mount, ok := m.(map[string]interface{})
			if !ok {
				remainingMounts = append(remainingMounts, m)
				continue
			}
			name, _ := mount["name"].(string)
			isCert := slices.Contains(certVolumeNames, name)
			if !isCert {
				remainingMounts = append(remainingMounts, m)
				continue
			}

			var mb strings.Builder
			mountPath, _ := mount["mountPath"].(string)
			fmt.Fprintf(&mb, "- mountPath: %s\n", mountPath)
			fmt.Fprintf(&mb, "  name: %s", name)
			if readOnly, ok := mount["readOnly"].(bool); ok && readOnly {
				mb.WriteString("\n  readOnly: true")
			}
			mountBlockParts = append(mountBlockParts, mb.String())
		}

		if len(mountBlockParts) == 0 {
			continue
		}

		mountMarker := b.nextMarker()
		remainingMounts = append(remainingMounts, map[string]interface{}{"mountPath": mountMarker})
		c["volumeMounts"] = remainingMounts

		var mountBlock strings.Builder
		mountBlock.WriteString("{{- if .Values.webhooks.enabled }}\n")
		for i, part := range mountBlockParts {
			if i > 0 {
				mountBlock.WriteString("\n")
			}
			mountBlock.WriteString(part)
		}
		mountBlock.WriteString("\n{{- end }}")

		b.replacements = append(b.replacements, markerReplacement{
			marker:    mountMarker,
			tplExpr:   mountBlock.String(),
			fieldName: "volumemounts_cert",
			style:     rawBlockReplace,
		})
	}
}

// ReplaceTLSSecretRefs replaces TLS-related secret names in volumes.
func (b *templateBuilder) ReplaceTLSSecretRefs() {
	volumes, ok := nestedSlice(b.obj, "spec", "template", "spec", "volumes")
	if !ok {
		return
	}
	templateSecretName := fmt.Sprintf(`{{ include "%s.fullname" . }}-webhook-server-cert`, b.chartName)
	for _, v := range volumes {
		vol, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		secret, ok := vol["secret"].(map[string]interface{})
		if !ok {
			continue
		}
		secretName, _ := secret["secretName"].(string)
		if secretName == "" {
			continue
		}
		lower := strings.ToLower(secretName)
		if strings.Contains(lower, "cert") || strings.Contains(lower, "tls") || strings.Contains(lower, "webhook") {
			marker := b.nextMarker()
			secret["secretName"] = marker
			b.replacements = append(b.replacements, markerReplacement{
				marker:  marker,
				tplExpr: templateSecretName,
				style:   valueReplace,
			})
		}
	}
}

// ReplaceSubjectSANames replaces ServiceAccount names in binding subjects.
func (b *templateBuilder) ReplaceSubjectSANames() {
	subjects, ok := nestedSlice(b.obj, "subjects")
	if !ok {
		return
	}
	saExpr := fmt.Sprintf(`{{ include "%s.serviceAccountName" . }}`, b.chartName)
	nsExpr := "{{ .Release.Namespace }}"
	for _, s := range subjects {
		sMap, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		kind, _ := sMap["kind"].(string)
		if kind != "ServiceAccount" {
			continue
		}
		name, _ := sMap["name"].(string)
		if name != "" {
			marker := b.nextMarker()
			sMap["name"] = marker
			b.replacements = append(b.replacements, markerReplacement{
				marker: marker, tplExpr: saExpr, style: valueReplace,
			})
		}
		marker := b.nextMarker()
		sMap["namespace"] = marker
		b.replacements = append(b.replacements, markerReplacement{
			marker: marker, tplExpr: nsExpr, style: valueReplace,
		})
	}
}

// AppendToPodSpec adds a template block at the spec.template.spec level.
func (b *templateBuilder) AppendToPodSpec(block string) {
	b.podAppends = append(b.podAppends, block)
}

// AppendToSpec adds a template block at the spec level.
func (b *templateBuilder) AppendToSpec(block string) {
	b.specAppends = append(b.specAppends, block)
}

// Build marshals the object and applies all registered transformations.
func (b *templateBuilder) Build() string {
	data, err := yaml.Marshal(b.obj)
	if err != nil {
		return fmt.Sprintf("# Error marshaling: %v\n", err)
	}
	content := escapeTemplateDelimiters(string(data))

	// Convert compact block sequences to indented style so the output
	// matches Helm chart conventions (list items indented 2 under the key).
	content = reindentBlockSequences(content)

	// Replace markers
	for _, r := range b.replacements {
		switch r.style {
		case valueReplace:
			// Handle both quoted and unquoted marker forms
			content = strings.ReplaceAll(content, fmt.Sprintf(`"%s"`, r.marker), r.tplExpr)
			content = strings.ReplaceAll(content, r.marker, r.tplExpr)
		case conditionalWrap:
			content = replaceConditionalMarker(content, r.marker, r.fieldName, r.tplExpr)
		case rawBlockReplace:
			content = replaceRawBlockMarker(content, r.marker, r.fieldName, r.tplExpr)
		}
	}

	// Inject labels (the one string operation we keep — appends to existing dict)
	content = injectLabelsLine(content, b.chartName)

	// Inject pod annotations block
	content = injectPodAnnotationsBlock(content)

	// Append pod-spec blocks
	if len(b.podAppends) > 0 {
		block := strings.Join(b.podAppends, "\n")
		content = strings.TrimRight(content, "\n") + "\n" + block + "\n"
	}

	return content
}

// --- internal helpers ---

func replaceConditionalMarker(content, marker, fieldName, valuesPath string) string {
	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		if !strings.Contains(line, marker) {
			result = append(result, line)
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		pad := strings.Repeat(" ", indent)
		result = append(result,
			fmt.Sprintf("%s{{- with %s }}", pad, valuesPath),
			fmt.Sprintf("%s%s:", pad, fieldName),
			fmt.Sprintf("%s  {{- toYaml . | nindent %d }}", pad, indent+2),
			fmt.Sprintf("%s{{- end }}", pad),
		)
	}
	return strings.Join(result, "\n")
}

func replaceRawBlockMarker(content, marker, fieldName, block string) string {
	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		if strings.Contains(line, marker) {
			indent := len(line) - len(strings.TrimLeft(line, " "))
			blockLines := strings.Split(block, "\n")

			// Find minimum indentation across YAML content lines so
			// relative indentation (e.g. "value:" under a list item)
			// is preserved when re-indenting to the marker position.
			minIndent := -1
			for _, bl := range blockLines {
				trimmed := strings.TrimSpace(bl)
				if trimmed == "" || strings.HasPrefix(trimmed, "{{") {
					continue
				}
				blIndent := len(bl) - len(strings.TrimLeft(bl, " "))
				if minIndent < 0 || blIndent < minIndent {
					minIndent = blIndent
				}
			}
			if minIndent < 0 {
				minIndent = 0
			}

			for _, bl := range blockLines {
				if strings.TrimSpace(bl) == "" {
					continue
				}
				blIndent := len(bl) - len(strings.TrimLeft(bl, " "))
				rel := blIndent - minIndent
				if rel < 0 {
					rel = 0
				}
				result = append(result, strings.Repeat(" ", indent+rel)+strings.TrimLeft(bl, " "))
			}
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func injectLabelsLine(content, chartName string) string {
	lines := strings.Split(content, "\n")
	var result []string
	injected := false
	inMetadata := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))

		if indent == 0 && trimmed == "metadata:" {
			inMetadata = true
		} else if indent == 0 && trimmed != "" && !strings.HasPrefix(trimmed, "{{") {
			inMetadata = false
		}

		if !injected && inMetadata && indent == 2 && trimmed == "labels:" {
			labelLine := fmt.Sprintf("    {{- include \"%s.labels\" . | nindent 4 }}", chartName)
			result = append(result, line)
			result = append(result, labelLine)
			injected = true
			continue
		}

		if !injected && inMetadata && indent == 2 && strings.HasPrefix(trimmed, "name:") {
			labelLine := fmt.Sprintf("    {{- include \"%s.labels\" . | nindent 4 }}", chartName)
			result = append(result, "  labels:")
			result = append(result, labelLine)
			injected = true
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

func injectPodAnnotationsBlock(content string) string {
	mergeInto := "        {{- with .Values.podAnnotations }}\n" +
		"        {{- toYaml . | nindent 8 }}\n" +
		"        {{- end }}"

	newBlock := "      {{- with .Values.podAnnotations }}\n" +
		"      annotations:\n" +
		"        {{- toYaml . | nindent 8 }}\n" +
		"      {{- end }}"

	lines := strings.Split(content, "\n")
	var result []string
	inPodTemplate := false
	inPodMeta := false
	injected := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))

		if indent == 2 && trimmed == "template:" {
			inPodTemplate = true
		}

		if inPodTemplate && indent == 4 && trimmed == "metadata:" {
			inPodMeta = true
			result = append(result, line)
			continue
		}

		if inPodMeta && !injected && indent <= 4 && trimmed != "" {
			result = append(result, newBlock)
			injected = true
			inPodMeta = false
		}

		if inPodMeta && !injected && indent == 6 && trimmed == "annotations:" {
			result = append(result, line)
			result = append(result, mergeInto)
			injected = true
			continue
		}

		result = append(result, line)
	}

	if !injected {
		if strings.Contains(content, "    spec:") {
			return strings.Replace(content, "    spec:", "    metadata:\n"+newBlock+"\n    spec:", 1)
		}
	}

	return strings.Join(result, "\n")
}

func replaceNamespaceRecursive(obj map[string]interface{}, namespace string, nextMarker func() string, replacements *[]markerReplacement, nextID *int) {
	// Walk subjects and other nested namespace references
	if subjects, ok := obj["subjects"].([]interface{}); ok {
		for _, s := range subjects {
			if sMap, ok := s.(map[string]interface{}); ok {
				if ns, _ := sMap["namespace"].(string); ns == namespace {
					marker := fmt.Sprintf("HELMTPL_%d_END", *nextID)
					*nextID++
					sMap["namespace"] = marker
					*replacements = append(*replacements, markerReplacement{
						marker: marker, tplExpr: "{{ .Release.Namespace }}", style: valueReplace,
					})
				}
			}
		}
	}
	// Walk webhooks[].clientConfig.service.namespace
	if webhooks, ok := obj["webhooks"].([]interface{}); ok {
		for _, wh := range webhooks {
			if whMap, ok := wh.(map[string]interface{}); ok {
				if cc, ok := whMap["clientConfig"].(map[string]interface{}); ok {
					if svc, ok := cc["service"].(map[string]interface{}); ok {
						if ns, _ := svc["namespace"].(string); ns == namespace {
							marker := fmt.Sprintf("HELMTPL_%d_END", *nextID)
							*nextID++
							svc["namespace"] = marker
							*replacements = append(*replacements, markerReplacement{
								marker: marker, tplExpr: "{{ .Release.Namespace }}", style: valueReplace,
							})
						}
					}
				}
			}
		}
	}
}

// deepCopyMap creates a deep copy of a map[string]interface{}.
func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	data, _ := json.Marshal(m)
	var cp map[string]interface{}
	_ = json.Unmarshal(data, &cp)
	return cp
}

func nestedFieldExists(obj map[string]interface{}, path []string) bool {
	current := obj
	for i, key := range path {
		val, ok := current[key]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		next, ok := val.(map[string]interface{})
		if !ok {
			return false
		}
		current = next
	}
	return false
}

func setNestedField(obj map[string]interface{}, path []string, value interface{}) {
	current := obj
	for i, key := range path {
		if i == len(path)-1 {
			current[key] = value
			return
		}
		next, ok := current[key].(map[string]interface{})
		if !ok {
			return
		}
		current = next
	}
}

func removeNestedField(obj map[string]interface{}, path []string) {
	current := obj
	for i, key := range path {
		if i == len(path)-1 {
			delete(current, key)
			return
		}
		next, ok := current[key].(map[string]interface{})
		if !ok {
			return
		}
		current = next
	}
}

// reindentBlockSequences converts compact YAML block sequences (where list
// items are at the same indent as the parent key) to indented style (items
// indented +2 under the key), matching Helm chart conventions.
func reindentBlockSequences(content string) string {
	lines := strings.Split(content, "\n")
	n := len(lines)
	extraIndent := make([]int, n)

	for i := 0; i < n; i++ {
		trimmed := strings.TrimLeft(lines[i], " ")
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}

		itemIndent := len(lines[i]) - len(trimmed)

		isCompact := false
		for j := i - 1; j >= 0; j-- {
			ptrimmed := strings.TrimSpace(lines[j])
			if ptrimmed == "" || strings.HasPrefix(ptrimmed, "{{") || strings.HasPrefix(ptrimmed, "#") {
				continue
			}
			pIndent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
			effectiveKeyIndent := pIndent
			if strings.HasPrefix(strings.TrimLeft(lines[j], " "), "- ") {
				effectiveKeyIndent = pIndent + 2
			}
			if effectiveKeyIndent == itemIndent && strings.HasSuffix(ptrimmed, ":") {
				isCompact = true
			}
			break
		}

		if !isCompact {
			continue
		}

		extraIndent[i] += 2

		for j := i + 1; j < n; j++ {
			jtrimmed := strings.TrimSpace(lines[j])
			if jtrimmed == "" {
				continue
			}
			if strings.HasPrefix(jtrimmed, "{{") || strings.HasPrefix(jtrimmed, "#") {
				jIndent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
				if jIndent >= itemIndent {
					extraIndent[j] += 2
				} else {
					break
				}
				continue
			}
			jIndent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
			if jIndent < itemIndent {
				break
			}
			if jIndent == itemIndent && !strings.HasPrefix(jtrimmed, "- ") {
				break
			}
			extraIndent[j] += 2
		}
	}

	var result []string
	for i, line := range lines {
		if extraIndent[i] > 0 && strings.TrimSpace(line) != "" {
			result = append(result, strings.Repeat(" ", extraIndent[i])+line)
		} else {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

func nestedSlice(obj map[string]interface{}, path ...string) ([]interface{}, bool) {
	current := obj
	for i, key := range path {
		val, ok := current[key]
		if !ok {
			return nil, false
		}
		if i == len(path)-1 {
			slice, ok := val.([]interface{})
			return slice, ok
		}
		next, ok := val.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current = next
	}
	return nil, false
}
