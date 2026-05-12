package reconcile

import (
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
	r := FailReconcile("X", "boom")
	require.NotNil(t, r)
	require.True(t, r.RequeueAfter > 0)
}
