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

package cloudflare

import (
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Backoff is a simple exponential backoff with a ceiling.
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

// NewBackoff constructs a Backoff.
func NewBackoff(base, max time.Duration) Backoff {
	return Backoff{Base: base, Max: max}
}

// Next returns the delay for retry attempt n (zero-indexed).
func (b Backoff) Next(attempt int) time.Duration {
	d := b.Base
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= b.Max {
			return b.Max
		}
	}
	return d
}

// RetryAfter inspects an error and (optionally) a response Header set and
// returns the recommended retry delay if the error is a rate-limit and the
// header carries a value. ok=false means "no advice; use backoff".
func RetryAfter(err error, headers http.Header) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return 0, false
	}
	if apiErr.StatusCode != http.StatusTooManyRequests && apiErr.StatusCode != http.StatusServiceUnavailable {
		return 0, false
	}
	if headers == nil {
		return 0, false
	}
	v := headers.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	// Numeric seconds only; HTTP-date form is rare and not needed for our caller.
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}
