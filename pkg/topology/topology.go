package topology

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Edge represents a directed relationship between resources.
type Edge struct {
	From     string // "Kind/Name"
	Relation string // relationship label (empty for default arrow)
	To       string // target description
}

// File maintains resource edges and writes topology.md.
type File struct {
	basePath string
	title    string
	edges    map[string][]Edge // key: "ns/kind/name"
	mu       sync.Mutex
}

// NewFile creates a topology file writer that writes topology.md to basePath.
func NewFile(basePath, title string) *File {
	return &File{
		basePath: basePath,
		title:    title,
		edges:    make(map[string][]Edge),
	}
}

// Set replaces all edges for a resource and rewrites topology.md.
func (f *File) Set(ns, kind, name string, edges []Edge) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := ns + "/" + kind + "/" + name
	if len(edges) == 0 {
		delete(f.edges, key)
	} else {
		f.edges[key] = edges
	}
	f.write()
}

// Remove deletes all edges for a resource and rewrites topology.md.
func (f *File) Remove(ns, kind, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.edges, ns+"/"+kind+"/"+name)
	f.write()
}

func (f *File) write() {
	path := filepath.Join(f.basePath, "topology.md")
	if len(f.edges) == 0 {
		os.Remove(path)
		return
	}

	// Group by namespace
	byNS := make(map[string][]Edge)
	for key, edges := range f.edges {
		ns := strings.SplitN(key, "/", 3)[0]
		byNS[ns] = append(byNS[ns], edges...)
	}

	var sb strings.Builder
	sb.WriteString("# " + f.title + "\n")

	nss := sortedKeys(byNS)
	for _, ns := range nss {
		sb.WriteString("\n## " + ns + "\n\n")
		sb.WriteString("```\n")
		edges := byNS[ns]
		sort.Slice(edges, func(i, j int) bool {
			if edges[i].From != edges[j].From {
				return edges[i].From < edges[j].From
			}
			if edges[i].Relation != edges[j].Relation {
				return edges[i].Relation < edges[j].Relation
			}
			return edges[i].To < edges[j].To
		})
		for _, e := range edges {
			if e.Relation != "" {
				fmt.Fprintf(&sb, "%s --[%s]--> %s\n", e.From, e.Relation, e.To)
			} else {
				fmt.Fprintf(&sb, "%s ---> %s\n", e.From, e.To)
			}
		}
		sb.WriteString("```\n")
	}

	os.MkdirAll(f.basePath, 0755)
	os.WriteFile(path, []byte(sb.String()), 0644)
}

func sortedKeys(m map[string][]Edge) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// xcpHierarchyLabels are the XCP label keys that show provenance on Istio resources.
var xcpHierarchyLabels = []string{
	"xcp.tetrate.io/gatewayGroup",
	"xcp.tetrate.io/trafficGroup",
	"xcp.tetrate.io/securityGroup",
}

// ExtractIstioEdges parses an Istio CR YAML and returns relationship edges.
func ExtractIstioEdges(kind, name, ns string, yamlData []byte) []Edge {
	var edges []Edge

	switch strings.ToLower(kind) {
	case "virtualservice":
		edges = extractVSEdges(name, yamlData)
	case "gateway":
		edges = extractGWEdges(name, yamlData)
	case "destinationrule":
		edges = extractDREdges(name, yamlData)
	case "authorizationpolicy":
		edges = extractSelectorEdges("AuthorizationPolicy", name, yamlData)
	case "peerauthentication":
		edges = extractSelectorEdges("PeerAuthentication", name, yamlData)
	case "requestauthentication":
		edges = extractSelectorEdges("RequestAuthentication", name, yamlData)
	case "serviceentry":
		edges = extractSEEdges(name, yamlData)
	}

	// Add XCP provenance if present
	edges = append(edges, extractXCPProvenance(kind, name, yamlData)...)

	return edges
}

