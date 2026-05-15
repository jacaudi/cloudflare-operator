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

package envtest_test

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// setupSingleton ensures no leftover CloudflareOperator/cluster CR is present
// at the start of a test. Deletes it if present (tolerating NotFound) and
// polls via waitFor until the apiserver confirms removal. Use at the top of
// any envtest that creates its own singleton.
func setupSingleton(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	key := types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}

	existing := &v1alpha1.CloudflareOperator{}
	if err := sharedClient.Get(ctx, key, existing); err == nil {
		_ = sharedClient.Delete(ctx, existing)
		waitFor(t, 10*time.Second, func() bool {
			err := sharedClient.Get(ctx, key, &v1alpha1.CloudflareOperator{})
			return apierrors.IsNotFound(err)
		})
	}
}
