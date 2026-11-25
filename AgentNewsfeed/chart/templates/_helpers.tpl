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
{{- /*
Returns identity.nativeClientIDs for NEWSFEED_NATIVE_CLIENT_IDS, or "" when
native apps are disabled; ingress.yaml adds the native discovery route only for
a non-empty result. values.schema.json already bounds the list's count,
characters and lengths. This helper applies the app's remaining startup rules,
which the schema cannot express: no ID repeats and none equals the web client
or an MCP client. An invalid list therefore fails rendering instead of replacing
the single running pod with one that cannot start.
*/ -}}
{{- define "newsfeed.nativeClientIDs" -}}
{{- with .Values.identity.nativeClientIDs -}}
{{- /* $taken starts with the web and MCP clients and gains each checked native
ID, so one membership test rejects both overlaps and repeats. */ -}}
{{- $taken := append (splitList "," $.Values.identity.mcpClientIDs) $.Values.identity.webClientID -}}
{{- range $client := splitList "," . -}}
{{- if has $client $taken -}}
{{- fail "identity.nativeClientIDs must be distinct and differ from the web and MCP clients" -}}
{{- end -}}
{{- $taken = append $taken $client -}}
{{- end -}}
{{- . -}}
{{- end -}}
{{- end -}}
