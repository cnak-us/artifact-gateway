{{/*
Expand the name of the chart.
*/}}
{{- define "artifact-gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name. Truncated at 63 chars (DNS label limit).
*/}}
{{- define "artifact-gateway.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version, as used in the chart label.
*/}}
{{- define "artifact-gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "artifact-gateway.labels" -}}
helm.sh/chart: {{ include "artifact-gateway.chart" . }}
{{ include "artifact-gateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: cnak
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "artifact-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "artifact-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Name of the service account to use.
*/}}
{{- define "artifact-gateway.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "artifact-gateway.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Fully-qualified container image reference. Falls back to Chart.AppVersion when tag is empty.
*/}}
{{- define "artifact-gateway.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end }}

{{/*
Name of the env Secret to mount. Resolves to the user-provided existingSecret
when set, otherwise the chart-rendered Secret.
*/}}
{{- define "artifact-gateway.envSecretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
{{- printf "%s-env" (include "artifact-gateway.fullname" .) -}}
{{- end -}}
{{- end }}

{{/*
Name of the env ConfigMap.
*/}}
{{- define "artifact-gateway.configMapName" -}}
{{- printf "%s-config" (include "artifact-gateway.fullname" .) -}}
{{- end }}

{{/*
Render an env block sourcing DATABASE_URL from .Values.database. Used in the
migrate Job and Deployment so they read the DSN the same way.
*/}}
{{- define "artifact-gateway.databaseEnv" -}}
{{- if .Values.database.existingSecret -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.database.existingSecret | quote }}
      key: {{ .Values.database.existingSecretKey | default "url" | quote }}
{{- else if .Values.database.url -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "artifact-gateway.envSecretName" . | quote }}
      key: DATABASE_URL
{{- end -}}
{{- end }}

{{/*
Fail fast at render time if the chart is missing required inputs.
*/}}
{{- define "artifact-gateway.validate" -}}
{{- if and (not .Values.database.url) (not .Values.database.existingSecret) -}}
{{- fail "artifact-gateway: one of .Values.database.url or .Values.database.existingSecret is required" -}}
{{- end -}}
{{- if and .Values.database.url .Values.database.existingSecret -}}
{{- fail "artifact-gateway: set EITHER .Values.database.url OR .Values.database.existingSecret, not both" -}}
{{- end -}}
{{- if and .Values.secrets.create (not .Values.secrets.kekBase64) -}}
{{- fail "artifact-gateway: .Values.secrets.kekBase64 is required when secrets.create=true. Generate with: openssl rand -base64 32" -}}
{{- end -}}
{{- if and .Values.secrets.create (not .Values.secrets.sessionSigningKey) -}}
{{- fail "artifact-gateway: .Values.secrets.sessionSigningKey is required when secrets.create=true" -}}
{{- end -}}
{{- if and .Values.secrets.create (not .Values.secrets.jwtSigningKey) -}}
{{- fail "artifact-gateway: .Values.secrets.jwtSigningKey is required when secrets.create=true" -}}
{{- end -}}
{{- end -}}
