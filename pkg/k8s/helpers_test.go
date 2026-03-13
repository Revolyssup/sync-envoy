package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCleanMetadata(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "networking.istio.io/v1",
			"kind":       "VirtualService",
			"metadata": map[string]interface{}{
				"name":              "test",
				"namespace":         "default",
				"resourceVersion":   "12345",
				"uid":               "abc-123",
				"creationTimestamp":  "2024-01-01T00:00:00Z",
				"generation":        int64(1),
				"managedFields":     []interface{}{},
				"labels":            map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"hosts": []interface{}{"test"},
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{},
			},
		},
	}

	CleanMetadata(u)

	// Verify removed fields
	if _, ok := u.Object["status"]; ok {
		t.Error("status should have been removed")
	}

	meta := u.Object["metadata"].(map[string]interface{})
	for _, field := range []string{"resourceVersion", "uid", "creationTimestamp", "generation", "managedFields"} {
		if _, ok := meta[field]; ok {
			t.Errorf("metadata.%s should have been removed", field)
		}
	}

	// Verify preserved fields
	if meta["name"] != "test" {
		t.Error("metadata.name should be preserved")
	}
	if meta["namespace"] != "default" {
		t.Error("metadata.namespace should be preserved")
	}
	labels := meta["labels"].(map[string]interface{})
	if labels["app"] != "test" {
		t.Error("metadata.labels should be preserved")
	}
	if _, ok := u.Object["spec"]; !ok {
		t.Error("spec should be preserved")
	}
}

func TestCrKey_Namespaced(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind": "VirtualService",
			"metadata": map[string]interface{}{
				"name":      "httpbin",
				"namespace": "default",
			},
		},
	}
	key := crKey(u)
	expected := "default/virtualservice/httpbin"
	if key != expected {
		t.Errorf("crKey = %q, want %q", key, expected)
	}
}

func TestCrKey_ClusterScoped(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind": "Gateway",
			"metadata": map[string]interface{}{
				"name": "my-gateway",
			},
		},
	}
	key := crKey(u)
	expected := "gateway/my-gateway"
	if key != expected {
		t.Errorf("crKey = %q, want %q", key, expected)
	}
}

func TestCrMetadata(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind": "DestinationRule",
			"metadata": map[string]interface{}{
				"name":      "httpbin",
				"namespace": "default",
			},
		},
	}
	meta := crMetadata(u)
	if meta["kind"] != "DestinationRule" {
		t.Errorf("kind = %q, want DestinationRule", meta["kind"])
	}
	if meta["name"] != "httpbin" {
		t.Errorf("name = %q, want httpbin", meta["name"])
	}
	if meta["namespace"] != "default" {
		t.Errorf("namespace = %q, want default", meta["namespace"])
	}
}
