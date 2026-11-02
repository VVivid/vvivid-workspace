{{- define "newsfeed.labels" -}}
app.kubernetes.io/name: agent-newsfeed
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "newsfeed.security" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}
