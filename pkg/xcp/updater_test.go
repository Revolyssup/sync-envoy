package xcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/revolyssup/sync-envoy/pkg/types"
)

func TestXCPFileUpdater_WriteNamespaced(t *testing.T) {
	xcpDir := t.TempDir()
	istioDir := t.TempDir()

	updater := NewXCPFileUpdater(xcpDir, istioDir, nil)

	event := types.Event{
		Type:    types.EventAdd,
		Key:     "default/serviceroute/foo",
		NewData: []byte("apiVersion: traffic.xcp.tetrate.io/v2\nkind: ServiceRoute\n"),
		Metadata: map[string]string{
			"kind":      "ServiceRoute",
			"name":      "foo",
			"namespace": "default",
		},
	}

	if err := updater.Update(context.Background(), event); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	path := filepath.Join(xcpDir, "default", "serviceroute", "foo_current.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(data) != string(event.NewData) {
		t.Errorf("content mismatch")
	}
}

func TestXCPFileUpdater_SkipNoDiff(t *testing.T) {
	xcpDir := t.TempDir()
	istioDir := t.TempDir()
	updater := NewXCPFileUpdater(xcpDir, istioDir, nil)

	event := types.Event{
		Type:    types.EventUpdate,
		Key:     "default/trafficsetting/ts1",
		NewData: []byte("kind: TrafficSetting\n"),
		Metadata: map[string]string{
			"kind":      "TrafficSetting",
			"name":      "ts1",
			"namespace": "default",
		},
	}

	updater.Update(context.Background(), event)

	// Modify file on disk to verify skip
	path := filepath.Join(xcpDir, "default", "trafficsetting", "ts1_current.yaml")
	os.WriteFile(path, []byte("modified"), 0644)

	// Same data → should skip
	updater.Update(context.Background(), event)

	data, _ := os.ReadFile(path)
	if string(data) != "modified" {
		t.Errorf("expected file to remain modified (no-diff skip), got: %q", string(data))
	}
}

func TestXCPFileUpdater_Delete(t *testing.T) {
	xcpDir := t.TempDir()
	istioDir := t.TempDir()
	updater := NewXCPFileUpdater(xcpDir, istioDir, nil)

	// Write first
	addEvent := types.Event{
		Type:    types.EventAdd,
		Key:     "default/serviceroute/foo",
		NewData: []byte("kind: ServiceRoute\n"),
		Metadata: map[string]string{
			"kind":      "ServiceRoute",
			"name":      "foo",
			"namespace": "default",
		},
	}
	updater.Update(context.Background(), addEvent)

	path := filepath.Join(xcpDir, "default", "serviceroute", "foo_current.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist after add: %v", err)
	}

	// Delete
	deleteEvent := types.Event{
		Type: types.EventDelete,
		Key:  "default/serviceroute/foo",
		Metadata: map[string]string{
			"kind":      "ServiceRoute",
			"name":      "foo",
			"namespace": "default",
		},
	}
	if err := updater.Update(context.Background(), deleteEvent); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be removed after delete")
	}
}

func TestBuildLabelSelector(t *testing.T) {
	labels := map[string]string{
		"xcp.tetrate.io/workspace":    "ws1",
		"xcp.tetrate.io/trafficGroup": "tg1",
		"unrelated":                   "ignored",
	}
	sel := buildLabelSelector(labels)
	if sel == "" {
		t.Fatal("expected non-empty selector")
	}
	// Should contain both XCP hierarchy labels, not the unrelated one
	if !contains(sel, "xcp.tetrate.io/workspace=ws1") {
		t.Errorf("selector missing workspace: %s", sel)
	}
	if !contains(sel, "xcp.tetrate.io/trafficGroup=tg1") {
		t.Errorf("selector missing trafficGroup: %s", sel)
	}
	if contains(sel, "unrelated") {
		t.Errorf("selector should not contain unrelated: %s", sel)
	}
}

func TestBuildLabelSelector_Empty(t *testing.T) {
	sel := buildLabelSelector(map[string]string{"random": "val"})
	if sel != "" {
		t.Errorf("expected empty selector for non-XCP labels, got %q", sel)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
