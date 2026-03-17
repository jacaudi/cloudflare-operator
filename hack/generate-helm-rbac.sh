#!/usr/bin/env bash
# AUTO-GENERATED RBAC SYNC SCRIPT
# This script converts kubebuilder-generated RBAC (config/rbac/role.yaml)
# to the bjw-s app-template format used by the Helm chart.
#
# Usage: ./hack/generate-helm-rbac.sh
#
# The kubebuilder markers in controller code are the source of truth.
# Do not manually edit chart/templates/_values-rbac.tpl

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

INPUT_FILE="${REPO_ROOT}/config/rbac/role.yaml"
OUTPUT_FILE="${REPO_ROOT}/chart/templates/_values-rbac.tpl"

if [[ ! -f "${INPUT_FILE}" ]]; then
    echo "Error: ${INPUT_FILE} not found. Run 'make manifests' first."
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
HEADER

# Extract and format rules from the kubebuilder-generated YAML
# Use yq to output proper YAML format with correct indentation
yq eval '.rules' "${INPUT_FILE}" | sed 's/^/        /' >> "${OUTPUT_FILE}"

cat >> "${OUTPUT_FILE}" << 'FOOTER'
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
FOOTER

echo "Generated ${OUTPUT_FILE} from ${INPUT_FILE}"
