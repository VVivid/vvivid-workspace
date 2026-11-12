{{- define "vocabbank.labels" -}}
app.kubernetes.io/name: vocabbank
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "vocabbank.security" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}

{{- define "vocabbank.validate" -}}
{{- $p := .Values.provenance -}}
{{- if or (ne $p.channel "production") (ne $p.sourceRef (printf "refs/tags/VocabBank/v%s" $p.version)) (ne $p.chartVersion $p.version) (ne $p.chartVersion .Chart.Version) (ne $p.imageDigest .Values.image.digest) (ne $p.runUrl (printf "https://github.com/VVivid/vvivid-workspace/actions/runs/%.0f" (float64 $p.runId))) -}}
{{- fail "Inconsistent VocabBank release provenance" -}}
{{- end -}}
{{- if and .Values.ingress.enabled (not (and .Values.tls.productionEnabled .Values.tls.trustedCertificateReady)) -}}
{{- fail "Public ingress requires production TLS and observed trusted certificate readiness" -}}
{{- end -}}
{{- end -}}
