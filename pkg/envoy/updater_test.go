package envoy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/revolyssup/sync-envoy/pkg/correlation"
	"github.com/revolyssup/sync-envoy/pkg/types"
)

func TestFileUpdater_Write(t *testing.T) {
	tmpDir := t.TempDir()
	updater := NewFileUpdater(tmpDir)

	event := types.Event{
		Type:    types.EventUpdate,
		Key:     "default/httpbin-abc/cluster",
		NewData: []byte(`{"config": "test"}`),
		Metadata: map[string]string{
			"pod_name":    "httpbin-abc",
			"namespace":   "default",
			"config_type": "cluster",
		},
	}

	err := updater.Update(context.Background(), event)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, "default", "httpbin-abc", "cluster.json")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read written file: %v", err)
	}
	if string(data) != string(event.NewData) {
		t.Errorf("file content mismatch")
	}
}

func TestFileUpdater_SkipNoDiff(t *testing.T) {
	tmpDir := t.TempDir()
	updater := NewFileUpdater(tmpDir)

	event := types.Event{
		Type:    types.EventUpdate,
		Key:     "default/httpbin-abc/listener",
		NewData: []byte(`{"listeners": []}`),
		Metadata: map[string]string{
			"pod_name":    "httpbin-abc",
			"namespace":   "default",
			"config_type": "listener",
		},
	}

	if err := updater.Update(context.Background(), event); err != nil {
		t.Fatalf("First update failed: %v", err)
	}

	path := filepath.Join(tmpDir, "default", "httpbin-abc", "listener.json")
	os.WriteFile(path, []byte("modified"), 0644)

	if err := updater.Update(context.Background(), event); err != nil {
		t.Fatalf("Second update failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "modified" {
		t.Errorf("expected file to remain modified (no-diff skip), got: %q", string(data))
	}
}

func TestFileUpdater_SkipIgnoredPathChange(t *testing.T) {
	tmpDir := t.TempDir()
	updater := NewFileUpdater(tmpDir, "last_updated")

	base := `{"last_updated":"2026-03-13T10:00:00Z","config":"v1"}`
	event1 := types.Event{
		Type:    types.EventUpdate,
		Key:     "default/httpbin-abc/bootstrap",
		NewData: []byte(base),
		Metadata: map[string]string{
			"pod_name":    "httpbin-abc",
			"namespace":   "default",
			"config_type": "bootstrap",
		},
	}
	updater.Update(context.Background(), event1)

	onlyTimestampChanged := `{"last_updated":"2026-03-13T10:00:05Z","config":"v1"}`
	event2 := event1
	event2.NewData = []byte(onlyTimestampChanged)
	if err := updater.Update(context.Background(), event2); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	path := filepath.Join(tmpDir, "default", "httpbin-abc", "bootstrap.json")
	data, _ := os.ReadFile(path)
	if string(data) != base {
		t.Errorf("expected file unchanged (only timestamp differed), got: %q", string(data))
	}
}

func TestFileUpdater_WritesCorrelationJSON(t *testing.T) {
	tmpDir := t.TempDir()
	updater := NewFileUpdater(tmpDir)

	clusterDump := `{
		"dynamic_active_clusters": [{
			"cluster": {
				"name": "outbound|9080|v1|reviews.default.svc.cluster.local",
				"metadata": {"filter_metadata": {"istio": {
					"config": "/apis/networking.istio.io/v1alpha3/namespaces/default/virtual-service/reviews"
				}}}
			}
		}]
	}`
	tc := TimestampedConfig{
		PodName:    "httpbin-abc",
		Namespace:  "default",
		ConfigType: "cluster",
		Config:     json.RawMessage(clusterDump),
	}
	data, _ := json.Marshal(tc)

	event := types.Event{
		Type:    types.EventUpdate,
		Key:     "default/httpbin-abc/cluster",
		NewData: data,
		Metadata: map[string]string{
			"pod_name":    "httpbin-abc",
			"namespace":   "default",
			"config_type": "cluster",
		},
	}
	if err := updater.Update(context.Background(), event); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	corrPath := filepath.Join(tmpDir, "default", "httpbin-abc", "destinationrule-correlation.json")
	raw, err := os.ReadFile(corrPath)
	if err != nil {
		t.Fatalf("destinationrule-correlation.json not written: %v", err)
	}

	var pc correlation.PodCorrelation
	if err := json.Unmarshal(raw, &pc); err != nil {
		t.Fatalf("failed to parse correlation: %v", err)
	}
	if pc.Pod != "httpbin-abc" {
		t.Errorf("pod: got %q, want httpbin-abc", pc.Pod)
	}

	// The VS ref should have 1 affected cluster
	vsKey := "VirtualService/default/reviews"
	clusters, ok := pc.AffectedBy[vsKey]
	if !ok {
		t.Fatalf("expected key %q in affected_by, got keys: %v", vsKey, pc.AffectedBy)
	}
	if len(clusters) != 1 {
		t.Fatalf("expected 1 affected cluster, got %d", len(clusters))
	}
	if clusters[0].Type != "cluster" {
		t.Errorf("type: got %q, want cluster", clusters[0].Type)
	}
	if clusters[0].Service != "reviews" {
		t.Errorf("service: got %q, want reviews", clusters[0].Service)
	}
	if clusters[0].Subset != "v1" {
		t.Errorf("subset: got %q, want v1", clusters[0].Subset)
	}
}

func TestFileUpdater_WritesDiffDetected(t *testing.T) {
	tmpDir := t.TempDir()
	updater := NewFileUpdater(tmpDir)

	event1 := types.Event{
		Type:    types.EventUpdate,
		Key:     "default/httpbin-abc/route",
		NewData: []byte(`{"routes": "v1"}`),
		Metadata: map[string]string{
			"pod_name":    "httpbin-abc",
			"namespace":   "default",
			"config_type": "route",
		},
	}
	updater.Update(context.Background(), event1)

	event2 := types.Event{
		Type:    types.EventUpdate,
		Key:     "default/httpbin-abc/route",
		NewData: []byte(`{"routes": "v2"}`),
		Metadata: map[string]string{
			"pod_name":    "httpbin-abc",
			"namespace":   "default",
			"config_type": "route",
		},
	}
	if err := updater.Update(context.Background(), event2); err != nil {
		t.Fatalf("Update with diff failed: %v", err)
	}

	path := filepath.Join(tmpDir, "default", "httpbin-abc", "route.json")
	data, _ := os.ReadFile(path)
	if string(data) != `{"routes": "v2"}` {
		t.Errorf("expected updated content, got: %q", string(data))
	}
}
