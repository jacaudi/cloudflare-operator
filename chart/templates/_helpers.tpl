{{- define "cloudflare-operator.labels" -}}
app.kubernetes.io/name: cloudflare-operator
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: Helm
app.kubernetes.io/component: meta-operator
{{- end -}}
