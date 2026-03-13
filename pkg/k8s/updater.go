package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/revolyssup/sync-envoy/pkg/diff"
	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// CRUpdater applies Istio CR changes to the Kubernetes cluster.
// It is the updater for the file provider.
type CRUpdater struct {
	clients     *Clients
	lastApplied map[string][]byte
}

func NewCRUpdater(clients *Clients) *CRUpdater {
	return &CRUpdater{
		clients:     clients,
		lastApplied: make(map[string][]byte),
	}
}

func (u *CRUpdater) Name() string { return "kubernetes-updater" }

func (u *CRUpdater) Update(ctx context.Context, event types.Event) error {
	if event.Type == types.EventDelete {
		return u.deleteResource(ctx, event)
	}

	// Diff check against last applied
	if lastData, ok := u.lastApplied[event.Key]; ok {
		d := diff.Compute(lastData, event.NewData)
		if d == "" {
			logging.Debug("No diff for %s, skipping cluster apply", event.Key)
			return nil
		}
		logging.Info("Diff detected for %s:\n%s", event.Key, d)
	}

	return u.applyResource(ctx, event)
}

func (u *CRUpdater) applyResource(ctx context.Context, event types.Event) error {
	var obj map[string]interface{}
	if err := yaml.Unmarshal(event.NewData, &obj); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}
	un := &unstructured.Unstructured{Object: obj}

	CleanMetadata(un)

	gvk := un.GroupVersionKind()
	if gvk.Kind == "" {
		return fmt.Errorf("missing kind in YAML")
	}

	resourceName, err := GetResourceNameFromKind(u.clients.Discovery, gvk)
	if err != nil {
		return fmt.Errorf("failed to discover resource for kind %s: %w", gvk.Kind, err)
	}
	gvr := schema.GroupVersionResource{
		Group: gvk.Group, Version: gvk.Version, Resource: resourceName,
	}

	ns := un.GetNamespace()
	var current *unstructured.Unstructured
	if ns == "" {
		current, err = u.clients.Dynamic.Resource(gvr).Get(ctx, un.GetName(), metav1.GetOptions{})
	} else {
		current, err = u.clients.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, un.GetName(), metav1.GetOptions{})
	}

	if err != nil {
		// Resource doesn't exist in cluster - create it
		logging.Info("Creating %s %s/%s", gvk.Kind, ns, un.GetName())
		if ns == "" {
			_, err = u.clients.Dynamic.Resource(gvr).Create(ctx, un, metav1.CreateOptions{})
		} else {
			_, err = u.clients.Dynamic.Resource(gvr).Namespace(ns).Create(ctx, un, metav1.CreateOptions{})
		}
		if err != nil {
			return fmt.Errorf("failed to create %s %s/%s: %w", gvk.Kind, ns, un.GetName(), err)
		}
		u.lastApplied[event.Key] = event.NewData
		logging.Debug("Successfully created %s %s/%s", gvk.Kind, ns, un.GetName())
		return nil
	}

	// Resource exists - compare and possibly update
	currentRV := current.GetResourceVersion()
	CleanMetadata(current)

	currentJSON, _ := json.Marshal(current.Object)
	newJSON, _ := json.Marshal(un.Object)
	if bytes.Equal(currentJSON, newJSON) {
		logging.Debug("No changes detected for %s %s/%s in cluster, skipping", gvk.Kind, ns, un.GetName())
		u.lastApplied[event.Key] = event.NewData
		return nil
	}

	un.SetResourceVersion(currentRV)
	logging.Info("Updating %s %s/%s", gvk.Kind, ns, un.GetName())
	if ns == "" {
		_, err = u.clients.Dynamic.Resource(gvr).Update(ctx, un, metav1.UpdateOptions{})
	} else {
		_, err = u.clients.Dynamic.Resource(gvr).Namespace(ns).Update(ctx, un, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("failed to update %s %s/%s: %w", gvk.Kind, ns, un.GetName(), err)
	}
	u.lastApplied[event.Key] = event.NewData
	logging.Debug("Successfully updated %s %s/%s", gvk.Kind, ns, un.GetName())
	return nil
}

func (u *CRUpdater) deleteResource(ctx context.Context, event types.Event) error {
	kind := event.Metadata["kind"]
	name := event.Metadata["name"]
	ns := event.Metadata["namespace"]

	if kind == "" || name == "" {
		return fmt.Errorf("delete event missing kind or name metadata")
	}

	gvk := schema.GroupVersionKind{Kind: kind}
	// Try to find the GVR - we need group/version info
	// For delete from file, we won't have full GVK info, so skip
	logging.Debug("Delete event for %s %s/%s - cluster deletion not implemented from file watcher", gvk.Kind, ns, name)
	delete(u.lastApplied, event.Key)
	return nil
}
