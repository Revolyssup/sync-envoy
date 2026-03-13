package topology

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFile_SetAndWrite(t *testing.T) {
	tmpDir := t.TempDir()
	f := NewFile(tmpDir, "Test Topology")

	f.Set("default", "gateway", "gw1", []Edge{
		{From: "Gateway/gw1", Relation: "selector", To: "app=httpbin"},
	})

	path := filepath.Join(tmpDir, "topology.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("topology.md not written: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "# Test Topology") {
		t.Error("missing title")
	}
	if !strings.Contains(content, "## default") {
		t.Error("missing namespace header")
	}
	if !strings.Contains(content, "Gateway/gw1 --[selector]--> app=httpbin") {
		t.Errorf("missing edge, got:\n%s", content)
	}
}

func TestFile_RemoveDeletesFile(t *testing.T) {
	tmpDir := t.TempDir()
	f := NewFile(tmpDir, "Test")

	f.Set("default", "gateway", "gw1", []Edge{
		{From: "Gateway/gw1", To: "something"},
	})

	path := filepath.Join(tmpDir, "topology.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("topology.md should exist after Set")
	}

	f.Remove("default", "gateway", "gw1")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("topology.md should be removed when no edges remain")
	}
}

func TestFile_DefaultArrow(t *testing.T) {
	tmpDir := t.TempDir()
	f := NewFile(tmpDir, "XCP Topology")

	f.Set("xcp-system", "ingressgateway", "igw1", []Edge{
		{From: "IngressGateway/igw1", To: "Gateway/igw1 (echo)"},
		{From: "IngressGateway/igw1", To: "VirtualService/vs-igw1 (echo)"},
	})

	data, _ := os.ReadFile(filepath.Join(tmpDir, "topology.md"))
	content := string(data)

	if !strings.Contains(content, "IngressGateway/igw1 ---> Gateway/igw1 (echo)") {
		t.Errorf("missing default arrow, got:\n%s", content)
	}
	if !strings.Contains(content, "IngressGateway/igw1 ---> VirtualService/vs-igw1 (echo)") {
		t.Errorf("missing second edge, got:\n%s", content)
	}
}

func TestFile_MultipleNamespaces(t *testing.T) {
	tmpDir := t.TempDir()
	f := NewFile(tmpDir, "Multi-NS")

	f.Set("ns-a", "gateway", "gw1", []Edge{
		{From: "Gateway/gw1", Relation: "selector", To: "app=a"},
	})
	f.Set("ns-b", "gateway", "gw2", []Edge{
		{From: "Gateway/gw2", Relation: "selector", To: "app=b"},
	})

	data, _ := os.ReadFile(filepath.Join(tmpDir, "topology.md"))
	content := string(data)

	// ns-a should come before ns-b (sorted)
	aIdx := strings.Index(content, "## ns-a")
	bIdx := strings.Index(content, "## ns-b")
	if aIdx == -1 || bIdx == -1 {
		t.Fatalf("missing namespace headers in:\n%s", content)
	}
	if aIdx >= bIdx {
		t.Error("namespaces should be sorted alphabetically")
	}
}

func TestExtractIstioEdges_VirtualService(t *testing.T) {
	yaml := []byte(`apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: my-vs
  namespace: default
spec:
  gateways:
  - default/my-gw
  - mesh
  hosts:
  - app.example.com
  http:
  - route:
    - destination:
        host: app.default.svc.cluster.local
        port:
          number: 8080
`)
	edges := ExtractIstioEdges("VirtualService", "my-vs", "default", yaml)

	found := map[string]bool{}
	for _, e := range edges {
		found[e.From+" "+e.Relation+" "+e.To] = true
	}

	if !found["VirtualService/my-vs gateway Gateway/default/my-gw"] {
		t.Errorf("missing gateway edge, got: %v", edges)
	}
	if found["VirtualService/my-vs gateway Gateway/mesh"] {
		t.Error("mesh gateway should be excluded")
	}
	if !found["VirtualService/my-vs route app.default.svc.cluster.local:8080"] {
		t.Errorf("missing route edge, got: %v", edges)
	}
}

