//go:build tools
// +build tools

/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

// Pattern: this file is built only under the "tools" build tag (declared
// above), so its blank imports keep tool-only modules (controller-gen,
// setup-envtest) in go.mod / go.sum without pulling them into the
// production binary.
//
// To regenerate tool versions: edit the import line, run `go mod tidy`,
// then invoke the tool via `go run <module-path>/<tool-cmd>`.
//
// See https://golang.org/issue/25922 for the upstream discussion.

package tools

import (
	_ "sigs.k8s.io/controller-runtime/tools/setup-envtest"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
