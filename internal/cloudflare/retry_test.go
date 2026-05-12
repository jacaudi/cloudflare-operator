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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNextBackoff_Progression(t *testing.T) {
	b := NewBackoff(100*time.Millisecond, 2*time.Second)
	d0 := b.Next(0)
	d1 := b.Next(1)
	d2 := b.Next(2)
	require.Equal(t, 100*time.Millisecond, d0)
	require.Equal(t, 200*time.Millisecond, d1)
	require.Equal(t, 400*time.Millisecond, d2)
}

func TestNextBackoff_CapsAtMax(t *testing.T) {
	b := NewBackoff(100*time.Millisecond, 500*time.Millisecond)
	require.Equal(t, 500*time.Millisecond, b.Next(10))
}

func TestRetryAfter_RespectsHeader(t *testing.T) {
	err := &APIError{StatusCode: http.StatusTooManyRequests}
	d, ok := RetryAfter(err, http.Header{"Retry-After": []string{"7"}})
	require.True(t, ok)
	require.Equal(t, 7*time.Second, d)
}

func TestRetryAfter_NoHeaderForUnrelatedError(t *testing.T) {
	_, ok := RetryAfter(errors.New("x"), nil)
	require.False(t, ok)
}