func extractVSEdges(name string, data []byte) []Edge {
	var cr struct {
		Spec struct {
			Gateways []string `yaml:"gateways"`
			HTTP     []struct {
				Route []struct {
					Destination struct {
						Host string `yaml:"host"`
						Port struct {
							Number int `yaml:"number"`
						} `yaml:"port"`
					} `yaml:"destination"`
				} `yaml:"route"`
			} `yaml:"http"`
			TCP []struct {
				Route []struct {
					Destination struct {
						Host string `yaml:"host"`
						Port struct {
							Number int `yaml:"number"`
						} `yaml:"port"`
					} `yaml:"destination"`
				} `yaml:"route"`
			} `yaml:"tcp"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &cr); err != nil {
		return nil
	}

	from := "VirtualService/" + name
	var edges []Edge

	for _, gw := range cr.Spec.Gateways {
		if gw == "mesh" {
			continue
		}
		edges = append(edges, Edge{From: from, Relation: "gateway", To: "Gateway/" + gw})
	}

	seen := make(map[string]bool)
	addDest := func(host string, port int) {
		if host == "" || seen[host] {
			return
		}
		seen[host] = true
		target := host
		if port > 0 {
			target = fmt.Sprintf("%s:%d", host, port)
		}
		edges = append(edges, Edge{From: from, Relation: "route", To: target})
	}

	for _, rule := range cr.Spec.HTTP {
		for _, r := range rule.Route {
			addDest(r.Destination.Host, r.Destination.Port.Number)
		}
	}
	for _, rule := range cr.Spec.TCP {
		for _, r := range rule.Route {
			addDest(r.Destination.Host, r.Destination.Port.Number)
		}
	}

	return edges
}

func extractGWEdges(name string, data []byte) []Edge {
	var cr struct {
		Spec struct {
			Selector map[string]string `yaml:"selector"`
			Servers  []struct {
				Hosts []string `yaml:"hosts"`
				Port  struct {
					Number   int    `yaml:"number"`
					Protocol string `yaml:"protocol"`
				} `yaml:"port"`
			} `yaml:"servers"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &cr); err != nil {
		return nil
	}

	from := "Gateway/" + name
	var edges []Edge

	if len(cr.Spec.Selector) > 0 {
		var parts []string
		for k, v := range cr.Spec.Selector {
			parts = append(parts, k+"="+v)
		}
		sort.Strings(parts)
		edges = append(edges, Edge{From: from, Relation: "selector", To: strings.Join(parts, ", ")})
	}

	for _, srv := range cr.Spec.Servers {
		for _, host := range srv.Hosts {
			target := host
			if srv.Port.Number > 0 {
				target = fmt.Sprintf("%s:%d/%s", host, srv.Port.Number, srv.Port.Protocol)
			}
			edges = append(edges, Edge{From: from, Relation: "serves", To: target})
		}
	}

	return edges
}

func extractDREdges(name string, data []byte) []Edge {
	var cr struct {
		Spec struct {
			Host    string `yaml:"host"`
			Subsets []struct {
				Name   string            `yaml:"name"`
				Labels map[string]string `yaml:"labels"`
			} `yaml:"subsets"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &cr); err != nil {
		return nil
	}

	from := "DestinationRule/" + name
	var edges []Edge

	if cr.Spec.Host != "" {
		edges = append(edges, Edge{From: from, Relation: "host", To: cr.Spec.Host})
	}

	for _, subset := range cr.Spec.Subsets {
		var labelParts []string
		for k, v := range subset.Labels {
			labelParts = append(labelParts, k+"="+v)
		}
		sort.Strings(labelParts)
		target := subset.Name
		if len(labelParts) > 0 {
			target += " {" + strings.Join(labelParts, ", ") + "}"
		}
		edges = append(edges, Edge{From: from, Relation: "subset", To: target})
	}

	return edges
}

func extractSelectorEdges(kind, name string, data []byte) []Edge {
	var cr struct {
		Spec struct {
			Selector struct {
				MatchLabels map[string]string `yaml:"matchLabels"`
			} `yaml:"selector"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &cr); err != nil || len(cr.Spec.Selector.MatchLabels) == 0 {
		return nil
	}

	var parts []string
	for k, v := range cr.Spec.Selector.MatchLabels {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return []Edge{{
		From:     kind + "/" + name,
		Relation: "selector",
		To:       strings.Join(parts, ", "),
	}}
}

func extractSEEdges(name string, data []byte) []Edge {
	var cr struct {
		Spec struct {
			Hosts []string `yaml:"hosts"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &cr); err != nil {
		return nil
	}

	from := "ServiceEntry/" + name
	var edges []Edge
	for _, host := range cr.Spec.Hosts {
		edges = append(edges, Edge{From: from, Relation: "host", To: host})
	}
	return edges
}

func extractXCPProvenance(kind, name string, data []byte) []Edge {
	var meta struct {
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(data, &meta); err != nil || meta.Metadata.Labels == nil {
		return nil
	}

	ws := meta.Metadata.Labels["xcp.tetrate.io/workspace"]
	if ws == "" {
		return nil
	}

	target := "Workspace/" + ws
	for _, key := range xcpHierarchyLabels {
		if v, ok := meta.Metadata.Labels[key]; ok {
			parts := strings.Split(key, "/")
			groupKind := parts[len(parts)-1]
			// capitalize first letter
			groupKind = strings.ToUpper(groupKind[:1]) + groupKind[1:]
			target += " > " + groupKind + "/" + v
			break
		}
	}

	from := kind + "/" + name
	return []Edge{{From: from, Relation: "managed by", To: target}}
}
