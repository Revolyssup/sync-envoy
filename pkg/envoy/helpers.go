package envoy

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ConfigType represents an Envoy configuration category.
type ConfigType string

const (
	ConfigListener  ConfigType = "listener"
	ConfigCluster   ConfigType = "cluster"
	ConfigEndpoint  ConfigType = "endpoint"
	ConfigRoute     ConfigType = "route"
	ConfigBootstrap ConfigType = "bootstrap"
	ConfigSecret    ConfigType = "secret"
)

// AllConfigTypes lists all known Envoy config types.
var AllConfigTypes = []ConfigType{
	ConfigListener, ConfigCluster, ConfigEndpoint,
	ConfigRoute, ConfigBootstrap, ConfigSecret,
}

// PodInfo holds basic pod identification.
type PodInfo struct {
	Name      string
	Namespace string
}

// TimestampedConfig wraps an Envoy config with metadata and timestamp.
type TimestampedConfig struct {
	LastUpdated time.Time       `json:"last_updated"`
	PodName     string          `json:"pod_name"`
	Namespace   string          `json:"namespace"`
	ConfigType  string          `json:"config_type"`
	Config      json.RawMessage `json:"config"`
}

// GetPods returns pods matching the given label selector across all namespaces.
func GetPods(ctx context.Context, selector string) ([]PodInfo, error) {
	args := []string{
		"get", "pods", "--all-namespaces",
		"-o", `jsonpath={range .items[*]}{.metadata.name}{" "}{.metadata.namespace}{"\n"}{end}`,
	}
	if selector != "" {
		args = append(args, "-l", selector)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kubectl failed: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var pods []PodInfo
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			pods = append(pods, PodInfo{Name: parts[0], Namespace: parts[1]})
		}
	}
	return pods, nil
}

// RunIstioctlProxyConfig executes istioctl proxy-config for a pod.
func RunIstioctlProxyConfig(ctx context.Context, pod, namespace, typ string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "istioctl", "proxy-config", typ, pod, "-n", namespace, "-o", "json")
	return cmd.CombinedOutput()
}

// FetchAdminConfigDump fetches the Envoy config dump via kubectl exec.
func FetchAdminConfigDump(ctx context.Context, podName, namespace string) ([]byte, error) {
	cmd := exec.CommandContext(
		ctx, "kubectl", "exec", podName, "-n", namespace, "-c", "istio-proxy",
		"--", "curl", "-s", "http://localhost:15000/config_dump",
	)
	return cmd.CombinedOutput()
}

// adminConfigTypeMap maps ConfigType to Envoy admin config_dump @type values.
var adminConfigTypeMap = map[ConfigType]string{
	ConfigBootstrap: "type.googleapis.com/envoy.admin.v3.BootstrapConfigDump",
	ConfigCluster:   "type.googleapis.com/envoy.admin.v3.ClustersConfigDump",
	ConfigEndpoint:  "type.googleapis.com/envoy.admin.v3.EndpointsConfigDump",
	ConfigListener:  "type.googleapis.com/envoy.admin.v3.ListenersConfigDump",
	ConfigRoute:     "type.googleapis.com/envoy.admin.v3.RoutesConfigDump",
	ConfigSecret:    "type.googleapis.com/envoy.admin.v3.SecretsConfigDump",
}

// ExtractConfigFromDump extracts a specific config type from a full config_dump.
func ExtractConfigFromDump(dump []byte, configType ConfigType) json.RawMessage {
	var result map[string]interface{}
	if err := json.Unmarshal(dump, &result); err != nil {
		return nil
	}

	configs, ok := result["configs"].([]interface{})
	if !ok {
		return nil
	}

	targetType := adminConfigTypeMap[configType]
	for _, cfg := range configs {
		cfgMap, ok := cfg.(map[string]interface{})
		if !ok {
			continue
		}
		if t, ok := cfgMap["@type"].(string); ok && t == targetType {
			data, _ := json.Marshal(cfgMap)
			return data
		}
	}
	return nil
}
