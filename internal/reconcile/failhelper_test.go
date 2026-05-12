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

package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestWrapDeleteErr_NotFoundReturnsNil(t *testing.T) {
	gvr := schema.GroupResource{Group: "", Resource: "configmaps"}
	err := apierrors.NewNotFound(gvr, "x")
	require.NoError(t, WrapDeleteErr(err))
}

func TestWrapDeleteErr_OtherErrorPasses(t *testing.T) {
	err := errors.New("boom")
	require.Error(t, WrapDeleteErr(err))
}

func TestFailReconcile_DefaultDelay(t *testing.T) {
	r := FailReconcile(context.Background(), "X", "boom")
	require.NotNil(t, r)
	require.True(t, r.RequeueAfter > 0)
}
