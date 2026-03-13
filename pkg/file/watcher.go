package file

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"sync-envoy/pkg/logging"
	"sync-envoy/pkg/types"
)

// DesiredFileWatcher watches for _desired.yaml files in istioconfigs/.
// When a plain .yaml file is created (without _current or _desired suffix),
// it is automatically renamed to _desired.yaml and an event is emitted.
type DesiredFileWatcher struct {
	basePath string
}

func NewDesiredFileWatcher(basePath string) *DesiredFileWatcher {
	return &DesiredFileWatcher{basePath: basePath}
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

	// Watch the entire directory tree
	err = filepath.Walk(w.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return watcher.Add(path)
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
							w.emitEvent(fsnotify.Event{Name: p, Op: fsnotify.Create}, events)
						}
						return nil
					})
					continue
				}
			}

			if !strings.HasSuffix(event.Name, ".yaml") {
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
					w.emitEvent(pe, events)
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

func (w *DesiredFileWatcher) emitEvent(event fsnotify.Event, events chan<- types.Event) {
	data, err := os.ReadFile(event.Name)
	if err != nil {
		logging.Errorf("Failed to read %s: %v", event.Name, err)
		return
	}

	relPath, err := filepath.Rel(w.basePath, event.Name)
	if err != nil {
		logging.Errorf("Failed to get relative path for %s: %v", event.Name, err)
		return
	}

	events <- types.Event{
		Type:    types.EventUpdate,
		Key:     relPath,
		NewData: data,
		Metadata: map[string]string{
			"file_path": event.Name,
			"rel_path":  relPath,
		},
	}
}
