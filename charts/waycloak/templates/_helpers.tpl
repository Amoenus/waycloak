{{- define "waycloak.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "waycloak.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := include "waycloak.name" . }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "waycloak.labels" -}}
app.kubernetes.io/name: {{ include "waycloak.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end }}

{{- define "waycloak.selectorLabels" -}}
app.kubernetes.io/name: {{ include "waycloak.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end }}

{{- define "waycloak.image" -}}
{{- $repository := required (printf "images.%s.repository is required" .name) .image.repository -}}
{{- $digest := required (printf "images.%s.digest is required" .name) .image.digest -}}
{{- if not (regexMatch "^sha256:[a-f0-9]{64}$" $digest) -}}
{{- fail (printf "images.%s.digest must be an immutable sha256 digest" .name) -}}
{{- end -}}
{{- printf "%s@%s" $repository $digest -}}
{{- end }}

{{- define "waycloak.admissionGeneration" -}}
{{- $controllerImage := include "waycloak.image" (dict "name" "controller" "image" .Values.images.controller) -}}
{{- $agentImage := include "waycloak.image" (dict "name" "agent" "image" .Values.images.agent) -}}
{{- printf "v1:%s:%s" $controllerImage $agentImage | sha256sum -}}
{{- end }}

{{- define "waycloak.admissionGenerationConfigMap" -}}
{{- printf "%s-admission-generation" (include "waycloak.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "waycloak.webhookCertificateName" -}}
{{- printf "%s-webhook" (include "waycloak.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "waycloak.webhookSecretName" -}}
{{- if .Values.webhook.tls.existingSecret -}}
{{- .Values.webhook.tls.existingSecret -}}
{{- else if .Values.webhook.tls.certManager.enabled -}}
{{- printf "%s-tls" (include "waycloak.webhookCertificateName" .) | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- fail "webhook.tls.existingSecret is required when webhook.tls.certManager.enabled is false" -}}
{{- end -}}
{{- end }}

{{- define "waycloak.webhookIssuerName" -}}
{{- if .Values.webhook.tls.certManager.createSelfSignedIssuer -}}
{{- printf "%s-selfsigned" (include "waycloak.webhookCertificateName" .) | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- required "webhook.tls.certManager.issuerRef.name is required when createSelfSignedIssuer is false" .Values.webhook.tls.certManager.issuerRef.name -}}
{{- end -}}
{{- end }}