func TestExtractIstioEdges_Gateway(t *testing.T) {
	yaml := []byte(`apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: my-gw
  namespace: default
spec:
  selector:
    app: my-gateway
  servers:
  - hosts:
    - "*.example.com"
    port:
      number: 443
      protocol: HTTPS
`)
	edges := ExtractIstioEdges("Gateway", "my-gw", "default", yaml)

	found := map[string]bool{}
	for _, e := range edges {
		found[e.From+" "+e.Relation+" "+e.To] = true
	}

	if !found["Gateway/my-gw selector app=my-gateway"] {
		t.Errorf("missing selector edge, got: %v", edges)
	}
	if !found["Gateway/my-gw serves *.example.com:443/HTTPS"] {
		t.Errorf("missing serves edge, got: %v", edges)
	}
}

func TestExtractIstioEdges_DestinationRule(t *testing.T) {
	yaml := []byte(`apiVersion: networking.istio.io/v1
kind: DestinationRule
metadata:
  name: reviews
  namespace: default
spec:
  host: reviews.default.svc.cluster.local
  subsets:
  - name: v1
    labels:
      version: v1
  - name: v2
    labels:
      version: v2
`)
	edges := ExtractIstioEdges("DestinationRule", "reviews", "default", yaml)

	found := map[string]bool{}
	for _, e := range edges {
		found[e.From+" "+e.Relation+" "+e.To] = true
	}

	if !found["DestinationRule/reviews host reviews.default.svc.cluster.local"] {
		t.Errorf("missing host edge, got: %v", edges)
	}
	if !found["DestinationRule/reviews subset v1 {version=v1}"] {
		t.Errorf("missing v1 subset edge, got: %v", edges)
	}
	if !found["DestinationRule/reviews subset v2 {version=v2}"] {
		t.Errorf("missing v2 subset edge, got: %v", edges)
	}
}

func TestExtractIstioEdges_AuthorizationPolicy(t *testing.T) {
	yaml := []byte(`apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: deny-all
  namespace: default
spec:
  selector:
    matchLabels:
      app: httpbin
`)
	edges := ExtractIstioEdges("AuthorizationPolicy", "deny-all", "default", yaml)

	if len(edges) == 0 {
		t.Fatal("expected edges for AuthorizationPolicy with selector")
	}
	found := false
	for _, e := range edges {
		if e.From == "AuthorizationPolicy/deny-all" && e.Relation == "selector" && e.To == "app=httpbin" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing selector edge, got: %v", edges)
	}
}

func TestExtractIstioEdges_ServiceEntry(t *testing.T) {
	yaml := []byte(`apiVersion: networking.istio.io/v1
kind: ServiceEntry
metadata:
  name: ext-svc
  namespace: default
spec:
  hosts:
  - api.external.com
  - cdn.external.com
`)
	edges := ExtractIstioEdges("ServiceEntry", "ext-svc", "default", yaml)

	found := map[string]bool{}
	for _, e := range edges {
		found[e.To] = true
	}

	if !found["api.external.com"] || !found["cdn.external.com"] {
		t.Errorf("missing hosts, got: %v", edges)
	}
}

func TestExtractIstioEdges_XCPProvenance(t *testing.T) {
	yaml := []byte(`apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: my-gw
  namespace: default
  labels:
    xcp.tetrate.io/workspace: ws1
    xcp.tetrate.io/gatewayGroup: gg1
spec:
  selector:
    app: gw
`)
	edges := ExtractIstioEdges("Gateway", "my-gw", "default", yaml)

	found := false
	for _, e := range edges {
		if e.Relation == "managed by" && strings.Contains(e.To, "Workspace/ws1") && strings.Contains(e.To, "GatewayGroup/gg1") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing XCP provenance edge, got: %v", edges)
	}
}
