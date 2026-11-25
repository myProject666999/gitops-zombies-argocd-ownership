package detector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

var applicationsGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

func newFakeClient(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			applicationsGVR: "ApplicationList",
		},
		objects...,
	)
}

func TestListApplications(t *testing.T) {
	t.Run("returns an empty list and no error if the applications CRD is not installed", func(t *testing.T) {
		client := newFakeClient()
		client.PrependReactor("list", "applications", func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(applicationsGVR.GroupResource(), "")
		})

		applications, err := listApplications(context.TODO(), client, "")
		require.NoError(t, err)
		require.Empty(t, applications)
	})

	t.Run("returns all applications", func(t *testing.T) {
		app := unstructured.Unstructured{}
		app.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "argoproj.io",
			Version: "v1alpha1",
			Kind:    "Application",
		})
		app.SetName("my-app")
		app.SetNamespace("argocd")

		client := newFakeClient(&app)

		applications, err := listApplications(context.TODO(), client, "")
		require.NoError(t, err)
		require.Len(t, applications, 1)
		require.Equal(t, "my-app", applications[0].GetName())
		require.Equal(t, "argocd", applications[0].GetNamespace())
	})
}
