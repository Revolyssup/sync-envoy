package k8s

import (
	"context"
	"strings"

	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/yaml"
)

// CRWatcher watches Kubernetes Istio CRs via informers and emits events.
type CRWatcher struct {
	clients *Clients
}

func NewCRWatcher(clients *Clients) *CRWatcher {
	return &CRWatcher{clients: clients}
}

func (w *CRWatcher) Name() string { return "kubernetes-watcher" }

func (w *CRWatcher) Watch(ctx context.Context, events chan<- types.Event) error {
	for _, gvr := range IstioResourceTypes {
		exists, err := ResourceExists(w.clients.Discovery, gvr)
		if err != nil {
			logging.Warn("Failed to check resource %s: %v", gvr.Resource, err)
			continue
		}
		if !exists {
			logging.Debug("Resource %s/%s not found, skipping", gvr.GroupVersion(), gvr.Resource)
			continue
		}
		logging.Info("Watching resource: %s/%s", gvr.GroupVersion(), gvr.Resource)

		informer := dynamicinformer.NewFilteredDynamicInformer(
			w.clients.Dynamic, gvr, metav1.NamespaceAll, 0, cache.Indexers{}, nil,
		)
		informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				u := obj.(*unstructured.Unstructured)
				logging.Debug("K8s ADD: %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
				data, err := yaml.Marshal(u.Object)
				if err != nil {
					logging.Errorf("Failed to marshal %s/%s: %v", u.GetNamespace(), u.GetName(), err)
					return
				}
				events <- types.Event{
					Type:     types.EventAdd,
					Key:      crKey(u),
					NewData:  data,
					Metadata: crMetadata(u),
				}
			},
			UpdateFunc: func(old, newObj interface{}) {
				u := newObj.(*unstructured.Unstructured)
				logging.Debug("K8s UPDATE: %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
				data, err := yaml.Marshal(u.Object)
				if err != nil {
					logging.Errorf("Failed to marshal %s/%s: %v", u.GetNamespace(), u.GetName(), err)
					return
				}
				events <- types.Event{
					Type:     types.EventUpdate,
					Key:      crKey(u),
					NewData:  data,
					Metadata: crMetadata(u),
				}
			},
			DeleteFunc: func(obj interface{}) {
				u := obj.(*unstructured.Unstructured)
				logging.Debug("K8s DELETE: %s %s/%s", u.GetKind(), u.GetNamespace(), u.GetName())
				events <- types.Event{
					Type:     types.EventDelete,
					Key:      crKey(u),
					Metadata: crMetadata(u),
				}
			},
		})
		go informer.Informer().Run(ctx.Done())
	}
	<-ctx.Done()
	return nil
}

func crKey(u *unstructured.Unstructured) string {
	kind := strings.ToLower(u.GetKind())
	if u.GetNamespace() == "" {
		return kind + "/" + u.GetName()
	}
	return u.GetNamespace() + "/" + kind + "/" + u.GetName()
}

func crMetadata(u *unstructured.Unstructured) map[string]string {
	return map[string]string{
		"kind":      u.GetKind(),
		"name":      u.GetName(),
		"namespace": u.GetNamespace(),
	}
}
