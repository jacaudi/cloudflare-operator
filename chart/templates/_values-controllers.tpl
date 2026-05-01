{{/*
Build controllers structure from flat values
*/}}
{{- define "cloudflare-operator.values.controllers" -}}
{{- $replicas := int (.Values.controller.replicas | default 1) -}}
{{- $tsc := .Values.controller.topologySpreadConstraints | default list -}}
{{- $aff := .Values.controller.affinity | default dict -}}
controllers:
  main:
    type: deployment
    replicas: {{ .Values.controller.replicas }}
    strategy: {{ .Values.controller.strategy }}
    {{- if .Values.serviceAccount.create }}
    serviceAccount:
      identifier: main
    {{- end }}
    {{- if and .Values.controller.podDisruptionBudget.enabled (gt $replicas 1) }}
    podDisruptionBudget:
      minAvailable: 1
    {{- end }}
    pod:
      {{- if and (gt $replicas 1) (empty $tsc) (empty $aff) }}
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: {{ include "cloudflare-operator.name" . }}
              app.kubernetes.io/instance: {{ .Release.Name }}
              app.kubernetes.io/controller: main
      {{- else if not (empty $tsc) }}
      topologySpreadConstraints: {{ toYaml $tsc | nindent 8 }}
      {{- end }}
      {{- if not (empty $aff) }}
      affinity: {{ toYaml $aff | nindent 8 }}
      {{- end }}
    containers:
      main:
        image:
          repository: {{ .Values.image.repository }}
          tag: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
          pullPolicy: {{ .Values.image.pullPolicy }}
        args:
          {{- if .Values.leaderElection.enabled }}
          - --leader-elect
          {{- end }}
          - --health-probe-bind-address=:8081
          - --metrics-bind-address=:8080
        env:
          TZ: {{ .Values.timezone }}
          {{- if .Values.registry.txtOwnerID }}
          TXT_OWNER_ID: {{ .Values.registry.txtOwnerID | quote }}
          {{- end }}
          {{- if .Values.registry.txtImportOwners }}
          TXT_IMPORT_OWNERS: {{ .Values.registry.txtImportOwners | join "," | quote }}
          {{- end }}
          {{- if .Values.registry.txtPrefix }}
          TXT_PREFIX: {{ .Values.registry.txtPrefix | quote }}
          {{- end }}
          {{- if .Values.registry.txtSuffix }}
          TXT_SUFFIX: {{ .Values.registry.txtSuffix | quote }}
          {{- end }}
          {{- if .Values.registry.txtWildcardReplacement }}
          TXT_WILDCARD_REPLACEMENT: {{ .Values.registry.txtWildcardReplacement | quote }}
          {{- end }}
        resources:
          {{- toYaml .Values.resources | nindent 10 }}
        securityContext:
          allowPrivilegeEscalation: false
          readOnlyRootFilesystem: true
          capabilities:
            drop:
              - ALL
        probes:
          liveness:
            enabled: true
            custom: true
            spec:
              httpGet:
                path: /healthz
                port: 8081
              initialDelaySeconds: 15
              periodSeconds: 20
          readiness:
            enabled: true
            custom: true
            spec:
              httpGet:
                path: /readyz
                port: 8081
              initialDelaySeconds: 5
              periodSeconds: 10
{{- end -}}
