{{/*
RBAC configuration for cloudflare-operator
AUTO-GENERATED from config/rbac/ - DO NOT EDIT MANUALLY
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
            - configmaps
            - serviceaccounts
          verbs:
            - create
            - delete
            - get
            - list
            - patch
            - update
            - watch
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
            - pods
            - services
          verbs:
            - get
            - list
            - watch
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
            - apps
          resources:
            - deployments
          verbs:
            - create
            - delete
            - get
            - list
            - patch
            - update
            - watch
        - apiGroups:
            - apps
          resources:
            - replicasets
          verbs:
            - get
            - list
            - watch
        - apiGroups:
            - cloudflare.io
          resources:
            - cloudflarednsrecords
            - cloudflarerulesets
            - cloudflaretunnelrules
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
            - cloudflaretunnelrules/status
            - cloudflaretunnels/status
            - cloudflarezoneconfigs/status
            - cloudflarezones/status
          verbs:
            - get
            - patch
            - update
        - apiGroups:
            - gateway.networking.k8s.io
          resources:
            - gateways
            - httproutes
          verbs:
            - get
            - list
            - watch
        - apiGroups:
            - policy
          resources:
            - poddisruptionbudgets
          verbs:
            - create
            - delete
            - get
            - list
            - patch
            - update
            - watch
{{- if .Values.leaderElection.enabled }}
    leader-election:
      enabled: true
      type: Role
      rules:
        - apiGroups:
            - ""
          resources:
            - configmaps
          verbs:
            - get
            - list
            - watch
            - create
            - update
            - patch
            - delete
        - apiGroups:
            - coordination.k8s.io
          resources:
            - leases
          verbs:
            - get
            - list
            - watch
            - create
            - update
            - patch
            - delete
        - apiGroups:
            - ""
          resources:
            - events
          verbs:
            - create
            - patch
{{- end }}
  bindings:
    main:
      enabled: true
      type: ClusterRoleBinding
      roleRef:
        identifier: main
      subjects:
        - identifier: main
{{- if .Values.leaderElection.enabled }}
    leader-election:
      enabled: true
      type: RoleBinding
      roleRef:
        identifier: leader-election
      subjects:
        - identifier: main
{{- end }}
{{- end }}
{{- end -}}
