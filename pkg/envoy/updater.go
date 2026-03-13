package envoy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/revolyssup/sync-envoy/pkg/correlation"
	"github.com/revolyssup/sync-envoy/pkg/diff"
	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/types"
)

// FileUpdater writes Envoy configuration JSON files to the envoyconfigs directory.
// For each config type that carries Istio source refs it writes a separate
// <kind>-correlation.json file so that files from different providers never
// overwrite each other.
type FileUpdater struct {
	baseDir          string
	istioconfigsPath string
	lastWritten      map[string][]byte
	ignorePaths      []string
	mu               sync.Mutex
}

func NewFileUpdater(baseDir string, ignorePaths ...string) *FileUpdater {
	return &FileUpdater{
		baseDir:     baseDir,
		lastWritten: make(map[string][]byte),
		ignorePaths: ignorePaths,
	}
}

func (u *FileUpdater) WithIstioconfigsPath(p string) *FileUpdater {
	u.istioconfigsPath = p
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
		logging.Info("Diff detected for envoy config %s:\n%s", filePath, d)
	}

	if err := os.WriteFile(filePath, event.NewData, 0644); err != nil {
		return err
	}

	u.mu.Lock()
	u.lastWritten[event.Key] = event.NewData
	u.mu.Unlock()

	logging.Debug("Written Envoy config %s for %s/%s", configType, namespace, podName)

	switch ConfigType(configType) {
	case ConfigCluster:
		u.handleClusterCorrelation(podDir, podName, namespace, event.NewData)
	case ConfigRoute:
		u.handleDumpCorrelation(podDir, podName, namespace, configType,
			event.NewData, "virtualservice", correlation.ExtractFromRouteDump)
	case ConfigListener:
		u.handleDumpCorrelation(podDir, podName, namespace, configType,
			event.NewData, "gateway", correlation.ExtractFromListenerDump)
	}

	return nil
}

// handleClusterCorrelation extracts DestinationRule refs from a cluster dump,
// writes destinationrule-correlation.json with per-ref affected clusters,
// and updates the istioconfigs reverse-index.
func (u *FileUpdater) handleClusterCorrelation(podDir, podName, namespace string, data []byte) {
	var tc TimestampedConfig
	if err := json.Unmarshal(data, &tc); err != nil {
		logging.Debug("correlation: failed to parse TimestampedConfig for %s/%s: %v", namespace, podName, err)
		return
	}

	affectedMap := correlation.ExtractFromClusterDump(tc.Config)

	pc := correlation.PodCorrelation{
		Pod:         podName,
		Namespace:   namespace,
		AffectedBy:  affectedMap,
		LastUpdated: time.Now(),
	}
	writePodKindFile(podDir, "destinationrule", pc)

	refs := correlation.RefsFromAffectedMap(affectedMap)
	if u.istioconfigsPath != "" && len(refs) > 0 {
		if err := correlation.WriteIstioCorrelation(
			u.istioconfigsPath, refs, podName, namespace, string(ConfigCluster), u.baseDir,
		); err != nil {
			logging.Errorf("correlation: failed to write istio reverse-index for %s/%s: %v", namespace, podName, err)
		}
	}
}

// handleDumpCorrelation is the generic handler for route and listener dumps.
func (u *FileUpdater) handleDumpCorrelation(
	podDir, podName, namespace, configType string,
	data []byte,
	kindFile string,
	extract func(json.RawMessage) map[string][]correlation.AffectedEnvoyResource,
) {
	var tc TimestampedConfig
	if err := json.Unmarshal(data, &tc); err != nil {
		logging.Debug("correlation: failed to parse TimestampedConfig for %s/%s/%s: %v", namespace, podName, configType, err)
		return
	}

	affectedMap := extract(tc.Config)

	pc := correlation.PodCorrelation{
		Pod:         podName,
		Namespace:   namespace,
		AffectedBy:  affectedMap,
		LastUpdated: time.Now(),
	}
	writePodKindFile(podDir, kindFile, pc)

	refs := correlation.RefsFromAffectedMap(affectedMap)
	if u.istioconfigsPath != "" && len(refs) > 0 {
		if err := correlation.WriteIstioCorrelation(u.istioconfigsPath, refs, podName, namespace, configType, u.baseDir); err != nil {
			logging.Errorf("correlation: failed to write istio reverse-index for %s/%s/%s: %v", namespace, podName, configType, err)
		}
	}
}

// writePodKindFile writes <kindFile>-correlation.json inside podDir.
func writePodKindFile(podDir, kindFile string, pc correlation.PodCorrelation) {
	data, err := json.MarshalIndent(pc, "", "  ")
	if err != nil {
		logging.Debug("correlation: marshal failed for %s/%s: %v", pc.Namespace, pc.Pod, err)
		return
	}
	path := filepath.Join(podDir, kindFile+"-correlation.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		logging.Errorf("correlation: failed to write %s: %v", path, err)
		return
	}
	logging.Debug("Written %s for %s/%s", filepath.Base(path), pc.Namespace, pc.Pod)
}
