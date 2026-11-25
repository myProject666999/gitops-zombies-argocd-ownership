// Package collector provides functionality for discovering and collecting Kubernetes resources.
package collector

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	helmapi "github.com/fluxcd/helm-controller/api/v2"
	ksapi "github.com/fluxcd/kustomize-controller/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
	"sigs.k8s.io/cli-utils/pkg/object"

	v1 "github.com/raffis/gitops-zombies/pkg/apis/gitopszombies/v1"
)

const (
	fluxHelmNameLabel           = "helm.toolkit.fluxcd.io/name"
	fluxHelmNamespaceLabel      = "helm.toolkit.fluxcd.io/namespace"
	fluxKustomizeNameLabel      = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizeNamespaceLabel = "kustomize.toolkit.fluxcd.io/namespace"
)

// Manager identifies the gitops manager which claims ownership of a resource.
type Manager struct {
	Kind      string
	Name      string
	Namespace string
}

// String returns a human readable identifier of the manager.
func (m Manager) String() string {
	return fmt.Sprintf("%s %s/%s", m.Kind, m.Namespace, m.Name)
}

// DualClaim describes a resource which is claimed by more than one gitops manager.
type DualClaim struct {
	Resource unstructured.Unstructured
	Managers []Manager
}

// FilterFunc is a function that filters resources.
// It returns true if the resource is filtered (i.e. not considered a zombie)
// and the gitops manager which claims the resource, if any.
type FilterFunc func(res unstructured.Unstructured, logger klog.Logger) (bool, Manager)

// Interface represents collector interface.
type Interface interface {
	Discover(ctx context.Context, list *unstructured.UnstructuredList, ch chan unstructured.Unstructured) error
	DualClaims() []DualClaim
}

type discovery struct {
	filters    []FilterFunc
	logger     klog.Logger
	mu         sync.Mutex
	dualClaims []DualClaim
}

// NewDiscovery returns a new discovery instance.
func NewDiscovery(logger klog.Logger, filters ...FilterFunc) Interface {
	return &discovery{
		logger:  logger,
		filters: filters,
	}
}

// Discover validates discovered resources against all filters and adds it to consumer channel.
// Resources claimed by more than one gitops manager are collected as dual claims.
func (d *discovery) Discover(
	_ context.Context,
	list *unstructured.UnstructuredList,
	ch chan unstructured.Unstructured,
) error {
	for _, res := range list.Items {
		d.logger.V(1).Info("validate resource", "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion())

		var (
			filtered bool
			managers []Manager
		)

		for _, filter := range d.filters {
			ok, manager := filter(res, d.logger)
			if !ok {
				continue
			}

			filtered = true
			if manager.Kind != "" {
				managers = append(managers, manager)
			}
		}

		if len(managers) > 1 {
			d.logger.V(1).
				Info("resource is claimed by multiple gitops managers", "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion(), "managers", fmt.Sprintf("%v", managers))

			d.mu.Lock()
			d.dualClaims = append(d.dualClaims, DualClaim{Resource: res, Managers: managers})
			d.mu.Unlock()
		}

		if filtered {
			continue
		}

		ch <- res
	}

	return nil
}

// DualClaims returns all resources which are claimed by more than one gitops manager.
func (d *discovery) DualClaims() []DualClaim {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]DualClaim(nil), d.dualClaims...)
}

