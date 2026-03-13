package xcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sync-envoy/pkg/diff"
	"sync-envoy/pkg/k8s"
	"sync-envoy/pkg/logging"
	"sync-envoy/pkg/types"
)

// XCPFileUpdater writes XCP CR state to xcpconfigs/ and maintains
// XCP↔Istio correlation files.
type XCPFileUpdater struct {
	xcpBasePath   string // e.g. "xcpconfigs"
	istioBasePath string // e.g. "istioconfigs"
	clients       *k8s.Clients
	lastWritten   map[string][]byte
	mu            sync.Mutex
}

func NewXCPFileUpdater(xcpBasePath, istioBasePath string, clients *k8s.Clients) *XCPFileUpdater {
	return &XCPFileUpdater{
		xcpBasePath:   xcpBasePath,
		istioBasePath: istioBasePath,
		clients:       clients,
		lastWritten:   make(map[string][]byte),
	}
}

func (u *XCPFileUpdater) Name() string { return "xcp-file-updater" }

func (u *XCPFileUpdater) Update(ctx context.Context, event types.Event) error {
	kind := strings.ToLower(event.Metadata["kind"])
	name := event.Metadata["name"]
	ns := event.Metadata["namespace"]

	var path string
	if ns == "" {
		path = filepath.Join(u.xcpBasePath, kind, name+"_current.yaml")
	} else {
		path = filepath.Join(u.xcpBasePath, ns, kind, name+"_current.yaml")
	}

	if event.Type == types.EventDelete {
		u.mu.Lock()
		delete(u.lastWritten, event.Key)
		u.mu.Unlock()

		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		logging.Debug("Deleted XCP file: %s", path)
		RemoveXCPCorrelation(u.xcpBasePath, ns, kind, name)
		return nil
	}

	// Diff check
	u.mu.Lock()
	lastData, exists := u.lastWritten[event.Key]
	u.mu.Unlock()

	if exists {
		d := diff.Compute(lastData, event.NewData)
		if d == "" {
			logging.Debug("No diff for XCP %s, skipping write", path)
			return nil
		}
		logging.Info("Diff detected for XCP %s:\n%s", path, d)
	}

	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, event.NewData, 0644); err != nil {
		return err
	}

	u.mu.Lock()
	u.lastWritten[event.Key] = event.NewData
	u.mu.Unlock()

	logging.Debug("Written XCP file: %s", path)

	// Run XCP→Istio correlation (requires k8s clients for label/name matching)
	if u.clients != nil {
		u.updateCorrelation(ctx, kind, name, ns, event.Metadata)
	}

	return nil
}

// updateCorrelation finds Istio resources produced by this XCP resource
// and writes bidirectional correlation files.
func (u *XCPFileUpdater) updateCorrelation(ctx context.Context, kind, name, ns string, metadata map[string]string) {
	istioKinds, ok := XCPToIstioMapping[kind]
	if !ok {
		return
	}

	// Parse XCP hierarchy labels from event metadata.
	var xcpLabels map[string]string
	if labelsJSON := metadata["labels"]; labelsJSON != "" {
		json.Unmarshal([]byte(labelsJSON), &xcpLabels)
	}

	// Build the XCP source ref for reverse correlation.
	xcpRef := XCPResourceRef{
		Kind:      metadata["kind"], // original case
		Name:      name,
		Namespace: ns,
	}
	if xcpLabels != nil {
		xcpRef.Workspace = xcpLabels["xcp.tetrate.io/workspace"]
		for _, lbl := range []string{"xcp.tetrate.io/trafficGroup", "xcp.tetrate.io/securityGroup", "xcp.tetrate.io/gatewayGroup"} {
			if v, ok := xcpLabels[lbl]; ok {
				xcpRef.Group = v
				break
			}
		}
	}

	var allIstioRefs []IstioOutputRef

	for _, istioKind := range istioKinds {
		refs := u.findIstioResources(ctx, istioKind, name, ns, xcpLabels)
		allIstioRefs = append(allIstioRefs, refs...)
	}

	// Write forward correlation: xcpconfigs/<ns>/<kind>/correlation.json
	if err := WriteXCPCorrelation(u.xcpBasePath, ns, kind, name, allIstioRefs); err != nil {
		logging.Errorf("XCP correlation: failed to write forward for %s/%s/%s: %v", ns, kind, name, err)
	}

	// Write reverse correlation: istioconfigs/<ns>/<istioKind>/xcp-correlation.json
	for _, ref := range allIstioRefs {
		if err := WriteXCPReverseCorrelation(u.istioBasePath, ref, xcpRef); err != nil {
			logging.Errorf("XCP correlation: failed to write reverse for %s/%s: %v", ref.Kind, ref.Name, err)
		}
	}

	if len(allIstioRefs) > 0 {
		logging.Debug("XCP correlation written for %s/%s/%s: %d Istio resources", ns, kind, name, len(allIstioRefs))
	}
}

// findIstioResources looks for Istio resources of istioKind in the same namespace
// that were produced by the XCP resource. It tries label-based matching first,
// then falls back to name-based convention.
func (u *XCPFileUpdater) findIstioResources(ctx context.Context, istioKind, xcpName, ns string, xcpLabels map[string]string) []IstioOutputRef {
	gvr, ok := IstioGVRForKind(istioKind)
	if !ok {
		return nil
	}

	// Strategy 1: Label-based matching using XCP hierarchy labels.
	if len(xcpLabels) > 0 {
		selector := buildLabelSelector(xcpLabels)
		if selector != "" {
			list, err := u.clients.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
			if err == nil && len(list.Items) > 0 {
				refs := make([]IstioOutputRef, 0, len(list.Items))
				for _, item := range list.Items {
					refs = append(refs, IstioOutputRef{
						Kind:      istioKind,
						Name:      item.GetName(),
						Namespace: item.GetNamespace(),
						FilePath:  istioFilePath(u.istioBasePath, item.GetNamespace(), istioKind, item.GetName()),
					})
				}
				return refs
			}
		}
	}

	// Strategy 2: Name-based convention (XCP resource and Istio resource share the same name).
	_, err := u.clients.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, xcpName, metav1.GetOptions{})
	if err == nil {
		return []IstioOutputRef{{
			Kind:      istioKind,
			Name:      xcpName,
			Namespace: ns,
			FilePath:  istioFilePath(u.istioBasePath, ns, istioKind, xcpName),
		}}
	}

	return nil
}

// buildLabelSelector builds a k8s label selector string from XCP hierarchy labels.
func buildLabelSelector(labels map[string]string) string {
	var parts []string
	for _, key := range XCPHierarchyLabels {
		if val, ok := labels[key]; ok {
			parts = append(parts, key+"="+val)
		}
	}
	return strings.Join(parts, ",")
}

func istioFilePath(basePath, ns, kind, name string) string {
	return filepath.Join(basePath, ns, strings.ToLower(kind), name+"_current.yaml")
}
