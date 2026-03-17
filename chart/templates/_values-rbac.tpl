{{/*
RBAC configuration for cloudflare-operator
AUTO-GENERATED from config/rbac/role.yaml - DO NOT EDIT MANUALLY
Run 'make generate-helm-rbac' to regenerate after updating kubebuilder markers.
*/}}
{{- define "cloudflare-operator.values.rbac" -}}
{{- if .Values.rbac.enabled }}
rbac:
  roles:
    main:
      enabled: true
      type: ClusterRole
      rules:
        - apiGroups:
            - ""
          resources:
            - events
          verbs:
            - create
            - patch
        - apiGroups:
            - ""
          resources:
            - secrets
          verbs:
            - create
            - get
            - list
            - patch
            - update
            - watch
        - apiGroups:
            - cloudflare.io
          resources:
            - cloudflarednsrecords
            - cloudflarerulesets
            - cloudflaretunnels
            - cloudflarezoneconfigs
            - cloudflarezones
          verbs:
            - create
            - delete
            - get
            - list
            - patch
            - update
            - watch
        - apiGroups:
            - cloudflare.io
          resources:
            - cloudflarednsrecords/finalizers
            - cloudflarerulesets/finalizers
            - cloudflaretunnels/finalizers
            - cloudflarezoneconfigs/finalizers
            - cloudflarezones/finalizers
          verbs:
            - update
        - apiGroups:
            - cloudflare.io
          resources:
            - cloudflarednsrecords/status
            - cloudflarerulesets/status
            - cloudflaretunnels/status
            - cloudflarezoneconfigs/status
            - cloudflarezones/status
          verbs:
            - get
            - patch
            - update
  bindings:
    main:
      enabled: true
      type: ClusterRoleBinding
      roleRef:
        identifier: main
      subjects:
        - identifier: main
{{- end }}
{{- end -}}
