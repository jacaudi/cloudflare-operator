#!/usr/bin/env bash
# AUTO-GENERATED RBAC SYNC SCRIPT
# This script converts kubebuilder-generated RBAC (config/rbac/role.yaml and
# config/rbac/leader_election_role.yaml) to the bjw-s app-template format
# used by the Helm chart.
#
# Usage: ./hack/generate-helm-rbac.sh
#
# The kubebuilder markers in controller code are the source of truth.
# Do not manually edit chart/templates/_values-rbac.tpl

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

ROLE_FILE="${REPO_ROOT}/config/rbac/role.yaml"
LEADER_ELECTION_FILE="${REPO_ROOT}/config/rbac/leader_election_role.yaml"
OUTPUT_FILE="${REPO_ROOT}/chart/templates/_values-rbac.tpl"

if [[ ! -f "${ROLE_FILE}" ]]; then
    echo "Error: ${ROLE_FILE} not found. Run 'make manifests' first."
    exit 1
fi

# Check for yq
if ! command -v yq &> /dev/null; then
    echo "Error: yq is required but not installed."
    echo "Install with: brew install yq (macOS) or snap install yq (Linux)"
    exit 1
fi

# Generate the template file
cat > "${OUTPUT_FILE}" << 'HEADER'
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
HEADER

# Extract and format rules from the kubebuilder-generated YAML
yq eval '.rules' "${ROLE_FILE}" | sed 's/^/        /' >> "${OUTPUT_FILE}"

# Add leader election Role if the source file exists
if [[ -f "${LEADER_ELECTION_FILE}" ]]; then
    cat >> "${OUTPUT_FILE}" << 'LEADER_ROLE'
{{- if .Values.leaderElection.enabled }}
    leader-election:
      enabled: true
      type: Role
      rules:
LEADER_ROLE

    yq eval '.rules' "${LEADER_ELECTION_FILE}" | sed 's/^/        /' >> "${OUTPUT_FILE}"

    # Close the leader election conditional before bindings
    printf '{{- end }}\n' >> "${OUTPUT_FILE}"
fi

# Add bindings
cat >> "${OUTPUT_FILE}" << 'BINDINGS'
  bindings:
    main:
      enabled: true
      type: ClusterRoleBinding
      roleRef:
        identifier: main
      subjects:
        - identifier: main
BINDINGS

# Add leader election binding if applicable
if [[ -f "${LEADER_ELECTION_FILE}" ]]; then
    cat >> "${OUTPUT_FILE}" << 'LEADER_BINDING'
{{- if .Values.leaderElection.enabled }}
    leader-election:
      enabled: true
      type: RoleBinding
      roleRef:
        identifier: leader-election
      subjects:
        - identifier: main
{{- end }}
LEADER_BINDING
fi

# Close the template
cat >> "${OUTPUT_FILE}" << 'FOOTER'
{{- end }}
{{- end -}}
FOOTER

echo "Generated ${OUTPUT_FILE} from ${ROLE_FILE}"
