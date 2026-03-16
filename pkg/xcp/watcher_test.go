package xcp

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestExtractGatewayHostnames_IngressGateway(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.xcp.tetrate.io/v2",
			"kind":       "IngressGateway",
			"metadata":   map[string]interface{}{"name": "test-gw", "namespace": "istio-system"},
			"spec": map[string]interface{}{
				"http": []interface{}{
					map[string]interface{}{"hostname": "echo.tetrate.io", "name": "echo", "port": int64(8443)},
				},
			},
		},
	}
	hostnames := extractGatewayHostnames(u)
	if len(hostnames) != 1 || hostnames[0] != "echo.tetrate.io" {
		t.Errorf("expected [echo.tetrate.io], got %v", hostnames)
	}
}

func TestExtractGatewayHostnames_GatewayTCP(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.xcp.tetrate.io/v2",
			"kind":       "Gateway",
			"metadata":   map[string]interface{}{"name": "test-gw", "namespace": "istio-system"},
			"spec": map[string]interface{}{
				"tcp": []interface{}{
					map[string]interface{}{"hostname": "echo-tcp.tetrate.io", "name": "echo-tcp", "port": int64(6666)},
				},
			},
		},
	}
	hostnames := extractGatewayHostnames(u)
	if len(hostnames) != 1 || hostnames[0] != "echo-tcp.tetrate.io" {
		t.Errorf("expected [echo-tcp.tetrate.io], got %v", hostnames)
	}
}

func TestExtractGatewayHostnames_MultipleHostnames(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.xcp.tetrate.io/v2",
			"kind":       "IngressGateway",
			"metadata":   map[string]interface{}{"name": "multi-gw", "namespace": "istio-system"},
			"spec": map[string]interface{}{
				"http": []interface{}{
					map[string]interface{}{"hostname": "a.example.com"},
					map[string]interface{}{"hostname": "b.example.com"},
				},
				"tcp": []interface{}{
					map[string]interface{}{"hostname": "c.example.com"},
				},
			},
		},
	}
	hostnames := extractGatewayHostnames(u)
	if len(hostnames) != 3 {
		t.Fatalf("expected 3 hostnames, got %v", hostnames)
	}
	want := []string{"a.example.com", "b.example.com", "c.example.com"}
	for i, h := range want {
		if hostnames[i] != h {
			t.Errorf("hostname[%d]: got %q, want %q", i, hostnames[i], h)
		}
	}
}

func TestExtractGatewayHostnames_NonGatewayKind(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "traffic.xcp.tetrate.io/v2",
			"kind":       "ServiceRoute",
			"metadata":   map[string]interface{}{"name": "sr", "namespace": "istio-system"},
			"spec": map[string]interface{}{
				"http": []interface{}{
					map[string]interface{}{"hostname": "ignored.example.com"},
				},
			},
		},
	}
	hostnames := extractGatewayHostnames(u)
	if len(hostnames) != 0 {
		t.Errorf("expected no hostnames for ServiceRoute, got %v", hostnames)
	}
}

func TestXcpMetadata_IncludesHostnames(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "gateway.xcp.tetrate.io/v2",
			"kind":       "IngressGateway",
			"metadata":   map[string]interface{}{"name": "test-gw", "namespace": "istio-system"},
			"spec": map[string]interface{}{
				"http": []interface{}{
					map[string]interface{}{"hostname": "echo.tetrate.io"},
				},
			},
		},
	}
	m := xcpMetadata(u)
	if m["hostnames"] == "" {
		t.Fatal("expected hostnames in metadata")
	}
	if m["hostnames"] != `["echo.tetrate.io"]` {
		t.Errorf("unexpected hostnames value: %s", m["hostnames"])
	}
}
