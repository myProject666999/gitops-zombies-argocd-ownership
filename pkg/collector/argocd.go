package collector

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

const (
	argocdTrackingIDAnnotation = "argocd.argoproj.io/tracking-id"
	argocdInstanceLabel        = "app.kubernetes.io/instance"
)

// applicationRef identifies an Argo CD Application.
type applicationRef struct {
	namespace string
	name      string
}

// parseTrackingID parses the value of the argocd.argoproj.io/tracking-id annotation.
// The expected format is "<application>:<group>/<kind>:<namespace>/<name>".
// Applications which are not installed in the argocd namespace itself may prefix
// the application name with its namespace: "<app-namespace>/<application>:<group>/<kind>:<namespace>/<name>".
// The group may be empty for resources of the core api group (e.g. "my-app:/ConfigMap:ns/name").
// It returns false if the value does not match any of these formats.
func parseTrackingID(trackingID string) (applicationRef, bool) {
	var ref applicationRef

	parts := strings.Split(trackingID, ":")
	if len(parts) != 3 {
		return ref, false
	}

	switch appParts := strings.Split(parts[0], "/"); {
	case len(appParts) == 1 && appParts[0] != "":
		ref.name = appParts[0]
	case len(appParts) == 2 && appParts[0] != "" && appParts[1] != "":
		ref.namespace = appParts[0]
		ref.name = appParts[1]
	default:
		return ref, false
	}

	if gk := strings.Split(parts[1], "/"); len(gk) != 2 || gk[1] == "" {
		return ref, false
	}

	if nn := strings.Split(parts[2], "/"); len(nn) != 2 || nn[1] == "" {
		return ref, false
	}

	return ref, true
}

// IgnoreIfApplicationFound returns a FilterFunc which filters resources managed by an Argo CD application.
// A resource is only considered managed if a matching Application exists and the resource
// (group, kind, namespace, name) is listed in its status.resources.
func IgnoreIfApplicationFound(applications []unstructured.Unstructured) FilterFunc {
	return func(res unstructured.Unstructured, logger klog.Logger) (bool, Manager) {
		var candidates []applicationRef

		if trackingID, ok := res.GetAnnotations()[argocdTrackingIDAnnotation]; ok {
			if ref, valid := parseTrackingID(trackingID); valid {
				candidates = append(candidates, ref)
			} else {
				logger.V(1).
					Info("could not parse argocd tracking-id annotation", "trackingId", trackingID, "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion())
			}
		}

		if instance, ok := res.GetLabels()[argocdInstanceLabel]; ok && instance != "" {
			candidates = append(candidates, applicationRef{name: instance})
		}

		if len(candidates) == 0 {
			return false, Manager{}
		}

		for _, candidate := range candidates {
			for i := range applications {
				app := &applications[i]
				if app.GetName() != candidate.name {
					continue
				}
				if candidate.namespace != "" && app.GetNamespace() != candidate.namespace {
					continue
				}

				if applicationManagesResource(app, res) {
					return true, Manager{Kind: "Application", Name: app.GetName(), Namespace: app.GetNamespace()}
				}

				logger.V(1).
					Info("resource is not part of the application status resources", "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion(), "applicationName", app.GetName(), "applicationNamespace", app.GetNamespace())
			}
		}

		return false, Manager{}
	}
}

// applicationManagesResource returns true if the resource (group, kind, namespace, name)
// is listed in the status.resources of the given application.
func applicationManagesResource(app *unstructured.Unstructured, res unstructured.Unstructured) bool {
	resources, found, err := unstructured.NestedSlice(app.Object, "status", "resources")
	if err != nil || !found {
		return false
	}

	gvk := res.GroupVersionKind()
	for _, item := range resources {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}

		group, _, _ := unstructured.NestedString(entry, "group")
		kind, _, _ := unstructured.NestedString(entry, "kind")
		namespace, _, _ := unstructured.NestedString(entry, "namespace")
		name, _, _ := unstructured.NestedString(entry, "name")

		if group == gvk.Group &&
			kind == gvk.Kind &&
			namespace == res.GetNamespace() &&
			name == res.GetName() {
			return true
		}
	}

	return false
}
