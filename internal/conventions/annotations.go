package conventions

import "strings"

// AnnotationPrefix is the reserved prefix for operator-recognized annotations.
// Foundation §7 reserves the namespace; spec 3 populates specific names.
const AnnotationPrefix = "cloudflare.io/"

// IsReservedAnnotation reports whether a key lives under the operator's
// reserved annotation namespace.
func IsReservedAnnotation(key string) bool {
	return strings.HasPrefix(key, AnnotationPrefix)
}

// --- Append-only ---
// Specific annotation name constants land in spec 3's plan (per Foundation §6.1.1
// append-only contract). Foundation only declares the prefix above.
