{{- define "memory.name" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "memory.labels" -}}
app.kubernetes.io/name: artificial-memory
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
