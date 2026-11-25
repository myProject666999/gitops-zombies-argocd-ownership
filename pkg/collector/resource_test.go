package collector

import (
	"testing"

	helmapi "github.com/fluxcd/helm-controller/api/v2"
	ksapi "github.com/fluxcd/kustomize-controller/api/v1"
	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	gitopszombiesv1 "github.com/raffis/gitops-zombies/pkg/apis/gitopszombies/v1"
)

type NullLogger struct{}

func (l NullLogger) Debugf(_ string, _ ...any) {
}

func (l NullLogger) Failuref(_ string, _ ...any) {
}

type test struct {
	name               string
	filters            func() []FilterFunc
	list               func() *unstructured.UnstructuredList
	expectedPass       int
	expectedDualClaims int
}

func argocdApplication(name, namespace string, resources []map[string]any) unstructured.Unstructured {
	app := unstructured.Unstructured{}
	app.SetName(name)
	app.SetNamespace(namespace)
	app.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "argoproj.io",
		Version: "v1alpha1",
		Kind:    "Application",
	})

	if resources != nil {
		items := make([]any, 0, len(resources))
		for _, res := range resources {
			items = append(items, res)
		}
		_ = unstructured.SetNestedSlice(app.Object, items, "status", "resources")
	}

	return app
}

func getExclusionListResourceSet() *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{}

	res1 := unstructured.Unstructured{}
	res1.SetName("velero-capi-backup-1")
	res1.SetNamespace("velero")
	res1.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "velero.io",
		Version: "v1",
		Kind:    "Backup",
	})
	res1.SetAnnotations(map[string]string{"test-annotation": "velero-capi-backup-1"})

	res2 := unstructured.Unstructured{}
	res2.SetName("velero-capi-backup-2")
	res2.SetNamespace("velero")
	res2.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "velero.io",
		Version: "v2",
		Kind:    "Backup",
	})
	res2.SetLabels(map[string]string{"test-label": "velero-capi-backup-2"})

	res3 := unstructured.Unstructured{}
	res3.SetName("velero-capi-backup-3")
	res3.SetNamespace("velero2")
	res3.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "velero.io",
		Version: "v1",
		Kind:    "Backuped",
	})

	list.Items = append(list.Items, res1, res2, res3)

	return list
}

