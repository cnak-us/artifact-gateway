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
Name of the rendered Dex config Secret. Only used when externalDatabase.enabled
and dex.enabled — we render config.yaml ourselves so Dex's storage block points
at the same Postgres instance as the gateway.
*/}}
{{- define "artifact-gateway.dexConfigSecretName" -}}
{{- printf "%s-dex-config" (include "artifact-gateway.fullname" .) -}}
{{- end }}

{{/*
Build the Postgres DSN from .Values.externalDatabase. Used by databaseEnv and
documented as the source of truth when externalDatabase is enabled.
*/}}
{{- define "artifact-gateway.externalDatabaseURL" -}}
{{- $db := .Values.externalDatabase -}}
{{- printf "postgresql://%s:%s@%s:%v/%s?sslmode=%s" $db.user $db.password $db.host (toString $db.port) $db.databases.app $db.sslMode -}}
{{- end }}

{{/*
Render an env block sourcing DATABASE_URL for the gateway.
Order of precedence:
  1. .Values.database.existingSecret  (BYO Secret + key)
  2. .Values.database.url             (rendered into envSecretName)
  3. .Values.externalDatabase.enabled (built DSN, rendered into envSecretName)
*/}}
{{- define "artifact-gateway.databaseEnv" -}}
{{- if .Values.database.existingSecret -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.database.existingSecret | quote }}
      key: {{ .Values.database.existingSecretKey | default "url" | quote }}
{{- else if or .Values.database.url .Values.externalDatabase.enabled -}}
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
{{- $sources := list -}}
{{- if .Values.database.url -}}{{- $sources = append $sources "database.url" -}}{{- end -}}
{{- if .Values.database.existingSecret -}}{{- $sources = append $sources "database.existingSecret" -}}{{- end -}}
{{- if .Values.externalDatabase.enabled -}}{{- $sources = append $sources "externalDatabase.enabled" -}}{{- end -}}
{{- if eq (len $sources) 0 -}}
{{- fail "artifact-gateway: set exactly one of .Values.database.url, .Values.database.existingSecret, or .Values.externalDatabase.enabled" -}}
{{- end -}}
{{- if gt (len $sources) 1 -}}
{{- fail (printf "artifact-gateway: set EXACTLY ONE database source — got: %s" (join ", " $sources)) -}}
{{- end -}}
{{- if .Values.externalDatabase.enabled -}}
{{- if not .Values.externalDatabase.host -}}{{- fail "artifact-gateway: .Values.externalDatabase.host is required when externalDatabase.enabled" -}}{{- end -}}
{{- if not .Values.externalDatabase.user -}}{{- fail "artifact-gateway: .Values.externalDatabase.user is required when externalDatabase.enabled" -}}{{- end -}}
{{- if not .Values.externalDatabase.password -}}{{- fail "artifact-gateway: .Values.externalDatabase.password is required when externalDatabase.enabled" -}}{{- end -}}
{{- if not .Values.externalDatabase.databases.app -}}{{- fail "artifact-gateway: .Values.externalDatabase.databases.app is required when externalDatabase.enabled" -}}{{- end -}}
{{- if and .Values.dex.enabled (not .Values.externalDatabase.databases.dex) -}}{{- fail "artifact-gateway: .Values.externalDatabase.databases.dex is required when externalDatabase.enabled and dex.enabled" -}}{{- end -}}
{{- end -}}
{{- end -}}
