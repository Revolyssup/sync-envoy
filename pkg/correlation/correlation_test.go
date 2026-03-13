package correlation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseClusterName_Outbound(t *testing.T) {
	ci := ParseClusterName("outbound|9080|v1|reviews.default.svc.cluster.local")
	if ci.Direction != "outbound" {
		t.Errorf("direction: got %q, want %q", ci.Direction, "outbound")
	}
	if ci.Port != "9080" {
		t.Errorf("port: got %q, want %q", ci.Port, "9080")
	}
	if ci.Subset != "v1" {
		t.Errorf("subset: got %q, want %q", ci.Subset, "v1")
	}
	if ci.Service != "reviews" {
		t.Errorf("service: got %q, want %q", ci.Service, "reviews")
	}
	if ci.Namespace != "default" {
		t.Errorf("namespace: got %q, want %q", ci.Namespace, "default")
	}
	if ci.Raw != "outbound|9080|v1|reviews.default.svc.cluster.local" {
		t.Errorf("raw: got %q", ci.Raw)
	}
}

func TestParseClusterName_Inbound(t *testing.T) {
	ci := ParseClusterName("inbound|8080||httpbin.default.svc.cluster.local")
	if ci.Direction != "inbound" {
		t.Errorf("direction: got %q, want %q", ci.Direction, "inbound")
	}
	if ci.Subset != "" {
		t.Errorf("subset should be empty, got %q", ci.Subset)
	}
	if ci.Service != "httpbin" {
		t.Errorf("service: got %q, want %q", ci.Service, "httpbin")
	}
}

func TestParseClusterName_NoSubset(t *testing.T) {
	ci := ParseClusterName("outbound|80||httpbin.default.svc.cluster.local")
	if ci.Subset != "" {
		t.Errorf("expected empty subset, got %q", ci.Subset)
	}
	if ci.Service != "httpbin" {
		t.Errorf("service: got %q, want %q", ci.Service, "httpbin")
	}
}

func TestParseClusterName_Unrecognized(t *testing.T) {
	ci := ParseClusterName("BlackHoleCluster")
	if ci.Direction != "" {
		t.Errorf("expected empty direction for unrecognized name, got %q", ci.Direction)
	}
	if ci.Raw != "BlackHoleCluster" {
		t.Errorf("raw: got %q", ci.Raw)
	}
}

func TestParseConfigPath_Namespaced(t *testing.T) {
	path := "/apis/networking.istio.io/v1alpha3/namespaces/default/virtual-service/reviews"
	ref := parseConfigPath(path)
	if ref == nil {
		t.Fatal("expected non-nil ref")
	}
	if ref.Kind != "VirtualService" {
		t.Errorf("kind: got %q, want VirtualService", ref.Kind)
	}
	if ref.Name != "reviews" {
		t.Errorf("name: got %q, want reviews", ref.Name)
	}
	if ref.Namespace != "default" {
		t.Errorf("namespace: got %q, want default", ref.Namespace)
	}
}

func TestParseConfigPath_DestinationRule(t *testing.T) {
	path := "/apis/networking.istio.io/v1alpha3/namespaces/prod/destination-rule/httpbin"
	ref := parseConfigPath(path)
	if ref == nil {
		t.Fatal("expected non-nil ref")
	}
	if ref.Kind != "DestinationRule" {
		t.Errorf("kind: got %q, want DestinationRule", ref.Kind)
	}
	if ref.Namespace != "prod" {
		t.Errorf("namespace: got %q, want prod", ref.Namespace)
	}
}

func TestParseConfigPath_NonNamespaced(t *testing.T) {
	path := "/apis/networking.istio.io/v1alpha3/gateway/my-gw"
	ref := parseConfigPath(path)
	if ref == nil {
		t.Fatal("expected non-nil ref")
	}
	if ref.Kind != "Gateway" {
		t.Errorf("kind: got %q, want Gateway", ref.Kind)
	}
	if ref.Name != "my-gw" {
		t.Errorf("name: got %q, want my-gw", ref.Name)
	}
}

