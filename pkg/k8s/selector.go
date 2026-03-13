package k8s

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var podGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

// ListPodNames returns the names of pods in namespace matching all labels in matchLabels.
func (c *Clients) ListPodNames(ctx context.Context, namespace string, matchLabels map[string]string) ([]string, error) {
	labelSel := ""
	if len(matchLabels) > 0 {
		parts := make([]string, 0, len(matchLabels))
		for k, v := range matchLabels {
			parts = append(parts, k+"="+v)
		}
		labelSel = strings.Join(parts, ",")
	}

	list, err := c.Dynamic.Resource(podGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSel,
	})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.GetName())
	}
	return names, nil
}
