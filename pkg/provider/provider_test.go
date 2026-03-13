package provider

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/revolyssup/sync-envoy/pkg/types"
)

// mockWatcher implements types.Watcher for testing.
type mockWatcher struct {
	name   string
	events []types.Event
}

func (w *mockWatcher) Name() string { return w.name }

func (w *mockWatcher) Watch(ctx context.Context, events chan<- types.Event) error {
	for _, e := range w.events {
		select {
		case events <- e:
		case <-ctx.Done():
			return nil
		}
	}
	<-ctx.Done()
	return nil
}

// mockUpdater implements types.Updater for testing.
type mockUpdater struct {
	name     string
	received []types.Event
	mu       sync.Mutex
}

func (u *mockUpdater) Name() string { return u.name }

func (u *mockUpdater) Update(ctx context.Context, event types.Event) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.received = append(u.received, event)
	return nil
}

func (u *mockUpdater) Received() []types.Event {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]types.Event{}, u.received...)
}

func TestProvider_Run(t *testing.T) {
	events := []types.Event{
		{Type: types.EventAdd, Key: "test/1", NewData: []byte("data1")},
		{Type: types.EventUpdate, Key: "test/2", NewData: []byte("data2")},
	}

	watcher := &mockWatcher{name: "test-watcher", events: events}
	updater := &mockUpdater{name: "test-updater"}

	p := New("test-provider", watcher, updater)
	if p.Name() != "test-provider" {
		t.Errorf("Name() = %q, want test-provider", p.Name())
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// Wait for events to be processed
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	p.Run(ctx)

	received := updater.Received()
	if len(received) != 2 {
		t.Fatalf("updater received %d events, want 2", len(received))
	}
	if received[0].Key != "test/1" {
		t.Errorf("first event key = %q, want test/1", received[0].Key)
	}
	if received[1].Key != "test/2" {
		t.Errorf("second event key = %q, want test/2", received[1].Key)
	}
}

func TestRegistry_GetAll(t *testing.T) {
	reg := NewRegistry()
	reg.Register(New("a", &mockWatcher{name: "a"}, &mockUpdater{name: "a"}))
	reg.Register(New("b", &mockWatcher{name: "b"}, &mockUpdater{name: "b"}))

	providers, err := reg.Get("")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if len(providers) != 2 {
		t.Errorf("got %d providers, want 2", len(providers))
	}
}

func TestRegistry_GetFiltered(t *testing.T) {
	reg := NewRegistry()
	reg.Register(New("kubernetes", &mockWatcher{}, &mockUpdater{}))
	reg.Register(New("file", &mockWatcher{}, &mockUpdater{}))
	reg.Register(New("envoy", &mockWatcher{}, &mockUpdater{}))

	providers, err := reg.Get("kubernetes,envoy")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if len(providers) != 2 {
		t.Errorf("got %d providers, want 2", len(providers))
	}

	names := make(map[string]bool)
	for _, p := range providers {
		names[p.Name()] = true
	}
	if !names["kubernetes"] {
		t.Error("expected kubernetes provider")
	}
	if !names["envoy"] {
		t.Error("expected envoy provider")
	}
	if names["file"] {
		t.Error("file provider should not be included")
	}
}

func TestRegistry_GetUnknown(t *testing.T) {
	reg := NewRegistry()
	reg.Register(New("kubernetes", &mockWatcher{}, &mockUpdater{}))

	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestRunAll(t *testing.T) {
	watcher := &mockWatcher{
		name: "test",
		events: []types.Event{
			{Type: types.EventAdd, Key: "key1"},
		},
	}
	updater := &mockUpdater{name: "test"}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	providers := []*Provider{New("test", watcher, updater)}
	RunAll(ctx, providers)

	received := updater.Received()
	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
}
