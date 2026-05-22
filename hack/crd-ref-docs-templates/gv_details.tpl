{{- define "gvDetails" -}}
{{- $gv := . -}}

## {{ $gv.GroupVersionString }}

{{ $gv.Doc }}

{{- if $gv.Kinds  }}
### Resource Types
{{- range $gv.SortedKinds }}
- {{ $gv.TypeForKind . | markdownRenderTypeLink }}
{{- end }}
{{ end }}

{{/* Render Kinds (CRDs) first — each is a bounded section with a horizontal-rule
     separator above (handled in type.tpl when $type.GVK is set). */ -}}
{{ range $gv.SortedTypes }}
{{- if .GVK -}}
{{ template "type" . }}
{{- end }}
{{ end }}

{{/* Render non-Kind sub-types after all CRDs under a Sub-types section. Each
     stays at H4 (no separator) so the CRD boundaries above stay visually
     clean rather than fragmented by alphabetically-interleaved sub-types. */ -}}
{{- $hasSubTypes := false -}}
{{- range $gv.SortedTypes }}{{- if not .GVK }}{{- $hasSubTypes = true }}{{- end }}{{- end -}}
{{ if $hasSubTypes }}

---

### Sub-types

The types below are referenced by one or more of the CRDs above; they are
never instantiated directly.

{{ range $gv.SortedTypes }}
{{- if not .GVK -}}
{{ template "type" . }}
{{- end }}
{{ end }}
{{ end }}

{{- end -}}
