package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/revolyssup/sync-envoy/pkg/diff"
	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/topology"
	"github.com/revolyssup/sync-envoy/pkg/types"
)

// CurrentFileUpdater writes _current.yaml files to the istioconfigs directory.
// It is the updater for the kubernetes provider, writing CR state from the cluster.
type CurrentFileUpdater struct {
	basePath    string
	lastWritten map[string][]byte
	mu          sync.Mutex
	topology    *topology.File
	showDiff    bool
}

func NewCurrentFileUpdater(basePath string) *CurrentFileUpdater {
	return &CurrentFileUpdater{
		basePath:    basePath,
		lastWritten: make(map[string][]byte),
	}
}

// WithTopology enables writing topology.md for Istio resource relationships.
func (u *CurrentFileUpdater) WithTopology(topo *topology.File) *CurrentFileUpdater {
	u.topology = topo
	return u
}

// WithShowDiff enables printing unified diffs when changes are detected.
func (u *CurrentFileUpdater) WithShowDiff(show bool) *CurrentFileUpdater {
	u.showDiff = show
	return u
}

func (u *CurrentFileUpdater) Name() string { return "file-updater" }

func (u *CurrentFileUpdater) Update(ctx context.Context, event types.Event) error {
	kind := strings.ToLower(event.Metadata["kind"])
	name := event.Metadata["name"]
	ns := event.Metadata["namespace"]

	var path string
	if ns == "" {
		path = filepath.Join(u.basePath, kind, name+"_current.yaml")
	} else {
		path = filepath.Join(u.basePath, ns, kind, name+"_current.yaml")
	}

	if event.Type == types.EventDelete {
		u.mu.Lock()
		delete(u.lastWritten, event.Key)
		u.mu.Unlock()

		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		logging.Debug("Deleted file: %s", path)

		if u.topology != nil {
			u.topology.Remove(ns, kind, name)
		}
		return nil
	}

	// Diff check against last written content
	u.mu.Lock()
	lastData, exists := u.lastWritten[event.Key]
	u.mu.Unlock()

	if exists {
		d := diff.Compute(lastData, event.NewData)
		if d == "" {
			logging.Debug("No diff for %s, skipping write", path)
			return nil
		}
		if u.showDiff {
			logging.Info("Diff detected for %s:\n%s", path, d)
		} else {
			logging.Debug("Diff detected for %s:\n%s", path, d)
		}
	}

	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, event.NewData, 0644); err != nil {
		return err
	}

	// Seed _desired.yaml if it doesn't exist yet, so users can edit it directly.
	desiredPath := strings.TrimSuffix(path, "_current.yaml") + "_desired.yaml"
	if _, err := os.Stat(desiredPath); os.IsNotExist(err) {
		os.WriteFile(desiredPath, event.NewData, 0644)
		logging.Debug("Seeded desired file: %s", desiredPath)
	}

	u.mu.Lock()
	u.lastWritten[event.Key] = event.NewData
	u.mu.Unlock()

	logging.Debug("Written file: %s", path)

	// Update Istio resource topology
	if u.topology != nil {
		origKind := event.Metadata["kind"] // original casing
		edges := topology.ExtractIstioEdges(origKind, name, ns, event.NewData)
		u.topology.Set(ns, kind, name, edges)
	}

	return nil
}