func TestDiscovery(t *testing.T) {
	tests := []test{
		{
			name: "A resource which has owner references is skipped",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreOwnedResource()}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("resource-without-owner")

				notExpected := unstructured.Unstructured{}
				notExpected.SetName("resource-with-owner")
				notExpected.SetOwnerReferences([]v1.OwnerReference{
					{
						Name: "owner",
					},
				})

				list.Items = append(list.Items, expected, notExpected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A secret which belongs to a service account is ignored",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreServiceAccountSecret()}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("secret")
				expected.SetAPIVersion("v1")
				expected.SetKind("Secret")

				notExpected := unstructured.Unstructured{}
				notExpected.SetName("service-account-secret")
				notExpected.SetAPIVersion("v1")
				notExpected.SetKind("Secret")
				notExpected.SetAnnotations(map[string]string{
					"kubernetes.io/service-account.name": "sa",
				})

				list.Items = append(list.Items, expected, notExpected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A secret which is labeled as a helm owner is ignored",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreHelmSecret()}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("secret")
				expected.SetAPIVersion("v1")
				expected.SetKind("Secret")

				notExpected := unstructured.Unstructured{}
				notExpected.SetName("service-account-secret")
				notExpected.SetAPIVersion("v1")
				notExpected.SetKind("Secret")
				notExpected.SetLabels(map[string]string{
					"owner": "helm",
				})

				list.Items = append(list.Items, expected, notExpected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A resource which is part of a helmrelease is ignored",
			filters: func() []FilterFunc {
				helmReleases := []helmapi.HelmRelease{}
				hr := helmapi.HelmRelease{}
				hr.SetName("release")
				hr.SetNamespace("test")

				helmReleases = append(helmReleases, hr)

				return []FilterFunc{IgnoreIfHelmReleaseFound(helmReleases)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("resource")

				alsoExpected := unstructured.Unstructured{}
				alsoExpected.SetName("service-account-secret")
				alsoExpected.SetLabels(map[string]string{
					fluxHelmNameLabel:      "release",
					fluxHelmNamespaceLabel: "not-existing",
				})

				notExpected := unstructured.Unstructured{}
				notExpected.SetName("service-account-secret")
				notExpected.SetLabels(map[string]string{
					fluxHelmNameLabel:      "release",
					fluxHelmNamespaceLabel: "test",
				})

				list.Items = append(list.Items, expected, alsoExpected, notExpected)
				return list
			},
			expectedPass: 2,
		},
		{
			name: "A resource which is part of a kustomization but without a matching inventory entry is not ignored",
			filters: func() []FilterFunc {
				kustomizations := &ksapi.KustomizationList{}
				ks := ksapi.Kustomization{}
				ks.SetName("release")
				ks.SetNamespace("test")

				kustomizations.Items = append(kustomizations.Items, ks)

				return []FilterFunc{IgnoreIfKustomizationFound(kustomizations.Items)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("resource")

				alsoExpected := unstructured.Unstructured{}
				alsoExpected.SetName("service-account-secret")
				alsoExpected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "release",
					fluxKustomizeNamespaceLabel: "test",
				})

				list.Items = append(list.Items, expected, alsoExpected)
				return list
			},
			expectedPass: 2,
		},
		{
			name: "A resource which is part of a kustomization and has a valid matching inventory entry is ignored",
			filters: func() []FilterFunc {
				kustomizations := &ksapi.KustomizationList{}
				ks := ksapi.Kustomization{}
				ks.SetName("release")
				ks.SetNamespace("test")
				ks.Status.Inventory = &ksapi.ResourceInventory{
					Entries: []ksapi.ResourceRef{
						{
							ID: "test_cluster-role__test_rbac.authorization.k8s.io_ClusterRole",
						},
					},
				}

				kustomizations.Items = append(kustomizations.Items, ks)

				return []FilterFunc{IgnoreIfKustomizationFound(kustomizations.Items)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("resource")

				alsoExpected := unstructured.Unstructured{}
				alsoExpected.SetName("service-account-secret")
				alsoExpected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "release",
					fluxKustomizeNamespaceLabel: "test",
				})

				notExpected := unstructured.Unstructured{}
				notExpected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "rbac.authorization.k8s.io",
					Version: "v1",
					Kind:    "ClusterRole",
				})
				notExpected.SetNamespace("test")
				notExpected.SetName("cluster-role:test")
				notExpected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "release",
					fluxKustomizeNamespaceLabel: "test",
				})

				list.Items = append(list.Items, expected, alsoExpected, notExpected)
				return list
			},
			expectedPass: 2,
		},
		{
			name: "A resource which is part of a kustomization but the kustomization was not found",
			filters: func() []FilterFunc {
				kustomizations := &ksapi.KustomizationList{}
				ks := ksapi.Kustomization{}
				ks.SetName("release")
				ks.SetNamespace("test")
				ks.Status.Inventory = &ksapi.ResourceInventory{
					Entries: []ksapi.ResourceRef{
						{
							ID: "test_service-account-secret__Secret",
						},
					},
				}

				kustomizations.Items = append(kustomizations.Items, ks)

				return []FilterFunc{IgnoreIfKustomizationFound(kustomizations.Items)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("service-account-secret")
				expected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "does-not-exists",
					fluxKustomizeNamespaceLabel: "does-not-exists",
				})

				notExpected := unstructured.Unstructured{}
				notExpected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "",
					Version: "v1",
					Kind:    "Secret",
				})
				notExpected.SetNamespace("test")
				notExpected.SetName("service-account-secret")
				notExpected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "release",
					fluxKustomizeNamespaceLabel: "test",
				})

				list.Items = append(list.Items, expected, notExpected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "Resources excluded from conf: match all",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 0,
		},
		{
			name: "Resources excluded from conf: match restricted by cluster",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Cluster: "test",
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 0,
		},
		{
			name: "Resources excluded from conf: match restricted by cluster (regexp)",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Cluster: "t.*",
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 0,
		},
		{
			name: "Resources excluded from conf: match restricted by apiVersion",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						TypeMeta: v1.TypeMeta{APIVersion: "velero.io/v1"},
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 1,
		},
		{
			name: "Resources excluded from conf: match restricted by apiVersion and kind",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						TypeMeta: v1.TypeMeta{APIVersion: "velero.io/v1", Kind: "Backup"},
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by namespace",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Namespace: "velero",
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 1,
		},
		{
			name: "Resources excluded from conf: match restricted by namespace (regexp)",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Namespace: "v.*",
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 0,
		},
		{
			name: "Resources excluded from conf: match restricted by annotation",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Annotations: map[string]string{"test-annotation": "velero-capi-backup-1"},
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by annotation (regexp)",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Annotations: map[string]string{"test-annotation": "v.*"},
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by label",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Labels: map[string]string{"test-label": "velero-capi-backup-2"},
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by label (regexp)",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Labels: map[string]string{"test-label": "v.*"},
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by name",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Name: "velero-capi-backup-1",
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by name (regexp)",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreRuleExclusions("test", []gitopszombiesv1.ExcludeResources{
					{
						Name: "velero-capi-backup-(1|2)",
					},
				})}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 1,
		},
		{
			name: "A resource which is listed in the status resources of an argocd application is ignored",
			filters: func() []FilterFunc {
				applications := []unstructured.Unstructured{
					argocdApplication("my-app", "argocd", []map[string]any{
						{
							"group":     "apps",
							"kind":      "Deployment",
							"namespace": "test",
							"name":      "nginx",
						},
					}),
				}

				return []FilterFunc{IgnoreIfApplicationFound(applications)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("resource")

				notExpected := unstructured.Unstructured{}
				notExpected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "Deployment",
				})
				notExpected.SetNamespace("test")
				notExpected.SetName("nginx")
				notExpected.SetLabels(map[string]string{
					argocdInstanceLabel: "my-app",
				})

				list.Items = append(list.Items, expected, notExpected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A resource with only the argocd instance label but no matching application is not ignored",
			filters: func() []FilterFunc {
				return []FilterFunc{IgnoreIfApplicationFound(nil)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "Deployment",
				})
				expected.SetNamespace("test")
				expected.SetName("nginx")
				expected.SetLabels(map[string]string{
					argocdInstanceLabel: "my-app",
				})

				list.Items = append(list.Items, expected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A resource is not ignored if the application exists but does not list it in its status resources",
			filters: func() []FilterFunc {
				applications := []unstructured.Unstructured{
					argocdApplication("my-app", "argocd", []map[string]any{
						{
							"group":     "apps",
							"kind":      "Deployment",
							"namespace": "test",
							"name":      "other",
						},
					}),
				}

				return []FilterFunc{IgnoreIfApplicationFound(applications)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "Deployment",
				})
				expected.SetNamespace("test")
				expected.SetName("nginx")
				expected.SetLabels(map[string]string{
					argocdInstanceLabel: "my-app",
				})

				list.Items = append(list.Items, expected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A resource with an invalid argocd tracking-id annotation is not ignored",
			filters: func() []FilterFunc {
				applications := []unstructured.Unstructured{
					argocdApplication("my-app", "argocd", []map[string]any{
						{
							"group":     "apps",
							"kind":      "Deployment",
							"namespace": "test",
							"name":      "nginx",
						},
					}),
				}

				return []FilterFunc{IgnoreIfApplicationFound(applications)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "Deployment",
				})
				expected.SetNamespace("test")
				expected.SetName("nginx")
				expected.SetAnnotations(map[string]string{
					argocdTrackingIDAnnotation: "not-a-valid-tracking-id",
				})

				list.Items = append(list.Items, expected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A resource of the core api group tracked via the argocd tracking-id annotation is ignored",
			filters: func() []FilterFunc {
				applications := []unstructured.Unstructured{
					argocdApplication("my-app", "argocd", []map[string]any{
						{
							"kind":      "ConfigMap",
							"namespace": "test",
							"name":      "config",
						},
					}),
				}

				return []FilterFunc{IgnoreIfApplicationFound(applications)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				notExpected := unstructured.Unstructured{}
				notExpected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "",
					Version: "v1",
					Kind:    "ConfigMap",
				})
				notExpected.SetNamespace("test")
				notExpected.SetName("config")
				notExpected.SetAnnotations(map[string]string{
					argocdTrackingIDAnnotation: "my-app:/ConfigMap:test/config",
				})

				list.Items = append(list.Items, notExpected)
				return list
			},
			expectedPass: 0,
		},
		{
			name: "A resource tracked via a namespaced argocd tracking-id annotation is ignored",
			filters: func() []FilterFunc {
				applications := []unstructured.Unstructured{
					argocdApplication("my-app", "gitops", []map[string]any{
						{
							"group":     "apps",
							"kind":      "Deployment",
							"namespace": "test",
							"name":      "nginx",
						},
					}),
				}

				return []FilterFunc{IgnoreIfApplicationFound(applications)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				notExpected := unstructured.Unstructured{}
				notExpected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "Deployment",
				})
				notExpected.SetNamespace("test")
				notExpected.SetName("nginx")
				notExpected.SetAnnotations(map[string]string{
					argocdTrackingIDAnnotation: "gitops/my-app:apps/Deployment:test/nginx",
				})

				list.Items = append(list.Items, notExpected)
				return list
			},
			expectedPass: 0,
		},
		{
			name: "A resource claimed by a kustomization and an argocd application is ignored and reported as dual claimed",
			filters: func() []FilterFunc {
				kustomizations := &ksapi.KustomizationList{}
				ks := ksapi.Kustomization{}
				ks.SetName("release")
				ks.SetNamespace("test")
				ks.Status.Inventory = &ksapi.ResourceInventory{
					Entries: []ksapi.ResourceRef{
						{
							ID: "test_config__ConfigMap",
						},
					},
				}
				kustomizations.Items = append(kustomizations.Items, ks)

				applications := []unstructured.Unstructured{
					argocdApplication("my-app", "argocd", []map[string]any{
						{
							"kind":      "ConfigMap",
							"namespace": "test",
							"name":      "config",
						},
					}),
				}

				return []FilterFunc{
					IgnoreIfKustomizationFound(kustomizations.Items),
					IgnoreIfApplicationFound(applications),
				}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				notExpected := unstructured.Unstructured{}
				notExpected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "",
					Version: "v1",
					Kind:    "ConfigMap",
				})
				notExpected.SetNamespace("test")
				notExpected.SetName("config")
				notExpected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "release",
					fluxKustomizeNamespaceLabel: "test",
					argocdInstanceLabel:         "my-app",
				})

				list.Items = append(list.Items, notExpected)
				return list
			},
			expectedPass:       0,
			expectedDualClaims: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ch := make(chan unstructured.Unstructured, test.expectedPass+1)
			discovery := NewDiscovery(klog.NewKlogr(), test.filters()...)
			err := discovery.Discover(t.Context(), test.list(), ch)
			require.NoError(t, err)
			assert.Equal(t, test.expectedPass, len(ch))
			assert.Equal(t, test.expectedDualClaims, len(discovery.DualClaims()))
		})
	}
}

