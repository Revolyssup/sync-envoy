package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/revolyssup/sync-envoy/pkg/topology"
	"github.com/revolyssup/sync-envoy/pkg/types"
)

func TestCurrentFileUpdater_WriteNamespaced(t *testing.T) {
	tmpDir := t.TempDir()
	updater := NewCurrentFileUpdater(tmpDir)

	event := types.Event{
		Type:    types.EventAdd,
		Key:     "default/virtualservice/httpbin",
		NewData: []byte("apiVersion: networking.istio.io/v1\nkind: VirtualService\n"),
		Metadata: map[string]string{
			"kind":      "VirtualService",
			"name":      "httpbin",
			"namespace": "default",
		},
	}

	err := updater.Update(context.Background(), event)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, "default", "virtualservice", "httpbin_current.yaml")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read written file: %v", err)
	}
	if string(data) != string(event.NewData) {
		t.Errorf("file content mismatch: got %q, want %q", string(data), string(event.NewData))
	}
}

func TestCurrentFileUpdater_WriteClusterScoped(t *testing.T) {
	tmpDir := t.TempDir()
	updater := NewCurrentFileUpdater(tmpDir)

	event := types.Event{
		Type:    types.EventAdd,
		Key:     "gateway/my-gateway",
		NewData: []byte("apiVersion: networking.istio.io/v1\nkind: Gateway\n"),
		Metadata: map[string]string{
			"kind":      "Gateway",
			"name":      "my-gateway",
			"namespace": "",
		},
	}

	err := updater.Update(context.Background(), event)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, "gateway", "my-gateway_current.yaml")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatalf("Expected file at %s does not exist", expectedPath)
	}
}

func TestCurrentFileUpdater_SkipNoDiff(t *testing.T) {
	tmpDir := t.TempDir()
	updater := NewCurrentFileUpdater(tmpDir)

	event := types.Event{
		Type:    types.EventUpdate,
		Key:     "default/virtualservice/httpbin",
		NewData: []byte("same content"),
		Metadata: map[string]string{
			"kind":      "VirtualService",
			"name":      "httpbin",
			"namespace": "default",
		},
	}

	// First write
	if err := updater.Update(context.Background(), event); err != nil {
		t.Fatalf("First update failed: %v", err)
	}

	// Modify the file on disk to verify it won't be overwritten
	path := filepath.Join(tmpDir, "default", "virtualservice", "httpbin_current.yaml")
	os.WriteFile(path, []byte("modified"), 0644)

	// Second write with same data should skip
	if err := updater.Update(context.Background(), event); err != nil {
		t.Fatalf("Second update failed: %v", err)
	}

	// File should still have "modified" content since diff was skipped
	data, _ := os.ReadFile(path)
	if string(data) != "modified" {
		t.Errorf("expected file to remain modified (no-diff skip), got: %q", string(data))
	}
}

