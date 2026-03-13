package envoy

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"sync-envoy/pkg/logging"
	"sync-envoy/pkg/types"
)

// EnvoyWatcher watches Envoy sidecar configurations.
// It tries three strategies in order:
// 1. CSDS gRPC streaming via istiod
// 2. Admin endpoint polling via kubectl exec
// 3. istioctl proxy-config polling (final fallback)
type EnvoyWatcher struct {
	namespace        string
	workloadSelector string
	csdsAddress      string // istiod gRPC address for CSDS (e.g., "localhost:15010")
	lastTimestamps   map[string]time.Time
	mu               sync.Mutex
}

func NewEnvoyWatcher(namespace, workloadSelector, csdsAddress string) *EnvoyWatcher {
	return &EnvoyWatcher{
		namespace:        namespace,
		workloadSelector: workloadSelector,
		csdsAddress:      csdsAddress,
		lastTimestamps:   make(map[string]time.Time),
	}
}

func (w *EnvoyWatcher) Name() string { return "envoy-watcher" }

func (w *EnvoyWatcher) Watch(ctx context.Context, events chan<- types.Event) error {
	// Strategy 1: CSDS streaming
	if w.csdsAddress != "" {
		logging.Info("Attempting CSDS streaming connection to %s", w.csdsAddress)
		err := w.StreamCSDS(ctx, events)
		if err != nil && ctx.Err() == nil {
			logging.Warn("CSDS streaming failed: %v, falling back to admin endpoint polling", err)
		} else {
			return err
		}
	}

	// Strategy 2: Admin endpoint polling
	logging.Info("Attempting admin endpoint polling for Envoy configs")
	err := w.watchAdminEndpoint(ctx, events)
	if err != nil && ctx.Err() == nil {
		logging.Warn("Admin endpoint polling failed: %v, falling back to istioctl", err)
	} else {
		return err
	}

	// Strategy 3: istioctl polling (final fallback)
	logging.Info("Falling back to istioctl proxy-config polling")
	return w.watchIstioctl(ctx, events)
}

// watchAdminEndpoint polls each sidecar's admin /config_dump endpoint.
func (w *EnvoyWatcher) watchAdminEndpoint(ctx context.Context, events chan<- types.Event) error {
	// Quick test: try to reach the first pod's admin endpoint
	pods, err := GetPods(w.namespace, w.workloadSelector)
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}
	if len(pods) == 0 {
		return fmt.Errorf("no pods found matching selector")
	}

	_, err = FetchAdminConfigDump(pods[0].Name, pods[0].Namespace)
	if err != nil {
		return fmt.Errorf("admin endpoint not reachable on %s/%s: %w", pods[0].Namespace, pods[0].Name, err)
	}

	logging.Info("Admin endpoint reachable, starting polling")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pods, err := GetPods(w.namespace, w.workloadSelector)
			if err != nil {
				logging.Errorf("Failed to list pods: %v", err)
				continue
			}
			for _, pod := range pods {
				w.fetchAndEmitAdminConfigs(ctx, pod, events)
			}
		}
	}
}

func (w *EnvoyWatcher) fetchAndEmitAdminConfigs(ctx context.Context, pod PodInfo, events chan<- types.Event) {
	dump, err := FetchAdminConfigDump(pod.Name, pod.Namespace)
	if err != nil {
		logging.Debug("Failed to fetch config_dump for %s/%s: %v", pod.Namespace, pod.Name, err)
		return
	}

	now := time.Now()
	for _, configType := range AllConfigTypes {
		key := fmt.Sprintf("%s/%s/%s", pod.Namespace, pod.Name, configType)

		configData := ExtractConfigFromDump(dump, configType)
		if configData == nil {
			continue
		}

		tc := TimestampedConfig{
			LastUpdated: now,
			PodName:     pod.Name,
			Namespace:   pod.Namespace,
			ConfigType:  string(configType),
			Config:      configData,
		}
		data, _ := json.MarshalIndent(tc, "", "  ")

		events <- types.Event{
			Type:    types.EventUpdate,
			Key:     key,
			NewData: data,
			Metadata: map[string]string{
				"pod_name":    pod.Name,
				"namespace":   pod.Namespace,
				"config_type": string(configType),
			},
		}

		w.mu.Lock()
		w.lastTimestamps[key] = now
		w.mu.Unlock()
	}
}

// watchIstioctl polls using istioctl proxy-config (final fallback).
func (w *EnvoyWatcher) watchIstioctl(ctx context.Context, events chan<- types.Event) error {
	logging.Info("Using istioctl proxy-config polling for Envoy configs")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pods, err := GetPods(w.namespace, w.workloadSelector)
			if err != nil {
				logging.Errorf("Failed to list pods: %v", err)
				continue
			}

			now := time.Now()
			for _, pod := range pods {
				for _, configType := range AllConfigTypes {
					key := fmt.Sprintf("%s/%s/%s", pod.Namespace, pod.Name, configType)

					out, err := RunIstioctlProxyConfig(pod.Name, pod.Namespace, string(configType))
					if err != nil {
						// Many pods won't have sidecars, skip silently
						continue
					}

					tc := TimestampedConfig{
						LastUpdated: now,
						PodName:     pod.Name,
						Namespace:   pod.Namespace,
						ConfigType:  string(configType),
						Config:      json.RawMessage(out),
					}
					data, _ := json.MarshalIndent(tc, "", "  ")

					events <- types.Event{
						Type:    types.EventUpdate,
						Key:     key,
						NewData: data,
						Metadata: map[string]string{
							"pod_name":    pod.Name,
							"namespace":   pod.Namespace,
							"config_type": string(configType),
						},
					}

					w.mu.Lock()
					w.lastTimestamps[key] = now
					w.mu.Unlock()
				}
			}
		}
	}
}
