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

// #7 structured error-class helper (combined design §4.5).
// Replaces log-grep-only error signaling with a typed class string usable in
// Warning Event reasons, status condition reasons, and (future) metrics. S1
// already emits Warning Events with ReasonOwnershipCompanionFailed; the
// classes here let consumers route on the underlying CAUSE
// (name-miss / foreign / undecodable / cf-api-<code>) rather than the
// generic catch-all reason.

package reconcile

import (
	"errors"
	"regexp"
)

// Sentinel errors for the named classes. Consumers in zone/ + tunnel/ wrap
// these via fmt.Errorf("%w: ...", ErrForeign) so errors.Is(err, ErrForeign)
// returns true.
var (
	// ErrNameMiss indicates the expected record was not found by name+type
	// lookup — Cloudflare ListRecords returned zero matches even though the
	// operator's state suggests one should exist. S1's companion-self-heal
	// classifies a "companion expected but list returned empty" condition as
	// this class.
	ErrNameMiss = errors.New("name-miss")
	// ErrForeign indicates a record exists at the expected name+type but is
	// owned by something other than this CR — the ownership companion
	// decoded successfully but the embedded reference doesn't match this
	// CR's identity.
	ErrForeign = errors.New("foreign")
	// ErrUndecodable indicates a record exists at the expected name+type
	// with an ownership companion that fails to decode (corrupted payload
	// or wrong codec). The operator refuses to write/adopt — manual cleanup
	// or AES key rotation is required.
	ErrUndecodable = errors.New("undecodable")
)

// cfAPICodeRe extracts the numeric Cloudflare API error code from a
// CF-SDK-wrapped error message. Pattern matches "code 81058", "code: 81058",
// or "code=81058" anywhere in the error string.
var cfAPICodeRe = regexp.MustCompile(`(?i)\bcode[:= ]\s*(\d{3,5})\b`)

// ErrorClass returns a stable string identifying the class of an error,
// for use in Warning Event reasons / status condition reasons / metric
// labels. Returns "" for nil. Order of precedence:
//
//  1. errors.Is(err, any of ErrNameMiss / ErrForeign / ErrUndecodable) →
//     that sentinel's class name.
//  2. Message matches the Cloudflare-API-code regex → "cf-api-<code>".
//  3. Otherwise → "unknown".
//
// Substring-only matches on plain error messages are NOT classified —
// callers MUST wrap with %w (errors.Is path) to get the named classes.
// This protects against false positives where an error message happens
// to contain a class name as a substring.
func ErrorClass(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrNameMiss):
		return "name-miss"
	case errors.Is(err, ErrForeign):
		return "foreign"
	case errors.Is(err, ErrUndecodable):
		return "undecodable"
	}
	if m := cfAPICodeRe.FindStringSubmatch(err.Error()); len(m) == 2 {
		return "cf-api-" + m[1]
	}
	return "unknown"
}
