package xcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/revolyssup/sync-envoy/pkg/diff"
	"github.com/revolyssup/sync-envoy/pkg/k8s"
	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/topology"
	"github.com/revolyssup/sync-envoy/pkg/types"
)

// istioRef identifies an Istio resource found via cluster query.
type istioRef struct {
	Kind      string
	Name      string
	Namespace string
}

// XCPFileUpdater writes XCP CR state to xcpconfigs/ and maintains
// XCP→Istio topology.
type XCPFileUpdater struct {
	xcpBasePath string
	clients     *k8s.Clients
	topology    *topology.File
	lastWritten map[string][]byte
	mu          sync.Mutex
}

func NewXCPFileUpdater(xcpBasePath string, clients *k8s.Clients, topo *topology.File) *XCPFileUpdater {
	return &XCPFileUpdater{
		xcpBasePath: xcpBasePath,
		clients:     clients,
		topology:    topo,
		lastWritten: make(map[string][]byte),
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

		if u.topology != nil {
			u.topology.Remove(ns, kind, name)
		}
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

	// Update XCP→Istio topology
	if u.clients != nil && u.topology != nil {
		u.updateTopology(ctx, kind, name, ns, event.Metadata)
	}

	return nil
}

// updateTopology finds Istio resources produced by this XCP resource
// and writes the XCP→Istio topology.
func (u *XCPFileUpdater) updateTopology(ctx context.Context, kind, name, ns string, metadata map[string]string) {
	istioKinds, ok := XCPToIstioMapping[kind]
	if !ok {
		return
	}

	// Parse XCP hierarchy labels from event metadata.
	var xcpLabels map[string]string
	if labelsJSON := metadata["labels"]; labelsJSON != "" {
		json.Unmarshal([]byte(labelsJSON), &xcpLabels)
	}

	from := metadata["kind"] + "/" + name // original case, e.g. "IngressGateway/e2e-test-gw"

	var edges []topology.Edge

	for _, istioKind := range istioKinds {
		refs := u.findIstioResources(ctx, istioKind, name, ns, xcpLabels)
		for _, ref := range refs {
			target := ref.Kind + "/" + ref.Name
			if ref.Namespace != "" && ref.Namespace != ns {
				target += " (" + ref.Namespace + ")"
			}
			edges = append(edges, topology.Edge{From: from, To: target})
		}
	}

	u.topology.Set(ns, kind, name, edges)

	if len(edges) > 0 {
		logging.Debug("XCP topology written for %s/%s/%s: %d Istio resources", ns, kind, name, len(edges))
	}
}

// findIstioResources looks for Istio resources of istioKind in the same namespace
// that were produced by the XCP resource.
func (u *XCPFileUpdater) findIstioResources(ctx context.Context, istioKind, xcpName, ns string, xcpLabels map[string]string) []istioRef {
	gvr, ok := IstioGVRForKind(istioKind)
	if !ok {
		return nil
	}

	// Strategy 1: Label-based matching using XCP hierarchy labels.
	// Search across all namespaces — generated Istio resources may live in a
	// different namespace than the XCP resource (e.g. XCP in xcp-system, Istio in echo).
	if len(xcpLabels) > 0 {
		selector := buildLabelSelector(xcpLabels)
		if selector != "" {
			list, err := u.clients.Dynamic.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: selector})
			if err == nil && len(list.Items) > 0 {
				refs := make([]istioRef, 0, len(list.Items))
				for _, item := range list.Items {
					refs = append(refs, istioRef{
						Kind:      istioKind,
						Name:      item.GetName(),
						Namespace: item.GetNamespace(),
					})
				}
				return refs
			}
		}
	}

	// Strategy 2: Name-based convention (XCP resource and Istio resource share the same name).
	_, err := u.clients.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, xcpName, metav1.GetOptions{})
	if err == nil {
		return []istioRef{{
			Kind:      istioKind,
			Name:      xcpName,
			Namespace: ns,
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
