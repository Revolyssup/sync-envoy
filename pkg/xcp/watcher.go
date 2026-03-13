package xcp

import (
	"context"
	"encoding/json"
	"strings"

	"sync-envoy/pkg/k8s"
	"sync-envoy/pkg/logging"
	"sync-envoy/pkg/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/yaml"
)

// XCPWatcher watches XCP CRDs via dynamic informers and emits events.
type XCPWatcher struct {
	clients *k8s.Clients
}

func NewXCPWatcher(clients *k8s.Clients) *XCPWatcher {
	return &XCPWatcher{clients: clients}
}

func (w *XCPWatcher) Name() string { return "xcp-watcher" }

func (w *XCPWatcher) Watch(ctx context.Context, events chan<- types.Event) error {
	for _, gvr := range XCPResourceTypes {
		exists, err := k8s.ResourceExists(w.clients.Discovery, gvr)
		if err != nil {
			logging.Warn("Failed to check XCP resource %s: %v", gvr.Resource, err)
			continue
		}
		if !exists {
			logging.Debug("XCP resource %s/%s not found, skipping", gvr.GroupVersion(), gvr.Resource)
			continue
		}
		logging.Info("Watching XCP resource: %s/%s", gvr.GroupVersion(), gvr.Resource)

		informer := dynamicinformer.NewFilteredDynamicInformer(
			w.clients.Dynamic, gvr, metav1.NamespaceAll, 0, cache.Indexers{}, nil,
		)
		informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				u := obj.(*unstructured.Unstructured)
				logging.Debug("XCP ADD: %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
				data, err := yaml.Marshal(u.Object)
				if err != nil {
					logging.Errorf("Failed to marshal XCP %s/%s: %v", u.GetNamespace(), u.GetName(), err)
					return
				}
				events <- types.Event{
					Type:     types.EventAdd,
					Key:      xcpKey(u),
					NewData:  data,
					Metadata: xcpMetadata(u),
				}
			},
			UpdateFunc: func(old, newObj interface{}) {
				u := newObj.(*unstructured.Unstructured)
				logging.Debug("XCP UPDATE: %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
				data, err := yaml.Marshal(u.Object)
				if err != nil {
					logging.Errorf("Failed to marshal XCP %s/%s: %v", u.GetNamespace(), u.GetName(), err)
					return
				}
				events <- types.Event{
					Type:     types.EventUpdate,
					Key:      xcpKey(u),
					NewData:  data,
					Metadata: xcpMetadata(u),
				}
			},
			DeleteFunc: func(obj interface{}) {
				u := obj.(*unstructured.Unstructured)
				logging.Debug("XCP DELETE: %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
				events <- types.Event{
					Type:     types.EventDelete,
					Key:      xcpKey(u),
					Metadata: xcpMetadata(u),
				}
			},
		})
		go informer.Informer().Run(ctx.Done())
	}
	<-ctx.Done()
	return nil
}

func xcpKey(u *unstructured.Unstructured) string {
	kind := strings.ToLower(u.GetKind())
	if u.GetNamespace() == "" {
		return kind + "/" + u.GetName()
	}
	return u.GetNamespace() + "/" + kind + "/" + u.GetName()
}

func xcpMetadata(u *unstructured.Unstructured) map[string]string {
	m := map[string]string{
		"kind":      u.GetKind(),
		"name":      u.GetName(),
		"namespace": u.GetNamespace(),
	}
	if labels := u.GetLabels(); len(labels) > 0 {
		data, _ := json.Marshal(labels)
		m["labels"] = string(data)
	}
	return m
}
