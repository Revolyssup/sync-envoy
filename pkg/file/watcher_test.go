package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/revolyssup/sync-envoy/pkg/types"
)

func TestDesiredFileWatcher_AutoRename(t *testing.T) {
	tmpDir := t.TempDir()
	istioDir := filepath.Join(tmpDir, "istioconfigs")
	kindDir := filepath.Join(istioDir, "default", "virtualservice")
	os.MkdirAll(kindDir, 0755)

	watcher := NewDesiredFileWatcher(istioDir)
	events := make(chan types.Event, 10)
	ctx, cancel := context.WithCancel(context.Background())

	go watcher.Watch(ctx, events)

	// Wait for watcher to start
	time.Sleep(200 * time.Millisecond)

	// Create a plain .yaml file - should be auto-renamed to _desired.yaml
	plainPath := filepath.Join(kindDir, "httpbin.yaml")
	content := []byte(`apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: httpbin
  namespace: default
spec:
  hosts:
  - httpbin
`)
	if err := os.WriteFile(plainPath, content, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Wait for debounce + processing
	select {
	case event := <-events:
		if event.Type != types.EventUpdate {
			t.Errorf("expected EventUpdate, got %v", event.Type)
		}
		if len(event.NewData) == 0 {
			t.Error("expected non-empty NewData")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Verify the file was renamed
	desiredPath := filepath.Join(kindDir, "httpbin_desired.yaml")
	if _, err := os.Stat(desiredPath); os.IsNotExist(err) {
		t.Error("expected _desired.yaml file to exist after auto-rename")
	}
	if _, err := os.Stat(plainPath); !os.IsNotExist(err) {
		t.Error("expected original plain .yaml to be gone after rename")
	}

	cancel()
}

func TestDesiredFileWatcher_SkipsCurrentFiles(t *testing.T) {
	tmpDir := t.TempDir()
	istioDir := filepath.Join(tmpDir, "istioconfigs")
	kindDir := filepath.Join(istioDir, "default", "virtualservice")
	os.MkdirAll(kindDir, 0755)

	watcher := NewDesiredFileWatcher(istioDir)
	events := make(chan types.Event, 10)
	ctx, cancel := context.WithCancel(context.Background())

	go watcher.Watch(ctx, events)
	time.Sleep(200 * time.Millisecond)

	// Write a _current.yaml file - should be ignored
	currentPath := filepath.Join(kindDir, "httpbin_current.yaml")
	if err := os.WriteFile(currentPath, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	select {
	case <-events:
		t.Error("should not receive event for _current.yaml file")
	case <-time.After(1 * time.Second):
		// Expected - no event
	}

	cancel()
}

func TestDesiredFileWatcher_ProcessesDesiredFiles(t *testing.T) {
	tmpDir := t.TempDir()
	istioDir := filepath.Join(tmpDir, "istioconfigs")
	kindDir := filepath.Join(istioDir, "default", "destinationrule")
	os.MkdirAll(kindDir, 0755)

	watcher := NewDesiredFileWatcher(istioDir)
	events := make(chan types.Event, 10)
	ctx, cancel := context.WithCancel(context.Background())

	go watcher.Watch(ctx, events)
	time.Sleep(200 * time.Millisecond)

	// Write directly as _desired.yaml
	desiredPath := filepath.Join(kindDir, "httpbin_desired.yaml")
	content := []byte(`apiVersion: networking.istio.io/v1
kind: DestinationRule
metadata:
  name: httpbin
  namespace: default
spec:
  host: httpbin
`)
	if err := os.WriteFile(desiredPath, content, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	select {
	case event := <-events:
		if event.Type != types.EventUpdate {
			t.Errorf("expected EventUpdate, got %v", event.Type)
		}
		expectedRel := filepath.Join("default", "destinationrule", "httpbin_desired.yaml")
		if event.Key != expectedRel {
			t.Errorf("event key = %q, want %q", event.Key, expectedRel)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	cancel()
}
