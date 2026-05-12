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

	"github.com/stretchr/testify/require"
)

func TestClassify_NotFound(t *testing.T) {
	err := &APIError{StatusCode: http.StatusNotFound, Code: 1001, Message: "Tunnel not found"}
	require.Equal(t, KindNotFound, Classify(err))
}

func TestClassify_RateLimited(t *testing.T) {
	err := &APIError{StatusCode: http.StatusTooManyRequests}
	require.Equal(t, KindRateLimited, Classify(err))
}

func TestClassify_InsufficientScope(t *testing.T) {
	err := &APIError{StatusCode: http.StatusForbidden, Code: 10000}
	require.Equal(t, KindInsufficientScope, Classify(err))
}

func TestClassify_GenericError(t *testing.T) {
	require.Equal(t, KindUnknown, Classify(errors.New("network down")))
}
