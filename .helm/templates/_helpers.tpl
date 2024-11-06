{{/*
Expand the name of the chart.
*/}}
{{- define "app.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "app.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := .Chart.Name }}
{{- if contains .Release.Name $name }}
{{- $name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "app.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "app.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "app.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Generage image reference based on image repository and tag
*/}}
{{- define "app.image" -}}
{{- printf "%s:%s" .Values.image.repository  (default (printf "%s" .Chart.AppVersion) .Values.image.tag) }}
{{- end }}

{{/*
Generage common labels
*/}}
{{- define "app.labels" -}}
helm.sh/chart: {{ include "app.chart" . }}
{{ include "app.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.additionalLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Generate the secret editor cluster role name
*/}}
{{- define "app.clusteRole.secretEditor" -}}
{{- if .Values.rbac.clusterRole.secretEditor.nameOverride }}
{{- .Values.rbac.clusterRole.secretEditor.nameOverride }}
{{- else }}
{{- printf "%s-secret-editor" (include "app.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Generate the configmap editor cluster role name
*/}}
{{- define "app.clusteRole.configMapEditor" -}}
{{- if .Values.rbac.clusterRole.configMapEditor.nameOverride }}
{{- .Values.rbac.clusterRole.configMapEditor.nameOverride }}
{{- else }}
{{- printf "%s-configmap-editor" (include "app.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Generate the MLA editor cluster role name
*/}}
{{- define "app.clusteRole.mlaEditor" -}}
{{- if .Values.rbac.clusterRole.mlaEditor.nameOverride }}
{{- .Values.rbac.clusterRole.mlaEditor.nameOverride }}
{{- else }}
{{- printf "%s-mla-editor" (include "app.fullname" .) }}
{{- end }}
{{- end }}