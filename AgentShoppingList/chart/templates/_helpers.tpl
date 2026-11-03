{{- define "shopping.labels" -}}
app.kubernetes.io/name: agent-shopping-list
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "shopping.security" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}
