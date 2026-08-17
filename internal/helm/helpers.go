package helm

import "fmt"

func generateHelpers(chartName string) string {
	return fmt.Sprintf(`{{/*
Expand the name of the chart.
*/}}
{{- define "%[1]s.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "%[1]s.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%%s-%%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "%[1]s.chart" -}}
{{- printf "%%s-%%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "%[1]s.labels" -}}
helm.sh/chart: {{ include "%[1]s.chart" . }}
{{ include "%[1]s.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "%[1]s.selectorLabels" -}}
app.kubernetes.io/name: {{ include "%[1]s.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "%[1]s.serviceAccountName" -}}
{{- if .Values.serviceAccount.name }}
{{- .Values.serviceAccount.name }}
{{- else }}
{{- include "%[1]s.fullname" . }}
{{- end }}
{{- end }}

{{/*
Build an image reference from an image config dict.
Supports both tag (:v1.0) and digest (@sha256:...) references.
Usage: {{ include "%[1]s.image" .Values.image.manager }}
*/}}
{{- define "%[1]s.image" -}}
{{- $sep := ":" }}
{{- if hasPrefix "sha256:" .tag }}
{{- $sep = "@" }}
{{- end }}
{{- if .registry }}
{{- printf "%%s/%%s%%s%%s" .registry .repository $sep .tag }}
{{- else }}
{{- printf "%%s%%s%%s" .repository $sep .tag }}
{{- end }}
{{- end }}

{{/*
Common annotations
*/}}
{{- define "%[1]s.commonAnnotations" -}}
{{- with .Values.commonAnnotations }}
{{ toYaml . }}
{{- end }}
{{- end }}
`, chartName)
}
