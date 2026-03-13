package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/types"
)

// Provider composes a Watcher and an Updater into a single runnable unit.
type Provider struct {
	name    string
	watcher types.Watcher
	updater types.Updater
}

// New creates a new Provider.
func New(name string, w types.Watcher, u types.Updater) *Provider {
	return &Provider{name: name, watcher: w, updater: u}
}

func (p *Provider) Name() string        { return p.name }
func (p *Provider) Watcher() types.Watcher { return p.watcher }
func (p *Provider) Updater() types.Updater { return p.updater }

// Run starts the provider: the watcher sends events, the updater consumes them.
// Blocks until ctx is cancelled.
func (p *Provider) Run(ctx context.Context) error {
	events := make(chan types.Event, 100)

	// Start updater as consumer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				if err := p.updater.Update(ctx, event); err != nil {
					logging.Errorf("[%s] updater error for key %s: %v", p.name, event.Key, err)
				}
			}
		}
	}()

	// Run watcher (blocks until ctx is cancelled)
	err := p.watcher.Watch(ctx, events)
	close(events)
	wg.Wait()
	return err
}

// Registry holds named providers and supports filtering by name.
type Registry struct {
	providers map[string]*Provider
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]*Provider)}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p *Provider) {
	r.providers[p.name] = p
}

// Get returns providers matching the given comma-separated names.
// If filter is empty, all providers are returned.
func (r *Registry) Get(filter string) ([]*Provider, error) {
	if filter == "" {
		result := make([]*Provider, 0, len(r.providers))
		for _, p := range r.providers {
			result = append(result, p)
		}
		return result, nil
	}

	names := strings.Split(filter, ",")
	result := make([]*Provider, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		p, ok := r.providers[name]
		if !ok {
			available := make([]string, 0, len(r.providers))
			for k := range r.providers {
				available = append(available, k)
			}
			return nil, fmt.Errorf("unknown provider %q, available: %s", name, strings.Join(available, ", "))
		}
		result = append(result, p)
	}
	return result, nil
}

// RunAll starts all given providers concurrently.
// Blocks until ctx is cancelled.
func RunAll(ctx context.Context, providers []*Provider) {
	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Add(1)
		go func(p *Provider) {
			defer wg.Done()
			logging.Info("Starting provider: %s", p.Name())
			if err := p.Run(ctx); err != nil && ctx.Err() == nil {
				logging.Errorf("Provider %s exited with error: %v", p.Name(), err)
			}
			logging.Info("Provider %s stopped", p.Name())
		}(p)
	}
	wg.Wait()
}
