package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/revolyssup/sync-envoy/pkg/correlation"
	"github.com/revolyssup/sync-envoy/pkg/types"
)

// mockPodLister implements PodLister for tests.
type mockPodLister struct {
	pods []string
	err  error
}

func (m *mockPodLister) ListPodNames(_ context.Context, _ string, _ map[string]string) ([]string, error) {
	return m.pods, m.err
}

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

func TestSelectorCorrelation_WritesCorrelationJSON(t *testing.T) {
	istioDir := t.TempDir()
	envoyDir := t.TempDir()

	// Pre-create a listener.json so ExtractListenerNamesFromFile has data to read.
	podDir := filepath.Join(envoyDir, "default", "httpbin-abc123")
	os.MkdirAll(podDir, 0755)
	listenerJSON := `{
		"pod_name": "httpbin-abc123", "namespace": "default", "config_type": "listener",
		"config": {"dynamic_listeners": [
			{"name": "0.0.0.0_8080"},
			{"name": "0.0.0.0_15006"}
		]}
	}`
	os.WriteFile(filepath.Join(podDir, "listener.json"), []byte(listenerJSON), 0644)

	lister := &mockPodLister{pods: []string{"httpbin-abc123"}}
	updater := NewCurrentFileUpdater(istioDir).WithSelectorCorrelation(lister, envoyDir)

	yamlData := []byte(`apiVersion: security.istio.io/v1beta1
kind: AuthorizationPolicy
metadata:
  name: deny-all
  namespace: default
spec:
  selector:
    matchLabels:
      app: httpbin
`)

	event := types.Event{
		Type:    types.EventAdd,
		Key:     "default/authorizationpolicy/deny-all",
		NewData: yamlData,
		Metadata: map[string]string{
			"kind":      "AuthorizationPolicy",
			"name":      "deny-all",
			"namespace": "default",
		},
	}

	if err := updater.Update(context.Background(), event); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// istioconfigs-side: istioconfigs/default/authorizationpolicy/correlation.json
	istioCorrPath := filepath.Join(istioDir, "default", "authorizationpolicy", "correlation.json")
	data, err := os.ReadFile(istioCorrPath)
	if err != nil {
		t.Fatalf("istioconfigs correlation.json not written: %v", err)
	}
	var corr correlation.IstioCorrelation
	if err := json.Unmarshal(data, &corr); err != nil {
		t.Fatalf("failed to parse istio correlation.json: %v", err)
	}
	entries, ok := corr["deny-all"]
	if !ok || len(entries) == 0 {
		t.Fatalf("expected entries for 'deny-all', got: %v", corr)
	}
	if entries[0].Pod != "httpbin-abc123" {
		t.Errorf("expected pod httpbin-abc123, got %s", entries[0].Pod)
	}
	if entries[0].ConfigType != "listener" {
		t.Errorf("expected config_type listener, got %s", entries[0].ConfigType)
	}

	// envoyconfigs-side: envoyconfigs/<ns>/<pod>/authorizationpolicy-correlation.json
	envoyCorrPath := filepath.Join(envoyDir, "default", "httpbin-abc123", "authorizationpolicy-correlation.json")
	envoyData, err := os.ReadFile(envoyCorrPath)
	if err != nil {
		t.Fatalf("envoyconfigs authorizationpolicy-correlation.json not written: %v", err)
	}
	var pc correlation.PodCorrelation
	if err := json.Unmarshal(envoyData, &pc); err != nil {
		t.Fatalf("failed to parse pod correlation: %v", err)
	}
	refKey := "AuthorizationPolicy/default/deny-all"
	affected, ok := pc.AffectedBy[refKey]
	if !ok {
		t.Fatalf("expected key %q in affected_by, got: %v", refKey, pc.AffectedBy)
	}
	if len(affected) != 2 {
		t.Errorf("expected 2 affected listeners, got %d: %+v", len(affected), affected)
	}
	names := map[string]bool{}
	for _, a := range affected {
		names[a.Name] = true
		if a.Type != "listener" {
			t.Errorf("type: got %q, want listener", a.Type)
		}
	}
	if !names["0.0.0.0_8080"] || !names["0.0.0.0_15006"] {
		t.Errorf("missing listener names: %v", names)
	}
}

func TestSelectorCorrelation_SkipsEmptySelector(t *testing.T) {
	istioDir := t.TempDir()
	envoyDir := t.TempDir()

	lister := &mockPodLister{pods: []string{"should-not-appear"}}
	updater := NewCurrentFileUpdater(istioDir).WithSelectorCorrelation(lister, envoyDir)

	// No spec.selector.matchLabels → should skip correlation
	yamlData := []byte(`apiVersion: security.istio.io/v1beta1
kind: AuthorizationPolicy
metadata:
  name: allow-all
  namespace: default
spec: {}
`)

	event := types.Event{
		Type:    types.EventAdd,
		Key:     "default/authorizationpolicy/allow-all",
		NewData: yamlData,
		Metadata: map[string]string{
			"kind":      "AuthorizationPolicy",
			"name":      "allow-all",
			"namespace": "default",
		},
	}

	if err := updater.Update(context.Background(), event); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	envoyCorrPath := filepath.Join(envoyDir, "default", "should-not-appear", "authorizationpolicy-correlation.json")
	if _, err := os.Stat(envoyCorrPath); !os.IsNotExist(err) {
		t.Error("authorizationpolicy-correlation.json should not be written for empty selector")
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
