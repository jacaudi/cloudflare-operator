//go:build tools

package tools

// This file pins dependencies that are used by the project but not yet
// directly imported in source code. The "tools" build constraint ensures
// this file is never compiled into any binary.
import (
	_ "github.com/cloudflare/cloudflare-go/v6"
)
