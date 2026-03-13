package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"sync-envoy/pkg/correlation"
	"sync-envoy/pkg/diff"
	"sync-envoy/pkg/logging"
	"sync-envoy/pkg/types"
)

// PodLister returns pod names in a namespace matching the given label selector.
type PodLister interface {
	ListPodNames(ctx context.Context, namespace string, matchLabels map[string]string) ([]string, error)
}

// selectorPolicyKinds maps lowercase Istio kind → the Envoy config type that
// carries the policy's effect (used when writing selector-based correlation).
var selectorPolicyKinds = map[string]string{
	"authorizationpolicy":    "listener",
	"requestauthentication":  "listener",
	"peerauthentication":     "listener",
	"wasmplugin":             "listener",
	"telemetry":              "cluster",
}

// CurrentFileUpdater writes _current.yaml files to the istioconfigs directory.
// It is the updater for the kubernetes provider, writing CR state from the cluster.
type CurrentFileUpdater struct {
	basePath    string
	lastWritten map[string][]byte
	mu          sync.Mutex

	// selector correlation (optional)
	podLister    PodLister
	envoyBaseDir string
}

func NewCurrentFileUpdater(basePath string) *CurrentFileUpdater {
	return &CurrentFileUpdater{
		basePath:    basePath,
		lastWritten: make(map[string][]byte),
	}
}

// WithSelectorCorrelation enables writing correlation.json into the istioconfigs
// tree for policy-type resources that don't embed filter_metadata.
func (u *CurrentFileUpdater) WithSelectorCorrelation(lister PodLister, envoyBaseDir string) *CurrentFileUpdater {
	u.podLister = lister
	u.envoyBaseDir = envoyBaseDir
	return u
}

func (u *CurrentFileUpdater) Name() string { return "file-updater" }

func (u *CurrentFileUpdater) Update(ctx context.Context, event types.Event) error {
	kind := strings.ToLower(event.Metadata["kind"])
	name := event.Metadata["name"]
	ns := event.Metadata["namespace"]

	var path string
	if ns == "" {
		// Non-namespaced: istioconfigs/resourcetype/name_current.yaml
		path = filepath.Join(u.basePath, kind, name+"_current.yaml")
	} else {
		// Namespaced: istioconfigs/namespace/resourcetype/name_current.yaml
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
		logging.Info("Diff detected for %s:\n%s", path, d)
	}

	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, event.NewData, 0644); err != nil {
		return err
	}

	u.mu.Lock()
	u.lastWritten[event.Key] = event.NewData
	u.mu.Unlock()

	logging.Debug("Written file: %s", path)

	// Write selector-based correlation for supported policy kinds.
	if u.podLister != nil {
		if envoyConfigType, ok := selectorPolicyKinds[kind]; ok {
			u.updateSelectorCorrelation(ctx, name, ns, kind, envoyConfigType, event.NewData)
		}
	}

	return nil
}

// updateSelectorCorrelation reads spec.selector.matchLabels from the CR YAML,
// lists matching pods, and writes reverse-index correlation.json entries.
func (u *CurrentFileUpdater) updateSelectorCorrelation(
	ctx context.Context,
	name, ns, kind, envoyConfigType string,
	yamlData []byte,
) {
	matchLabels := extractSelector(yamlData)
	// An empty selector means "all pods in namespace" for some resources;
	// skip to avoid flooding correlation with every pod.
	if len(matchLabels) == 0 {
		return
	}

	pods, err := u.podLister.ListPodNames(ctx, ns, matchLabels)
	if err != nil {
		logging.Debug("selector correlation: failed to list pods for %s/%s/%s: %v", ns, kind, name, err)
		return
	}
	if len(pods) == 0 {
		return
	}

	ref := correlation.IstioResourceRef{Kind: kindToTitle(kind), Name: name, Namespace: ns}
	for _, podName := range pods {
		// istioconfigs-side reverse-index: istioconfigs/<ns>/<kind>/correlation.json
		if err := correlation.WriteIstioCorrelation(
			u.basePath, []correlation.IstioResourceRef{ref},
			podName, ns, envoyConfigType, u.envoyBaseDir,
		); err != nil {
			logging.Errorf("selector correlation: failed to write istio side for %s/%s/%s pod=%s: %v", ns, kind, name, podName, err)
		}
		// envoyconfigs-side: envoyconfigs/<ns>/<pod>/<kind>-correlation.json
		// Read the pod's listener.json to get specific affected listener names.
		listenerFile := filepath.Join(u.envoyBaseDir, ns, podName, "listener.json")
		affected := correlation.ExtractListenerNamesFromFile(listenerFile)
		if err := correlation.UpsertPodSourceRef(u.envoyBaseDir, podName, ns, ref, affected); err != nil {
			logging.Errorf("selector correlation: failed to upsert pod ref for %s/%s/%s pod=%s: %v", ns, kind, name, podName, err)
		}
	}
	logging.Debug("Selector correlation written for %s/%s/%s: %d pods, config_type=%s",
		ns, kind, name, len(pods), envoyConfigType)
}

// extractSelector parses spec.selector.matchLabels from a Kubernetes CR YAML.
func extractSelector(data []byte) map[string]string {
	var cr struct {
		Spec struct {
			Selector struct {
				MatchLabels map[string]string `yaml:"matchLabels"`
			} `yaml:"selector"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &cr); err != nil {
		return nil
	}
	return cr.Spec.Selector.MatchLabels
}

// kindToTitle converts "authorizationpolicy" → "AuthorizationPolicy".
// Needed because the kind stored in metadata is already the proper casing from the k8s watcher.
// We receive it lowercased, so we just title-case it here for the IstioResourceRef.Kind field.
func kindToTitle(kind string) string {
	kindTitles := map[string]string{
		"authorizationpolicy":   "AuthorizationPolicy",
		"requestauthentication": "RequestAuthentication",
		"peerauthentication":    "PeerAuthentication",
		"wasmplugin":            "WasmPlugin",
		"telemetry":             "Telemetry",
	}
	if t, ok := kindTitles[kind]; ok {
		return t
	}
	// Fallback: capitalize first letter
	if len(kind) == 0 {
		return kind
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}