// IgnoreOwnedResource returns a FilterFunc which filters resources owner by parents ones.
func IgnoreOwnedResource() FilterFunc {
	return func(res unstructured.Unstructured, logger klog.Logger) (bool, Manager) {
		if refs := res.GetOwnerReferences(); len(refs) > 0 {
			logger.V(1).
				Info("ignore resource owned by parent", "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion())
			return true, Manager{}
		}

		return false, Manager{}
	}
}

// IgnoreServiceAccountSecret returns a FilterFunc which filters secrets linked to a service account.
func IgnoreServiceAccountSecret() FilterFunc {
	return func(res unstructured.Unstructured, _ klog.Logger) (bool, Manager) {
		if res.GetKind() == "Secret" && res.GetAPIVersion() == "v1" {
			if _, ok := res.GetAnnotations()["kubernetes.io/service-account.name"]; ok {
				return true, Manager{}
			}
		}

		return false, Manager{}
	}
}

// IgnoreHelmSecret returns a FilterFunc which filters secrets owned by helm.
func IgnoreHelmSecret() FilterFunc {
	return func(res unstructured.Unstructured, _ klog.Logger) (bool, Manager) {
		if res.GetKind() == "Secret" && res.GetAPIVersion() == "v1" {
			if v, ok := res.GetLabels()["owner"]; ok && v == "helm" {
				return true, Manager{}
			}
		}

		return false, Manager{}
	}
}

// IgnoreIfHelmReleaseFound returns a FilterFunc which filters resources part of an helm release.
func IgnoreIfHelmReleaseFound(helmReleases []helmapi.HelmRelease) FilterFunc {
	return func(res unstructured.Unstructured, logger klog.Logger) (bool, Manager) {
		labels := res.GetLabels()
		if helmName, ok := labels[fluxHelmNameLabel]; ok {
			if helmNamespace, ok := labels[fluxHelmNamespaceLabel]; ok {
				if hasResource(helmReleases, helmName, helmNamespace) {
					return true, Manager{Kind: "HelmRelease", Name: helmName, Namespace: helmNamespace}
				}

				logger.V(1).
					Info("helmrelease not found from resource", "helmReleaseName", helmName, "helmReleaseNamespace", helmNamespace, "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion())
			}
		}

		return false, Manager{}
	}
}

// IgnoreIfKustomizationFound returns a FilterFunc which filters resources part of a flux kustomization.
func IgnoreIfKustomizationFound(kustomizations []ksapi.Kustomization) FilterFunc {
	return func(res unstructured.Unstructured, logger klog.Logger) (bool, Manager) {
		labels := res.GetLabels()
		ksName, okKsName := labels[fluxKustomizeNameLabel]
		ksNamespace, okKsNamespace := labels[fluxKustomizeNamespaceLabel]
		if !okKsName || !okKsNamespace {
			return false, Manager{}
		}

		if ks := findKustomization(kustomizations, ksName, ksNamespace); ks != nil {
			obj := object.ObjMetadata{
				Namespace: res.GetNamespace(),
				Name:      res.GetName(),
				GroupKind: res.GroupVersionKind().GroupKind(),
			}
			id := obj.String()

			logger.V(1).
				Info("lookup kustomization inventory", "kustomizationName", ksName, "kustomizationNamespace", ksNamespace, "resourceId", id)

			if ks.Status.Inventory != nil {
				for _, entry := range ks.Status.Inventory.Entries {
					if entry.ID == id {
						return true, Manager{Kind: "Kustomization", Name: ksName, Namespace: ksNamespace}
					}
				}
			}

			logger.V(1).
				Info("resource is not part of the kustomization inventory", "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion(), "kustomizationName", ksName, "kustomizationNamespace", ksNamespace)
			return false, Manager{}
		}
		logger.V(1).
			Info("kustomization not found from resource", "resource", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion(), "kustomizationName", ksName, "kustomizationNamespace", ksNamespace)
		return false, Manager{}
	}
}

// IgnoreRuleExclusions returns a FilterFunc which excludes resources part of configuration exclusions.
func IgnoreRuleExclusions(cluster string, exclusions []v1.ExcludeResources) FilterFunc {
	return func(res unstructured.Unstructured, _ klog.Logger) (bool, Manager) {
		for _, exclusion := range exclusions {
			if !matchesCluster(cluster, exclusion.Cluster) {
				continue
			}

			if !resourceMatchesGetAPIVersionAndKind(res, exclusion.APIVersion, exclusion.Kind) {
				continue
			}

			if !resourceMatchesNamespace(res, exclusion.Namespace) {
				continue
			}

			if !resourceMatchesMetadata(res.GetAnnotations(), exclusion.Annotations) {
				continue
			}

			if !resourceMatchesMetadata(res.GetLabels(), exclusion.Labels) {
				continue
			}

			if resourceMatchesName(res, exclusion.Name) {
				return true, Manager{}
			}
		}
		return false, Manager{}
	}
}

func matchesCluster(cluster, clusterExclude string) bool {
	if clusterExclude != "" {
		match, err := regexp.MatchString(`^`+clusterExclude+`$`, cluster)
		if err != nil {
			klog.Error(err)
		}

		return match
	}

	return true
}

func resourceMatchesGetAPIVersionAndKind(res unstructured.Unstructured, apiVersion, kind string) bool {
	// match all api versions
	resVer := res.GetAPIVersion()
	if apiVersion != "" && resVer != apiVersion {
		return false
	}

	if kind != "" && res.GetKind() != kind {
		return false
	}

	return true
}

func resourceMatchesNamespace(res unstructured.Unstructured, namespace string) bool {
	if namespace != "" {
		match, err := regexp.MatchString(`^`+namespace+`$`, res.GetNamespace())
		if err != nil {
			klog.Error(err)
		}
		if !match {
			return false
		}
	}

	return true
}

func resourceMatchesMetadata(resMetadata, metadata map[string]string) bool {
	for key, val := range metadata {
		v, ok := resMetadata[key]
		if !ok {
			return false
		}

		match, err := regexp.MatchString(`^`+val+`$`, v)
		if err != nil {
			klog.Error(err)
		}
		if !match {
			return false
		}
	}

	return true
}

func resourceMatchesName(res unstructured.Unstructured, name string) bool {
	if name != "" {
		match, err := regexp.MatchString(`^`+name+`$`, res.GetName())
		if err != nil {
			klog.Error(err)
		}

		return match
	}

	return true
}

func hasResource(pool []helmapi.HelmRelease, name, namespace string) bool {
	for _, res := range pool {
		if res.GetName() == name && res.GetNamespace() == namespace {
			return true
		}
	}

	return false
}

func findKustomization(pool []ksapi.Kustomization, name, namespace string) *ksapi.Kustomization {
	for _, res := range pool {
		if res.GetName() == name && res.GetNamespace() == namespace {
			return &res
		}
	}

	return nil
}
