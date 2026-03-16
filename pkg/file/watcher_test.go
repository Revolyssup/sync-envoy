package file

import (
	"bytes"
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

// TestDesiredFileWatcher_StartupEmitsPreExisting verifies that _desired.yaml
// files that already exist on disk when the watcher starts are emitted as
// EventUpdate so CRUpdater applies them (e.g. resource deleted from cluster
// while tool was stopped).
func TestDesiredFileWatcher_StartupEmitsPreExisting(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "configs")
	kindDir := filepath.Join(baseDir, "istio-system", "serviceroute")
	os.MkdirAll(kindDir, 0755)

	content := []byte(`apiVersion: traffic.xcp.tetrate.io/v2
kind: ServiceRoute
metadata:
  name: echo-service-route
  namespace: istio-system
spec:
  service: echo.echo.svc.cluster.local
`)
	desiredPath := filepath.Join(kindDir, "echo-service-route_desired.yaml")
	if err := os.WriteFile(desiredPath, content, 0644); err != nil {
		t.Fatalf("Failed to write desired file: %v", err)
	}

	// No _current.yaml exists — simulates resource deleted from cluster

	watcher := NewDesiredFileWatcher(baseDir)
	events := make(chan types.Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watcher.Watch(ctx, events)

	select {
	case event := <-events:
		if event.Type != types.EventUpdate {
			t.Errorf("expected EventUpdate, got %v", event.Type)
		}
		expectedRel := filepath.Join("istio-system", "serviceroute", "echo-service-route_desired.yaml")
		if event.Key != expectedRel {
			t.Errorf("event key = %q, want %q", event.Key, expectedRel)
		}
		if !bytes.Equal(event.NewData, content) {
			t.Errorf("event data mismatch")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for startup event — pre-existing _desired.yaml was not emitted")
	}
}

// TestDesiredFileWatcher_StartupEmitsEvenWhenCurrentMatches verifies that the
// startup walk always emits _desired.yaml, bypassing the equality check that
// live events use. This handles the case where the resource was deleted from
// the cluster but _current.yaml still has matching content on disk.
func TestDesiredFileWatcher_StartupEmitsEvenWhenCurrentMatches(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "configs")
	kindDir := filepath.Join(baseDir, "default", "virtualservice")
	os.MkdirAll(kindDir, 0755)

	content := []byte(`apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: httpbin
  namespace: default
spec:
  hosts:
  - httpbin
`)
	// Write both _current and _desired with IDENTICAL content
	os.WriteFile(filepath.Join(kindDir, "httpbin_current.yaml"), content, 0644)
	os.WriteFile(filepath.Join(kindDir, "httpbin_desired.yaml"), content, 0644)

	watcher := NewDesiredFileWatcher(baseDir)
	events := make(chan types.Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watcher.Watch(ctx, events)

	// Startup walk should emit even though current == desired
	select {
	case event := <-events:
		if event.Type != types.EventUpdate {
			t.Errorf("expected EventUpdate, got %v", event.Type)
		}
		if !bytes.Equal(event.NewData, content) {
			t.Errorf("event data mismatch")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out — startup walk should emit _desired.yaml even when it matches _current.yaml")
	}
}

// TestDesiredFileWatcher_LiveSkipsWhenCurrentMatches verifies that during live
// operation (after startup), emitEvent skips events where _desired == _current
// to avoid unnecessary reconciliation.
func TestDesiredFileWatcher_LiveSkipsWhenCurrentMatches(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "configs")
	kindDir := filepath.Join(baseDir, "default", "gateway")
	os.MkdirAll(kindDir, 0755)

	content := []byte(`apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: my-gw
  namespace: default
`)
	// Write _current first (before watcher starts, so it's already on disk)
	os.WriteFile(filepath.Join(kindDir, "my-gw_current.yaml"), content, 0644)

	watcher := NewDesiredFileWatcher(baseDir)
	events := make(chan types.Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watcher.Watch(ctx, events)
	time.Sleep(300 * time.Millisecond)

	// Now write _desired with identical content — live emitEvent should skip
	os.WriteFile(filepath.Join(kindDir, "my-gw_desired.yaml"), content, 0644)

	select {
	case <-events:
		t.Error("should NOT emit event when live _desired.yaml matches _current.yaml")
	case <-time.After(2 * time.Second):
		// Expected — skipped
	}
}

// TestDesiredFileWatcher_DeleteEmitsEventDelete verifies that removing a
// _desired.yaml file emits an EventDelete with OldData from _current.yaml.
func TestDesiredFileWatcher_DeleteEmitsEventDelete(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "configs")
	kindDir := filepath.Join(baseDir, "default", "virtualservice")
	os.MkdirAll(kindDir, 0755)

	content := []byte(`apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: httpbin
  namespace: default
spec:
  hosts:
  - httpbin
`)
	currentPath := filepath.Join(kindDir, "httpbin_current.yaml")
	desiredPath := filepath.Join(kindDir, "httpbin_desired.yaml")
	os.WriteFile(currentPath, content, 0644)
	os.WriteFile(desiredPath, content, 0644)

	watcher := NewDesiredFileWatcher(baseDir)
	events := make(chan types.Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watcher.Watch(ctx, events)

	// Drain the startup event (startup walk emits pre-existing _desired)
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for startup event")
	}

	// Now delete _desired.yaml
	if err := os.Remove(desiredPath); err != nil {
		t.Fatalf("Failed to remove desired file: %v", err)
	}

	select {
	case event := <-events:
		if event.Type != types.EventDelete {
			t.Errorf("expected EventDelete, got %v", event.Type)
		}
		expectedRel := filepath.Join("default", "virtualservice", "httpbin_desired.yaml")
		if event.Key != expectedRel {
			t.Errorf("event key = %q, want %q", event.Key, expectedRel)
		}
		if !bytes.Equal(event.OldData, content) {
			t.Errorf("OldData should contain _current.yaml content for GVK parsing")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for delete event")
	}
}

// TestDesiredFileWatcher_StartupMultipleFiles verifies that multiple
// pre-existing _desired.yaml files across different directories are all
// emitted at startup.
func TestDesiredFileWatcher_StartupMultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "configs")

	files := map[string][]byte{
		filepath.Join(baseDir, "ns1", "virtualservice", "vs1_desired.yaml"): []byte("vs1"),
		filepath.Join(baseDir, "ns1", "gateway", "gw1_desired.yaml"):       []byte("gw1"),
		filepath.Join(baseDir, "ns2", "serviceroute", "sr1_desired.yaml"):  []byte("sr1"),
	}

	for path, content := range files {
		os.MkdirAll(filepath.Dir(path), 0755)
		os.WriteFile(path, content, 0644)
	}

	watcher := NewDesiredFileWatcher(baseDir)
	events := make(chan types.Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watcher.Watch(ctx, events)

	received := make(map[string]bool)
	timeout := time.After(3 * time.Second)
	for i := 0; i < len(files); i++ {
		select {
		case event := <-events:
			if event.Type != types.EventUpdate {
				t.Errorf("expected EventUpdate, got %v", event.Type)
			}
			received[event.Key] = true
		case <-timeout:
			t.Fatalf("timed out after receiving %d of %d events", i, len(files))
		}
	}

	for path := range files {
		rel, _ := filepath.Rel(baseDir, path)
		if !received[rel] {
			t.Errorf("missing startup event for %s", rel)
		}
	}
}
