package types

import "context"

// EventType represents the kind of change detected.
type EventType int

const (
	EventAdd EventType = iota
	EventUpdate
	EventDelete
)

func (e EventType) String() string {
	switch e {
	case EventAdd:
		return "ADD"
	case EventUpdate:
		return "UPDATE"
	case EventDelete:
		return "DELETE"
	default:
		return "UNKNOWN"
	}
}

// Event represents a change detected by a Watcher.
type Event struct {
	Type     EventType
	Key      string            // unique key for the resource (e.g., "namespace/kind/name")
	OldData  []byte            // previous data (for diff calculation)
	NewData  []byte            // new data
	Metadata map[string]string // arbitrary metadata (e.g., kind, name, namespace, file_path)
}

// Watcher detects changes and produces events on a channel.
type Watcher interface {
	// Watch starts watching and sends events to the channel.
	// It blocks until ctx is cancelled or an unrecoverable error occurs.
	Watch(ctx context.Context, events chan<- Event) error
	Name() string
}

// Updater consumes events and applies changes to a target.
type Updater interface {
	// Update processes a single event. Returns an error if the update fails.
	Update(ctx context.Context, event Event) error
	Name() string
}