func TestCurrentFileUpdater_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	updater := NewCurrentFileUpdater(tmpDir)

	// First create the file
	event := types.Event{
		Type:    types.EventAdd,
		Key:     "default/virtualservice/httpbin",
		NewData: []byte("test content"),
		Metadata: map[string]string{
			"kind":      "VirtualService",
			"name":      "httpbin",
			"namespace": "default",
		},
	}
	updater.Update(context.Background(), event)

	// Now delete
	deleteEvent := types.Event{
		Type: types.EventDelete,
		Key:  "default/virtualservice/httpbin",
		Metadata: map[string]string{
			"kind":      "VirtualService",
			"name":      "httpbin",
			"namespace": "default",
		},
	}

	err := updater.Update(context.Background(), deleteEvent)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	path := filepath.Join(tmpDir, "default", "virtualservice", "httpbin_current.yaml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestCurrentFileUpdater_WritesTopology(t *testing.T) {
	tmpDir := t.TempDir()
	topo := topology.NewFile(tmpDir, "Istio Resource Topology")
	updater := NewCurrentFileUpdater(tmpDir).WithTopology(topo)

	// Add a Gateway
	gwYAML := []byte(`apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: my-gw
  namespace: default
  labels:
    xcp.tetrate.io/workspace: ws1
    xcp.tetrate.io/gatewayGroup: gg1
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
	gwEvent := types.Event{
		Type:    types.EventAdd,
		Key:     "default/gateway/my-gw",
		NewData: gwYAML,
		Metadata: map[string]string{
			"kind":      "Gateway",
			"name":      "my-gw",
			"namespace": "default",
		},
	}
	if err := updater.Update(context.Background(), gwEvent); err != nil {
		t.Fatalf("Gateway update failed: %v", err)
	}

	// Add a VirtualService
	vsYAML := []byte(`apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: my-vs
  namespace: default
spec:
  gateways:
  - default/my-gw
  hosts:
  - app.example.com
  http:
  - route:
    - destination:
        host: app.default.svc.cluster.local
        port:
          number: 8080
`)
	vsEvent := types.Event{
		Type:    types.EventAdd,
		Key:     "default/virtualservice/my-vs",
		NewData: vsYAML,
		Metadata: map[string]string{
			"kind":      "VirtualService",
			"name":      "my-vs",
			"namespace": "default",
		},
	}
	if err := updater.Update(context.Background(), vsEvent); err != nil {
		t.Fatalf("VirtualService update failed: %v", err)
	}

	// Check topology.md was written
	topoPath := filepath.Join(tmpDir, "topology.md")
	data, err := os.ReadFile(topoPath)
	if err != nil {
		t.Fatalf("topology.md not written: %v", err)
	}

	content := string(data)

	// Check Gateway edges
	if !strings.Contains(content, "Gateway/my-gw --[selector]--> app=my-gateway") {
		t.Errorf("missing Gateway selector edge in topology:\n%s", content)
	}
	if !strings.Contains(content, "Gateway/my-gw --[serves]--> *.example.com:443/HTTPS") {
		t.Errorf("missing Gateway serves edge in topology:\n%s", content)
	}

	// Check VirtualService edges
	if !strings.Contains(content, "VirtualService/my-vs --[gateway]--> Gateway/default/my-gw") {
		t.Errorf("missing VS gateway edge in topology:\n%s", content)
	}
	if !strings.Contains(content, "VirtualService/my-vs --[route]--> app.default.svc.cluster.local:8080") {
		t.Errorf("missing VS route edge in topology:\n%s", content)
	}

	// Check XCP provenance
	if !strings.Contains(content, "Gateway/my-gw --[managed by]--> Workspace/ws1 > GatewayGroup/gg1") {
		t.Errorf("missing XCP provenance edge in topology:\n%s", content)
	}
}

func TestCurrentFileUpdater_TopologyRemovedOnDelete(t *testing.T) {
	tmpDir := t.TempDir()
	topo := topology.NewFile(tmpDir, "Istio Resource Topology")
	updater := NewCurrentFileUpdater(tmpDir).WithTopology(topo)

	// Add a resource
	event := types.Event{
		Type: types.EventAdd,
		Key:  "default/gateway/gw1",
		NewData: []byte(`apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: gw1
  namespace: default
spec:
  selector:
    app: gw1
`),
		Metadata: map[string]string{
			"kind":      "Gateway",
			"name":      "gw1",
			"namespace": "default",
		},
	}
	updater.Update(context.Background(), event)

	topoPath := filepath.Join(tmpDir, "topology.md")
	if _, err := os.Stat(topoPath); os.IsNotExist(err) {
		t.Fatal("topology.md should exist after add")
	}

	// Delete the resource
	deleteEvent := types.Event{
		Type: types.EventDelete,
		Key:  "default/gateway/gw1",
		Metadata: map[string]string{
			"kind":      "Gateway",
			"name":      "gw1",
			"namespace": "default",
		},
	}
	updater.Update(context.Background(), deleteEvent)

	// topology.md should be removed when no edges remain
	if _, err := os.Stat(topoPath); !os.IsNotExist(err) {
		t.Error("topology.md should be removed when all resources are deleted")
	}
}
