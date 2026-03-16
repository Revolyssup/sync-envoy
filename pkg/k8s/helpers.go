package k8s

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// IstioResourceTypes lists all known Istio CRD GroupVersionResources.
var IstioResourceTypes = []schema.GroupVersionResource{
	{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"},
	{Group: "networking.istio.io", Version: "v1", Resource: "destinationrules"},
	{Group: "networking.istio.io", Version: "v1", Resource: "gateways"},
	{Group: "networking.istio.io", Version: "v1", Resource: "serviceentries"},
	{Group: "networking.istio.io", Version: "v1", Resource: "workloadentries"},
	{Group: "networking.istio.io", Version: "v1", Resource: "workloadgroups"},
	{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies"},
	{Group: "security.istio.io", Version: "v1", Resource: "peerauthentications"},
	{Group: "security.istio.io", Version: "v1", Resource: "requestauthentications"},
	{Group: "telemetry.istio.io", Version: "v1", Resource: "telemetries"},
	{Group: "extensions.istio.io", Version: "v1", Resource: "wasmplugins"},
}

// ResourceExists checks if a given GVR exists in the cluster using discovery.
func ResourceExists(disc discovery.DiscoveryInterface, gvr schema.GroupVersionResource) (bool, error) {
	resourceList, err := disc.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		// Group/version doesn't exist in the cluster
		return false, nil
	}
	for _, r := range resourceList.APIResources {
		if r.Name == gvr.Resource {
			return true, nil
		}
	}
	return false, nil
}

// GetResourceNameFromKind uses discovery to find the plural resource name for a GVK.
func GetResourceNameFromKind(disc discovery.DiscoveryInterface, gvk schema.GroupVersionKind) (string, error) {
	resourceList, err := disc.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if err != nil {
		return "", err
	}
	for _, r := range resourceList.APIResources {
		if r.Kind == gvk.Kind {
			return r.Name, nil
		}
	}
	return "", fmt.Errorf("no resource found for kind %s in group %s", gvk.Kind, gvk.GroupVersion())
}

// CleanMetadata removes server-managed fields and XCP-reserved annotations
// from an unstructured object so it can be compared or applied cleanly.
func CleanMetadata(u *unstructured.Unstructured) {
	unstructured.RemoveNestedField(u.Object, "status")
	unstructured.RemoveNestedField(u.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(u.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(u.Object, "metadata", "uid")
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, "metadata", "generation")

	// Strip XCP-reserved annotations that admission webhooks reject
	// (e.g. edge-content-hash-mutation.xcp.tetrate.io).
	annotations := u.GetAnnotations()
	if len(annotations) > 0 {
		cleaned := make(map[string]string, len(annotations))
		for k, v := range annotations {
			if !strings.Contains(k, "xcp.tetrate.io") {
				cleaned[k] = v
			}
		}
		if len(cleaned) == 0 {
			unstructured.RemoveNestedField(u.Object, "metadata", "annotations")
		} else {
			u.SetAnnotations(cleaned)
		}
	}
}
