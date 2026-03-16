package envoy

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/revolyssup/sync-envoy/pkg/diff"
	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/types"
)

// FileUpdater writes Envoy configuration JSON files to the envoyconfigs directory.
type FileUpdater struct {
	baseDir     string
	lastWritten map[string][]byte
	ignorePaths []string
	mu          sync.Mutex
	showDiff    bool
}

func NewFileUpdater(baseDir string, ignorePaths ...string) *FileUpdater {
	return &FileUpdater{
		baseDir:     baseDir,
		lastWritten: make(map[string][]byte),
		ignorePaths: ignorePaths,
	}
}

// WithShowDiff enables printing unified diffs when changes are detected.
func (u *FileUpdater) WithShowDiff(show bool) *FileUpdater {
	u.showDiff = show
	return u
}

func (u *FileUpdater) Name() string { return "envoy-file-updater" }

func (u *FileUpdater) Update(ctx context.Context, event types.Event) error {
	podName := event.Metadata["pod_name"]
	namespace := event.Metadata["namespace"]
	configType := event.Metadata["config_type"]

	podDir := filepath.Join(u.baseDir, namespace, podName)
	os.MkdirAll(podDir, 0755)

	filePath := filepath.Join(podDir, configType+".json")

	// Diff check
	u.mu.Lock()
	lastData, exists := u.lastWritten[event.Key]
	u.mu.Unlock()

	if exists {
		d := diff.ComputeJSON(lastData, event.NewData, u.ignorePaths)
		if d == "" {
			logging.Debug("No diff for envoy config %s, skipping write", filePath)
			return nil
		}
		if u.showDiff {
			logging.Info("Diff detected for envoy config %s:\n%s", filePath, d)
		} else {
			logging.Debug("Diff detected for envoy config %s:\n%s", filePath, d)
		}
	}

	if err := os.WriteFile(filePath, event.NewData, 0644); err != nil {
		return err
	}

	u.mu.Lock()
	u.lastWritten[event.Key] = event.NewData
	u.mu.Unlock()

	logging.Debug("Written Envoy config %s for %s/%s", configType, namespace, podName)

	return nil
}
