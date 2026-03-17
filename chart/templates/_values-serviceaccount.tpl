{{/*
Build serviceAccount structure from flat values
*/}}
{{- define "cloudflare-operator.values.serviceaccount" -}}
serviceAccount:
  main:
    enabled: {{ .Values.serviceAccount.create }}
    {{- if .Values.serviceAccount.name }}
    name: {{ .Values.serviceAccount.name }}
    {{- end }}
    {{- with .Values.serviceAccount.annotations }}
    annotations:
      {{- toYaml . | nindent 6 }}
    {{- end }}
{{- end -}}