func TestKebabToKind(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"virtual-service", "VirtualService"},
		{"destination-rule", "DestinationRule"},
		{"gateway", "Gateway"},
		{"peer-authentication", "PeerAuthentication"},
	}
	for _, c := range cases {
		got := kebabToKind(c.in)
		if got != c.want {
			t.Errorf("kebabToKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractFromClusterDump(t *testing.T) {
	dumpJSON := `{
		"dynamic_active_clusters": [
			{
				"cluster": {
					"name": "outbound|9080|v1|reviews.default.svc.cluster.local",
					"metadata": {
						"filter_metadata": {
							"istio": {
								"config": "/apis/networking.istio.io/v1alpha3/namespaces/default/virtual-service/reviews"
							}
						}
					}
				}
			},
			{
				"cluster": {
					"name": "outbound|9080|v2|reviews.default.svc.cluster.local",
					"metadata": {
						"filter_metadata": {
							"istio": {
								"config": "/apis/networking.istio.io/v1alpha3/namespaces/default/virtual-service/reviews"
							}
						}
					}
				}
			},
			{
				"cluster": {
					"name": "outbound|8000||httpbin.default.svc.cluster.local",
					"metadata": {
						"filter_metadata": {
							"istio": {
								"config": "/apis/networking.istio.io/v1alpha3/namespaces/default/destination-rule/httpbin"
							}
						}
					}
				}
			}
		],
		"static_clusters": [
			{"cluster": {"name": "BlackHoleCluster"}},
			{"cluster": {"name": "PassthroughCluster"}}
		]
	}`

	affectedMap := ExtractFromClusterDump(json.RawMessage(dumpJSON))

	// Two source refs: reviews VS and httpbin DR
	if len(affectedMap) != 2 {
		t.Fatalf("expected 2 ref keys, got %d: %v", len(affectedMap), affectedMap)
	}

	// reviews VS should have 2 clusters (v1 + v2)
	vsKey := "VirtualService/default/reviews"
	vsClusters := affectedMap[vsKey]
	if len(vsClusters) != 2 {
		t.Errorf("expected 2 clusters for %s, got %d", vsKey, len(vsClusters))
	}
	var foundV1 bool
	for _, c := range vsClusters {
		if c.Subset == "v1" && c.Service == "reviews" && c.Type == "cluster" {
			foundV1 = true
		}
	}
	if !foundV1 {
		t.Error("expected to find reviews v1 cluster")
	}

	// httpbin DR should have 1 cluster
	drKey := "DestinationRule/default/httpbin"
	drClusters := affectedMap[drKey]
	if len(drClusters) != 1 {
		t.Errorf("expected 1 cluster for %s, got %d", drKey, len(drClusters))
	}
}

func TestExtractFromClusterDump_InvalidJSON(t *testing.T) {
	affectedMap := ExtractFromClusterDump(json.RawMessage(`not json`))
	if affectedMap != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestExtractFromClusterDump_EmptyDump(t *testing.T) {
	affectedMap := ExtractFromClusterDump(json.RawMessage(`{}`))
	if len(affectedMap) != 0 {
		t.Errorf("expected empty map for empty dump, got %v", affectedMap)
	}
}

func TestExtractFromRouteDump(t *testing.T) {
	dumpJSON := `{
		"dynamic_route_configs": [
			{
				"route_config": {
					"name": "8080",
					"virtual_hosts": [
						{
							"name": "httpbin.default.svc.cluster.local:8080",
							"routes": [
								{
									"metadata": {
										"filter_metadata": {
											"istio": {
												"config": "/apis/networking.istio.io/v1alpha3/namespaces/default/virtual-service/httpbin-weighted"
											}
										}
									}
								},
								{
									"metadata": {
										"filter_metadata": {
											"istio": {
												"config": "/apis/networking.istio.io/v1alpha3/namespaces/default/virtual-service/httpbin-weighted"
											}
										}
									}
								}
							]
						}
					]
				}
			}
		]
	}`

	affectedMap := ExtractFromRouteDump(json.RawMessage(dumpJSON))
	vsKey := "VirtualService/default/httpbin-weighted"
	routes := affectedMap[vsKey]
	if len(routes) != 1 {
		t.Fatalf("expected 1 deduped route entry, got %d: %+v", len(routes), routes)
	}
	if routes[0].Type != "route" {
		t.Errorf("type: got %q, want route", routes[0].Type)
	}
	if routes[0].Name != "8080" {
		t.Errorf("name: got %q, want 8080", routes[0].Name)
	}
	if routes[0].VirtualHost != "httpbin.default.svc.cluster.local:8080" {
		t.Errorf("virtual_host: got %q", routes[0].VirtualHost)
	}
}

func TestExtractFromRouteDump_Empty(t *testing.T) {
	affectedMap := ExtractFromRouteDump(json.RawMessage(`{}`))
	if len(affectedMap) != 0 {
		t.Errorf("expected empty map for empty dump, got %+v", affectedMap)
	}
}

func TestExtractFromListenerDump(t *testing.T) {
	dumpJSON := `{
		"dynamic_listeners": [
			{
				"name": "0.0.0.0_8443",
				"listener": {
					"metadata": {
						"filter_metadata": {
							"istio": {
								"config": "/apis/networking.istio.io/v1alpha3/namespaces/istio-system/gateway/my-gateway"
							}
						}
					}
				}
			},
			{
				"name": "0.0.0.0_15006",
				"listener": {
					"metadata": {
						"filter_metadata": {
							"istio": {}
						}
					}
				}
			}
		]
	}`

	affectedMap := ExtractFromListenerDump(json.RawMessage(dumpJSON))
	gwKey := "Gateway/istio-system/my-gateway"
	listeners := affectedMap[gwKey]
	if len(listeners) != 1 {
		t.Fatalf("expected 1 listener, got %d: %+v", len(listeners), listeners)
	}
	if listeners[0].Type != "listener" {
		t.Errorf("type: got %q, want listener", listeners[0].Type)
	}
	if listeners[0].Name != "0.0.0.0_8443" {
		t.Errorf("name: got %q, want 0.0.0.0_8443", listeners[0].Name)
	}
}

func TestExtractListenerNamesFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	listenerFile := filepath.Join(tmpDir, "listener.json")

	// Simulate the TimestampedConfig wrapper that the envoy updater writes.
	content := `{
		"pod_name": "httpbin-abc",
		"namespace": "default",
		"config_type": "listener",
		"config": {
			"dynamic_listeners": [
				{"name": "0.0.0.0_8080"},
				{"name": "0.0.0.0_15006"},
				{"name": "0.0.0.0_15006"}
			]
		}
	}`
	os.WriteFile(listenerFile, []byte(content), 0644)

	result := ExtractListenerNamesFromFile(listenerFile)
	if len(result) != 2 {
		t.Fatalf("expected 2 unique listeners, got %d: %+v", len(result), result)
	}
	names := map[string]bool{}
	for _, r := range result {
		names[r.Name] = true
		if r.Type != "listener" {
			t.Errorf("type: got %q, want listener", r.Type)
		}
	}
	if !names["0.0.0.0_8080"] || !names["0.0.0.0_15006"] {
		t.Errorf("missing expected listener names: %v", names)
	}
}

func TestExtractListenerNamesFromFile_Missing(t *testing.T) {
	result := ExtractListenerNamesFromFile("/nonexistent/file.json")
	if result != nil {
		t.Errorf("expected nil for missing file, got %+v", result)
	}
}

func TestRefsFromAffectedMap(t *testing.T) {
	m := map[string][]AffectedEnvoyResource{
		"DestinationRule/default/httpbin": {{Type: "cluster", Name: "outbound|80||httpbin"}},
		"VirtualService/prod/reviews":     {{Type: "route", Name: "8080"}},
	}
	refs := RefsFromAffectedMap(m)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	kinds := map[string]bool{}
	for _, r := range refs {
		kinds[r.Kind] = true
	}
	if !kinds["DestinationRule"] || !kinds["VirtualService"] {
		t.Errorf("missing expected kinds: %v", kinds)
	}
}

func TestWriteIstioCorrelation_CreatesFile(t *testing.T) {
	istioDir := t.TempDir()
	envoyDir := t.TempDir()

	refs := []IstioResourceRef{
		{Kind: "VirtualService", Name: "reviews", Namespace: "default"},
		{Kind: "DestinationRule", Name: "reviews", Namespace: "default"},
	}

	if err := WriteIstioCorrelation(istioDir, refs, "reviews-abc", "default", "cluster", envoyDir); err != nil {
		t.Fatalf("WriteIstioCorrelation failed: %v", err)
	}

	vsPath := istioDir + "/default/virtualservice/correlation.json"
	raw, err := os.ReadFile(vsPath)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", vsPath, err)
	}
	var corr IstioCorrelation
	if err := json.Unmarshal(raw, &corr); err != nil {
		t.Fatalf("parse: %v", err)
	}
	entries, ok := corr["reviews"]
	if !ok || len(entries) != 1 {
		t.Fatalf("expected 1 entry for reviews, got %+v", corr)
	}
	if entries[0].Pod != "reviews-abc" {
		t.Errorf("pod: got %q, want reviews-abc", entries[0].Pod)
	}
	if entries[0].ConfigType != "cluster" {
		t.Errorf("config_type: got %q, want cluster", entries[0].ConfigType)
	}

	drPath := istioDir + "/default/destinationrule/correlation.json"
	if _, err := os.ReadFile(drPath); err != nil {
		t.Fatalf("expected %s to exist: %v", drPath, err)
	}
}

func TestWriteIstioCorrelation_Upsert(t *testing.T) {
	istioDir := t.TempDir()
	envoyDir := t.TempDir()

	refs := []IstioResourceRef{{Kind: "VirtualService", Name: "httpbin", Namespace: "default"}}

	WriteIstioCorrelation(istioDir, refs, "httpbin-aaa", "default", "cluster", envoyDir)
	WriteIstioCorrelation(istioDir, refs, "httpbin-bbb", "default", "cluster", envoyDir)
	WriteIstioCorrelation(istioDir, refs, "httpbin-aaa", "default", "cluster", envoyDir)

	raw, _ := os.ReadFile(istioDir + "/default/virtualservice/correlation.json")
	var corr IstioCorrelation
	json.Unmarshal(raw, &corr)

	entries := corr["httpbin"]
	if len(entries) != 2 {
		t.Errorf("expected 2 entries (aaa + bbb, deduped), got %d: %+v", len(entries), entries)
	}
}

func TestUpsertPodSourceRef(t *testing.T) {
	envoyDir := t.TempDir()
	os.MkdirAll(filepath.Join(envoyDir, "default", "httpbin-abc"), 0755)

	ref := IstioResourceRef{Kind: "AuthorizationPolicy", Name: "deny-all", Namespace: "default"}
	affected := []AffectedEnvoyResource{
		{Type: "listener", Name: "0.0.0.0_8080"},
		{Type: "listener", Name: "0.0.0.0_15006"},
	}

	if err := UpsertPodSourceRef(envoyDir, "httpbin-abc", "default", ref, affected); err != nil {
		t.Fatalf("UpsertPodSourceRef failed: %v", err)
	}

	corrPath := filepath.Join(envoyDir, "default", "httpbin-abc", "authorizationpolicy-correlation.json")
	raw, err := os.ReadFile(corrPath)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}

	var pc PodCorrelation
	if err := json.Unmarshal(raw, &pc); err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if pc.Pod != "httpbin-abc" {
		t.Errorf("pod: got %q", pc.Pod)
	}

	refKey := "AuthorizationPolicy/default/deny-all"
	entries, ok := pc.AffectedBy[refKey]
	if !ok {
		t.Fatalf("expected key %q in affected_by", refKey)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 affected listeners, got %d", len(entries))
	}
}