func TestParseTrackingID(t *testing.T) {
	tests := []struct {
		name       string
		trackingID string
		expected   applicationRef
		valid      bool
	}{
		{
			name:       "application without namespace prefix",
			trackingID: "my-app:apps/Deployment:test/nginx",
			expected:   applicationRef{name: "my-app"},
			valid:      true,
		},
		{
			name:       "application with namespace prefix",
			trackingID: "gitops/my-app:apps/Deployment:test/nginx",
			expected:   applicationRef{namespace: "gitops", name: "my-app"},
			valid:      true,
		},
		{
			name:       "resource of the core api group",
			trackingID: "my-app:/ConfigMap:test/config",
			expected:   applicationRef{name: "my-app"},
			valid:      true,
		},
		{
			name:       "cluster scoped resource",
			trackingID: "my-app:rbac.authorization.k8s.io/ClusterRole:/cluster-admin",
			expected:   applicationRef{name: "my-app"},
			valid:      true,
		},
		{
			name:       "empty value",
			trackingID: "",
			valid:      false,
		},
		{
			name:       "no separators",
			trackingID: "garbage",
			valid:      false,
		},
		{
			name:       "missing resource segment",
			trackingID: "my-app:apps/Deployment",
			valid:      false,
		},
		{
			name:       "too many segments",
			trackingID: "my-app:apps/Deployment:test/nginx:extra",
			valid:      false,
		},
		{
			name:       "empty application name",
			trackingID: ":apps/Deployment:test/nginx",
			valid:      false,
		},
		{
			name:       "empty kind",
			trackingID: "my-app:apps/:test/nginx",
			valid:      false,
		},
		{
			name:       "empty resource name",
			trackingID: "my-app:apps/Deployment:test/",
			valid:      false,
		},
		{
			name:       "missing resource namespace separator",
			trackingID: "my-app:apps/Deployment:nginx",
			valid:      false,
		},
		{
			name:       "too many application segments",
			trackingID: "a/b/c:apps/Deployment:test/nginx",
			valid:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ref, valid := parseTrackingID(test.trackingID)
			assert.Equal(t, test.valid, valid)
			if test.valid {
				assert.Equal(t, test.expected, ref)
			}
		})
	}
}
