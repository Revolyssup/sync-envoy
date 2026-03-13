package k8s

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Clients holds the Kubernetes dynamic and discovery clients.
type Clients struct {
	Dynamic   dynamic.Interface
	Discovery discovery.DiscoveryInterface
}

// NewClients creates Kubernetes clients, trying in-cluster config first,
// then falling back to kubeconfig.
func NewClients() (*Clients, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
		}
	}
	dc, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}
	return &Clients{Dynamic: dc, Discovery: disc}, nil
}
