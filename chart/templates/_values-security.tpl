{{/*
Build defaultPodOptions structure from flat values
*/}}
{{- define "cloudflare-operator.values.security" -}}
defaultPodOptions:
  automountServiceAccountToken: true
  enableServiceLinks: false
  hostIPC: false
  hostNetwork: false
  hostPID: false
  securityContext:
    runAsNonRoot: true
    seccompProfile:
      type: RuntimeDefault
{{- end -}}
