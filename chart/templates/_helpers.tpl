{{/*
Return the full name for the chart
*/}}
{{- define "cloudflare-operator.fullname" -}}
{{- include "bjw-s.common.lib.chart.names.fullname" . -}}
{{- end -}}

{{/*
Return the chart name
*/}}
{{- define "cloudflare-operator.name" -}}
{{- include "bjw-s.common.lib.chart.names.name" . -}}
{{- end -}}
