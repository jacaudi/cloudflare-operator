{{/*
Build serviceMonitor structure from flat values
*/}}
{{- define "cloudflare-operator.values.servicemonitor" -}}
{{- if .Values.metrics.serviceMonitor.enabled }}
serviceMonitor:
  main:
    enabled: true
    service:
      identifier: main
    endpoints:
      - port: metrics
        scheme: http
        path: /metrics
        interval: {{ .Values.metrics.serviceMonitor.interval }}
        scrapeTimeout: {{ .Values.metrics.serviceMonitor.scrapeTimeout }}
{{- end }}
{{- end -}}
