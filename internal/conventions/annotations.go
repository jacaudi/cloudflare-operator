/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
