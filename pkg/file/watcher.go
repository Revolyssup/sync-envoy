package file

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/types"
)

// DesiredFileWatcher watches for _desired.yaml files in istioconfigs/.
// When a plain .yaml file is created (without _current or _desired suffix),
// it is automatically renamed to _desired.yaml and an event is emitted.
type DesiredFileWatcher struct {
	basePath       string
	applyOnStartup bool
}

func NewDesiredFileWatcher(basePath string) *DesiredFileWatcher {
	return &DesiredFileWatcher{basePath: basePath, applyOnStartup: true}
}

// WithApplyOnStartup controls whether pre-existing _desired.yaml files are
// emitted at startup. Set to false to skip the startup walk.
func (w *DesiredFileWatcher) WithApplyOnStartup(apply bool) *DesiredFileWatcher {
	w.applyOnStartup = apply
	return w
}

func (w *DesiredFileWatcher) Name() string { return "file-watcher" }

func (w *DesiredFileWatcher) Watch(ctx context.Context, events chan<- types.Event) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer watcher.Close()

	// Ensure base path exists
	os.MkdirAll(w.basePath, 0755)

	// Watch the entire directory tree and optionally emit pre-existing _desired.yaml files.
	// When applyOnStartup is true, bypass the equality check used for live events —
	// CRUpdater will diff against cluster state, so we always attempt to apply on
	// startup (handles the case where the cluster resource was deleted while the tool
	// wasn't running).
	err = filepath.Walk(w.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return watcher.Add(path)
		}
		if w.applyOnStartup && strings.HasSuffix(path, "_desired.yaml") {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				logging.Errorf("Failed to read %s: %v", path, readErr)
				return nil
			}
			relPath, relErr := filepath.Rel(w.basePath, path)
			if relErr != nil {
				logging.Errorf("Failed to get relative path for %s: %v", path, relErr)
				return nil
			}
			select {
			case events <- types.Event{
				Type:    types.EventUpdate,
				Key:     relPath,
				NewData: data,
				Metadata: map[string]string{
					"file_path": path,
					"rel_path":  relPath,
				},
			}:
			case <-ctx.Done():
				return fmt.Errorf("context cancelled")
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to add watches: %w", err)
	}

	logging.Info("File watcher started on %s (watching _desired.yaml files)", w.basePath)

	var debounceTimer *time.Timer
	const debounceDelay = 500 * time.Millisecond
	pendingEvents := make(map[string]fsnotify.Event)

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			// Watch newly created directories (recursively, to handle MkdirAll).
			// Also emit events for any _desired.yaml files already inside the new tree,
			// since their Create events may have fired before we added the directory.
			if event.Op&fsnotify.Create != 0 {
				info, statErr := os.Stat(event.Name)
				if statErr == nil && info.IsDir() {
					filepath.Walk(event.Name, func(p string, fi os.FileInfo, err error) error {
						if err != nil {
							return nil
						}
						if fi.IsDir() {
							watcher.Add(p)
							return nil
						}
						if strings.HasSuffix(p, "_desired.yaml") {
							w.emitEvent(ctx, fsnotify.Event{Name: p, Op: fsnotify.Create}, events)
						}
						return nil
					})
					continue
				}
			}

			if !strings.HasSuffix(event.Name, ".yaml") {
				continue
			}

			// Handle deletions of _desired.yaml files immediately (no debounce needed).
			if event.Op&fsnotify.Remove != 0 {
				if strings.HasSuffix(event.Name, "_desired.yaml") {
					w.emitDeleteEvent(ctx, event, events)
				}
				continue
			}

			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			// Skip _current files - those are written by the kubernetes provider
			if strings.HasSuffix(filepath.Base(event.Name), "_current.yaml") {
				continue
			}

			targetPath := event.Name

			// Auto-rename: plain .yaml → _desired.yaml
			base := filepath.Base(event.Name)
			if !strings.HasSuffix(base, "_desired.yaml") {
				desiredPath := strings.TrimSuffix(event.Name, ".yaml") + "_desired.yaml"
				if err := os.Rename(event.Name, desiredPath); err != nil {
					logging.Errorf("Failed to rename %s to %s: %v", event.Name, desiredPath, err)
					continue
				}
				logging.Info("Auto-renamed %s -> %s", event.Name, desiredPath)
				targetPath = desiredPath
			}

			logging.Debug("File event: %s %s", event.Op, targetPath)

			// Debounce: accumulate events per file
			pendingEvents[targetPath] = fsnotify.Event{Name: targetPath, Op: event.Op}

			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			// Capture current pending events for the closure
			pending := make(map[string]fsnotify.Event)
			for k, v := range pendingEvents {
				pending[k] = v
			}
			debounceTimer = time.AfterFunc(debounceDelay, func() {
				for _, pe := range pending {
					w.emitEvent(ctx, pe, events)
				}
			})
			// Clear pending after scheduling
			pendingEvents = make(map[string]fsnotify.Event)

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logging.Errorf("File watcher error: %v", err)
		}
	}
}

// emitDeleteEvent emits an EventDelete for a removed _desired.yaml.
// It reads the companion _current.yaml (if present) so the updater can parse the GVK for cluster deletion.
func (w *DesiredFileWatcher) emitDeleteEvent(ctx context.Context, event fsnotify.Event, events chan<- types.Event) {
	relPath, err := filepath.Rel(w.basePath, event.Name)
	if err != nil {
		logging.Errorf("Failed to get relative path for %s: %v", event.Name, err)
		return
	}

	// Read companion _current.yaml so the updater has GVK metadata for cluster deletion.
	currentPath := strings.TrimSuffix(event.Name, "_desired.yaml") + "_current.yaml"
	currentData, _ := os.ReadFile(currentPath)

	logging.Info("Desired file deleted: %s — will delete resource from cluster", relPath)

	select {
	case events <- types.Event{
		Type:    types.EventDelete,
		Key:     relPath,
		OldData: currentData,
		Metadata: map[string]string{
			"file_path": event.Name,
			"rel_path":  relPath,
		},
	}:
	case <-ctx.Done():
	}
}

func (w *DesiredFileWatcher) emitEvent(ctx context.Context, event fsnotify.Event, events chan<- types.Event) {
	data, err := os.ReadFile(event.Name)
	if err != nil {
		logging.Errorf("Failed to read %s: %v", event.Name, err)
		return
	}

	// Skip if _desired.yaml content is identical to _current.yaml — no user changes.
	currentPath := strings.TrimSuffix(event.Name, "_desired.yaml") + "_current.yaml"
	if currentData, err := os.ReadFile(currentPath); err == nil {
		if bytes.Equal(data, currentData) {
			logging.Debug("Desired matches current, skipping: %s", event.Name)
			return
		}
	}

	relPath, err := filepath.Rel(w.basePath, event.Name)
	if err != nil {
		logging.Errorf("Failed to get relative path for %s: %v", event.Name, err)
		return
	}

	select {
	case events <- types.Event{
		Type:    types.EventUpdate,
		Key:     relPath,
		NewData: data,
		Metadata: map[string]string{
			"file_path": event.Name,
			"rel_path":  relPath,
		},
	}:
	case <-ctx.Done():
	}
}
