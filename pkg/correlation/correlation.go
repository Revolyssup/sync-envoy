package correlation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// IstioResourceRef identifies a source Istio CR.
type IstioResourceRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

func (r IstioResourceRef) key() string {
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}

// AffectedEnvoyResource describes a specific Envoy resource affected by an Istio CR.
type AffectedEnvoyResource struct {
	Type        string `json:"type"`                         // "cluster", "listener", "route"
	Name        string `json:"name"`                         // raw envoy resource name
	Direction   string `json:"direction,omitempty"`           // cluster: outbound/inbound
	Port        string `json:"port,omitempty"`                // cluster: port number
	Subset      string `json:"subset,omitempty"`              // cluster: subset name
	Service     string `json:"service,omitempty"`             // cluster/route: service name
	ServiceNS   string `json:"service_namespace,omitempty"`   // cluster: service namespace
	VirtualHost string `json:"virtual_host,omitempty"`        // route: virtual host name
}

// ClusterInfo holds parsed Envoy cluster name information.
// Istio uses the pattern: outbound|<port>|<subset>|<service>.<ns>.svc.cluster.local
type ClusterInfo struct {
	Direction string `json:"direction"`
	Port      string `json:"port"`
	Subset    string `json:"subset,omitempty"`
	Service   string `json:"service,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Raw       string `json:"raw"`
}

// PodCorrelation is the <kind>-correlation.json written alongside envoy config files.
// AffectedBy maps "Kind/Namespace/Name" → list of affected Envoy resources.
type PodCorrelation struct {
	Pod         string                             `json:"pod"`
	Namespace   string                             `json:"namespace"`
	AffectedBy  map[string][]AffectedEnvoyResource `json:"affected_by"`
	LastUpdated time.Time                          `json:"last_updated"`
}

// ParseClusterName parses an Envoy cluster name following Istio naming conventions.
func ParseClusterName(name string) ClusterInfo {
	info := ClusterInfo{Raw: name}
	parts := strings.SplitN(name, "|", 4)
	if len(parts) != 4 {
		return info
	}
	info.Direction = parts[0]
	info.Port = parts[1]
	info.Subset = parts[2]

	hostParts := strings.SplitN(parts[3], ".", 3)
	if len(hostParts) >= 2 {
		info.Service = hostParts[0]
		info.Namespace = hostParts[1]
	}
	return info
}

// ---------- cluster dump ----------

type clusterDump struct {
	DynamicActiveClusters []struct {
		Cluster struct {
			Name     string `json:"name"`
			Metadata struct {
				FilterMetadata struct {
					Istio struct {
						Config string `json:"config"`
						Subset string `json:"subset"`
					} `json:"istio"`
				} `json:"filter_metadata"`
			} `json:"metadata"`
		} `json:"cluster"`
	} `json:"dynamic_active_clusters"`
	StaticClusters []struct {
		Cluster struct {
			Name string `json:"name"`
		} `json:"cluster"`
	} `json:"static_clusters"`
}

// ExtractFromClusterDump returns a map from Istio resource key (Kind/NS/Name) to
// the list of affected Envoy clusters. Only clusters with an embedded istio config
// ref are included.
func ExtractFromClusterDump(data json.RawMessage) map[string][]AffectedEnvoyResource {
	var dump clusterDump
	if err := json.Unmarshal(data, &dump); err != nil {
		return nil
	}

	result := make(map[string][]AffectedEnvoyResource)
	seen := make(map[string]map[string]bool) // refKey → clusterName → seen

	for _, c := range dump.DynamicActiveClusters {
		name := c.Cluster.Name
		if name == "" {
			continue
		}
		path := c.Cluster.Metadata.FilterMetadata.Istio.Config
		if path == "" {
			continue
		}
		ref := parseConfigPath(path)
		if ref == nil {
			continue
		}

		rk := ref.key()
		if seen[rk] == nil {
			seen[rk] = make(map[string]bool)
		}
		if seen[rk][name] {
			continue
		}
		seen[rk][name] = true

		ci := ParseClusterName(name)
		result[rk] = append(result[rk], AffectedEnvoyResource{
			Type:      "cluster",
			Name:      name,
			Direction: ci.Direction,
			Port:      ci.Port,
			Subset:    ci.Subset,
			Service:   ci.Service,
			ServiceNS: ci.Namespace,
		})
	}
	return result
}

// ---------- route dump ----------

type routeDump struct {
	DynamicRouteConfigs []struct {
		RouteConfig struct {
			Name         string `json:"name"`
			VirtualHosts []struct {
				Name   string `json:"name"`
				Routes []struct {
					Metadata struct {
						FilterMetadata struct {
							Istio struct {
								Config string `json:"config"`
							} `json:"istio"`
						} `json:"filter_metadata"`
					} `json:"metadata"`
				} `json:"routes"`
			} `json:"virtual_hosts"`
		} `json:"route_config"`
	} `json:"dynamic_route_configs"`
}

// ExtractFromRouteDump returns a map from Istio resource key to affected route entries.
func ExtractFromRouteDump(data json.RawMessage) map[string][]AffectedEnvoyResource {
	var dump routeDump
	if err := json.Unmarshal(data, &dump); err != nil {
		return nil
	}

	result := make(map[string][]AffectedEnvoyResource)
	// Deduplicate by refKey + routeConfigName + virtualHostName
	type dedupKey struct{ refKey, rc, vh string }
	seen := make(map[dedupKey]bool)

	for _, drc := range dump.DynamicRouteConfigs {
		rcName := drc.RouteConfig.Name
		for _, vh := range drc.RouteConfig.VirtualHosts {
			for _, r := range vh.Routes {
				path := r.Metadata.FilterMetadata.Istio.Config
				if path == "" {
					continue
				}
				ref := parseConfigPath(path)
				if ref == nil {
					continue
				}
				rk := ref.key()
				dk := dedupKey{rk, rcName, vh.Name}
				if seen[dk] {
					continue
				}
				seen[dk] = true

				result[rk] = append(result[rk], AffectedEnvoyResource{
					Type:        "route",
					Name:        rcName,
					VirtualHost: vh.Name,
				})
			}
		}
	}
	return result
}

// ---------- listener dump ----------

type listenerDump struct {
	DynamicListeners []struct {
		Name     string `json:"name"`
		Listener struct {
			Metadata struct {
				FilterMetadata struct {
					Istio struct {
						Config string `json:"config"`
					} `json:"istio"`
				} `json:"filter_metadata"`
			} `json:"metadata"`
		} `json:"listener"`
	} `json:"dynamic_listeners"`
}

// ExtractFromListenerDump returns a map from Istio resource key to affected listener entries.
func ExtractFromListenerDump(data json.RawMessage) map[string][]AffectedEnvoyResource {
	var dump listenerDump
	if err := json.Unmarshal(data, &dump); err != nil {
		return nil
	}

	result := make(map[string][]AffectedEnvoyResource)
	seen := make(map[string]map[string]bool) // refKey → listenerName → seen

	for _, dl := range dump.DynamicListeners {
		path := dl.Listener.Metadata.FilterMetadata.Istio.Config
		if path == "" {
			continue
		}
		ref := parseConfigPath(path)
		if ref == nil {
			continue
		}
		rk := ref.key()
		if seen[rk] == nil {
			seen[rk] = make(map[string]bool)
		}
		lName := dl.Name
		if seen[rk][lName] {
			continue
		}
		seen[rk][lName] = true

		result[rk] = append(result[rk], AffectedEnvoyResource{
			Type: "listener",
			Name: lName,
		})
	}
	return result
}

// RefsFromAffectedMap derives a flat IstioResourceRef list from an affected-by map.
func RefsFromAffectedMap(m map[string][]AffectedEnvoyResource) []IstioResourceRef {
	refs := make([]IstioResourceRef, 0, len(m))
	for key := range m {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) == 3 {
			refs = append(refs, IstioResourceRef{Kind: parts[0], Namespace: parts[1], Name: parts[2]})
		}
	}
	return refs
}

// ExtractListenerNamesFromFile reads a listener.json file (TimestampedConfig wrapper)
// and returns all dynamic listener names as AffectedEnvoyResource entries.
func ExtractListenerNamesFromFile(filePath string) []AffectedEnvoyResource {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	// listener.json is wrapped in TimestampedConfig; extract the inner "config" field.
	var wrapper struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil
	}

	var dump listenerDump
	if err := json.Unmarshal(wrapper.Config, &dump); err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var result []AffectedEnvoyResource
	for _, dl := range dump.DynamicListeners {
		if dl.Name == "" || seen[dl.Name] {
			continue
		}
		seen[dl.Name] = true
		result = append(result, AffectedEnvoyResource{
			Type: "listener",
			Name: dl.Name,
		})
	}
	return result
}

// ---------- config path parsing ----------

func parseConfigPath(path string) *IstioResourceRef {
	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(path, "/")

	for i, p := range parts {
		if p == "namespaces" && i+3 < len(parts) {
			return &IstioResourceRef{
				Namespace: parts[i+1],
				Kind:      kebabToKind(parts[i+2]),
				Name:      parts[i+3],
			}
		}
	}

	if len(parts) >= 2 {
		return &IstioResourceRef{
			Kind: kebabToKind(parts[len(parts)-2]),
			Name: parts[len(parts)-1],
		}
	}
	return nil
}

func kebabToKind(s string) string {
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// ---------- envoyconfigs-side: per-pod per-kind correlation file ----------

// UpsertPodSourceRef adds or updates a source Istio resource ref in a pod's
// <kind>-correlation.json inside envoyconfigs.
// affected is the list of Envoy resources affected by this ref.
func UpsertPodSourceRef(envoyBaseDir, podName, namespace string, ref IstioResourceRef, affected []AffectedEnvoyResource) error {
	corrPath := filepath.Join(envoyBaseDir, namespace, podName, strings.ToLower(ref.Kind)+"-correlation.json")

	var pc PodCorrelation
	if raw, err := os.ReadFile(corrPath); err == nil {
		json.Unmarshal(raw, &pc) //nolint:errcheck
	} else if !os.IsNotExist(err) {
		return err
	}

	if pc.Pod == "" {
		pc.Pod = podName
		pc.Namespace = namespace
	}
	if pc.AffectedBy == nil {
		pc.AffectedBy = make(map[string][]AffectedEnvoyResource)
	}

	pc.AffectedBy[ref.key()] = affected
	pc.LastUpdated = time.Now()

	if err := os.MkdirAll(filepath.Dir(corrPath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(corrPath, data, 0644)
}

// ---------- istioconfigs-side: reverse-index ----------

// EnvoyRef points to an Envoy config file affected by an Istio resource.
type EnvoyRef struct {
	Pod        string `json:"pod"`
	Namespace  string `json:"namespace"`
	ConfigType string `json:"config_type"`
	File       string `json:"file"`
}

// IstioCorrelation is the reverse-index written as correlation.json inside each
// istioconfigs/<ns>/<kind>/ directory.  Key = resource name, value = Envoy configs affected.
type IstioCorrelation map[string][]EnvoyRef

// WriteIstioCorrelation upserts correlation.json in every Istio resource directory
// referenced by refs, recording that podName's configType file is derived from each resource.
func WriteIstioCorrelation(
	istioconfigsPath string,
	refs []IstioResourceRef,
	podName, podNS, configType, envoyBaseDir string,
) error {
	envoyFile := filepath.Join(envoyBaseDir, podNS, podName, configType+".json")
	incoming := EnvoyRef{Pod: podName, Namespace: podNS, ConfigType: configType, File: envoyFile}

	for _, ref := range refs {
		var dir string
		if ref.Namespace == "" {
			dir = filepath.Join(istioconfigsPath, strings.ToLower(ref.Kind))
		} else {
			dir = filepath.Join(istioconfigsPath, ref.Namespace, strings.ToLower(ref.Kind))
		}

		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}

		corrPath := filepath.Join(dir, "correlation.json")

		corr := make(IstioCorrelation)
		if raw, err := os.ReadFile(corrPath); err == nil {
			json.Unmarshal(raw, &corr) //nolint:errcheck – best-effort read
		}

		entries := corr[ref.Name]
		updated := false
		for i, e := range entries {
			if e.Pod == incoming.Pod && e.ConfigType == incoming.ConfigType {
				entries[i] = incoming
				updated = true
				break
			}
		}
		if !updated {
			entries = append(entries, incoming)
		}
		corr[ref.Name] = entries

		data, err := json.MarshalIndent(corr, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(corrPath, data, 0644); err != nil {
			return fmt.Errorf("write %s: %w", corrPath, err)
		}
	}
	return nil
}
