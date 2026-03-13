package envoy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/revolyssup/sync-envoy/pkg/logging"
	"github.com/revolyssup/sync-envoy/pkg/types"

	discovery "github.com/envoyproxy/go-control-plane/envoy/service/status/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

// StreamCSDS connects to istiod's CSDS gRPC endpoint and streams client status updates.
// The csdsAddress should be the istiod address (e.g., "localhost:15010" or "istiod.istio-system:15010").
func (w *EnvoyWatcher) StreamCSDS(ctx context.Context, events chan<- types.Event) error {
	conn, err := grpc.NewClient(w.csdsAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to CSDS endpoint %s: %w", w.csdsAddress, err)
	}
	defer conn.Close()

	client := discovery.NewClientStatusDiscoveryServiceClient(conn)
	stream, err := client.StreamClientStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to start CSDS stream: %w", err)
	}

	logging.Info("CSDS streaming connected to %s", w.csdsAddress)

	// Send initial request
	req := &discovery.ClientStatusRequest{}
	if err := stream.Send(req); err != nil {
		return fmt.Errorf("failed to send initial CSDS request: %w", err)
	}

	// Periodically re-send request to get fresh updates
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := stream.Send(req); err != nil {
					logging.Debug("Failed to send CSDS request: %v", err)
					return
				}
			}
		}
	}()

	// Receive and process responses
	for {
		resp, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("CSDS stream recv error: %w", err)
		}

		now := time.Now()
		for _, clientConfig := range resp.GetConfig() {
			node := clientConfig.GetNode()
			if node == nil {
				continue
			}

			podName, namespace := parseNodeID(node.GetId())
			if podName == "" {
				continue
			}

			for _, xdsConfig := range clientConfig.GetXdsConfig() {
				configType := classifyXdsConfig(xdsConfig)
				if configType == "" {
					continue
				}

				key := fmt.Sprintf("%s/%s/%s", namespace, podName, configType)

				configJSON, err := protojson.Marshal(xdsConfig)
				if err != nil {
					logging.Debug("Failed to marshal xDS config: %v", err)
					continue
				}

				tc := TimestampedConfig{
					LastUpdated: now,
					PodName:     podName,
					Namespace:   namespace,
					ConfigType:  configType,
					Config:      json.RawMessage(configJSON),
				}
				data, _ := json.MarshalIndent(tc, "", "  ")

				select {
				case events <- types.Event{
					Type:    types.EventUpdate,
					Key:     key,
					NewData: data,
					Metadata: map[string]string{
						"pod_name":    podName,
						"namespace":   namespace,
						"config_type": configType,
					},
				}:
				case <-ctx.Done():
					return nil
				}
				w.mu.Lock()
				w.lastTimestamps[key] = now
				w.mu.Unlock()
			}
		}
	}
}

// parseNodeID extracts pod name and namespace from an Envoy node ID.
// Format: sidecar~<IP>~<pod>.<namespace>~<namespace>.svc.cluster.local
func parseNodeID(nodeID string) (podName, namespace string) {
	parts := strings.Split(nodeID, "~")
	if len(parts) < 3 {
		return "", ""
	}
	podParts := strings.SplitN(parts[2], ".", 2)
	if len(podParts) < 2 {
		return parts[2], ""
	}
	return podParts[0], podParts[1]
}

// classifyXdsConfig determines the config type from a PerXdsConfig.
func classifyXdsConfig(xds *discovery.PerXdsConfig) string {
	switch xds.GetPerXdsConfig().(type) {
	case *discovery.PerXdsConfig_ListenerConfig:
		return string(ConfigListener)
	case *discovery.PerXdsConfig_ClusterConfig:
		return string(ConfigCluster)
	case *discovery.PerXdsConfig_RouteConfig:
		return string(ConfigRoute)
	case *discovery.PerXdsConfig_EndpointConfig:
		return string(ConfigEndpoint)
	default:
		return ""
	}
}
