package envoy

import (
	"encoding/json"
	"testing"
)

func TestExtractConfigFromDump(t *testing.T) {
	dump := map[string]interface{}{
		"configs": []interface{}{
			map[string]interface{}{
				"@type": "type.googleapis.com/envoy.admin.v3.BootstrapConfigDump",
				"bootstrap": map[string]interface{}{
					"node": map[string]interface{}{
						"id": "test-node",
					},
				},
			},
			map[string]interface{}{
				"@type": "type.googleapis.com/envoy.admin.v3.ClustersConfigDump",
				"static_clusters": []interface{}{},
			},
			map[string]interface{}{
				"@type": "type.googleapis.com/envoy.admin.v3.ListenersConfigDump",
				"static_listeners": []interface{}{},
			},
			map[string]interface{}{
				"@type": "type.googleapis.com/envoy.admin.v3.RoutesConfigDump",
				"static_route_configs": []interface{}{},
			},
			map[string]interface{}{
				"@type": "type.googleapis.com/envoy.admin.v3.EndpointsConfigDump",
				"static_endpoint_configs": []interface{}{},
			},
			map[string]interface{}{
				"@type": "type.googleapis.com/envoy.admin.v3.SecretsConfigDump",
				"static_secrets": []interface{}{},
			},
		},
	}
	dumpJSON, _ := json.Marshal(dump)

	tests := []struct {
		configType ConfigType
		wantNil    bool
	}{
		{ConfigBootstrap, false},
		{ConfigCluster, false},
		{ConfigListener, false},
		{ConfigRoute, false},
		{ConfigEndpoint, false},
		{ConfigSecret, false},
	}

	for _, tt := range tests {
		result := ExtractConfigFromDump(dumpJSON, tt.configType)
		if tt.wantNil && result != nil {
			t.Errorf("ExtractConfigFromDump(%s) expected nil, got data", tt.configType)
		}
		if !tt.wantNil && result == nil {
			t.Errorf("ExtractConfigFromDump(%s) expected data, got nil", tt.configType)
		}
	}
}

func TestExtractConfigFromDump_InvalidJSON(t *testing.T) {
	result := ExtractConfigFromDump([]byte("not json"), ConfigBootstrap)
	if result != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestExtractConfigFromDump_MissingConfigs(t *testing.T) {
	dump := map[string]interface{}{"other": "data"}
	dumpJSON, _ := json.Marshal(dump)
	result := ExtractConfigFromDump(dumpJSON, ConfigBootstrap)
	if result != nil {
		t.Error("expected nil for missing configs key")
	}
}

func TestTimestampedConfig_JSON(t *testing.T) {
	tc := TimestampedConfig{
		PodName:    "test-pod",
		Namespace:  "default",
		ConfigType: "cluster",
		Config:     json.RawMessage(`{"test": true}`),
	}
	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("Failed to marshal TimestampedConfig: %v", err)
	}

	var decoded TimestampedConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal TimestampedConfig: %v", err)
	}
	if decoded.PodName != "test-pod" {
		t.Errorf("PodName = %q, want test-pod", decoded.PodName)
	}
	if decoded.Namespace != "default" {
		t.Errorf("Namespace = %q, want default", decoded.Namespace)
	}
	if decoded.ConfigType != "cluster" {
		t.Errorf("ConfigType = %q, want cluster", decoded.ConfigType)
	}
}

func TestParseNodeID(t *testing.T) {
	tests := []struct {
		nodeID    string
		wantPod   string
		wantNS    string
	}{
		{"sidecar~10.0.0.1~httpbin-abc.default~default.svc.cluster.local", "httpbin-abc", "default"},
		{"sidecar~10.0.0.1~sleep-xyz.istio-system~istio-system.svc.cluster.local", "sleep-xyz", "istio-system"},
		{"invalid", "", ""},
		{"a~b", "", ""},
		{"sidecar~ip~podonly", "podonly", ""},
	}
	for _, tt := range tests {
		pod, ns := parseNodeID(tt.nodeID)
		if pod != tt.wantPod {
			t.Errorf("parseNodeID(%q) pod = %q, want %q", tt.nodeID, pod, tt.wantPod)
		}
		if ns != tt.wantNS {
			t.Errorf("parseNodeID(%q) ns = %q, want %q", tt.nodeID, ns, tt.wantNS)
		}
	}
}
