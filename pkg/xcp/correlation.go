package xcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IstioOutputRef identifies an Istio resource produced by an XCP resource.
type IstioOutputRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	FilePath  string `json:"file_path"` // relative path under istioconfigs/
}

// XCPResourceRef identifies an XCP source resource.
type XCPResourceRef struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Group     string `json:"group,omitempty"` // trafficGroup/securityGroup/gatewayGroup value
}

// XCPCorrelation is written as correlation.json inside xcpconfigs/<ns>/<kind>/.
// Key = XCP resource name, value = list of Istio resources it produced.
type XCPCorrelation map[string][]IstioOutputRef

// XCPReverseCorrelation is written as xcp-correlation.json inside istioconfigs/<ns>/<kind>/.
// Key = Istio resource name, value = list of XCP resources that produced it.
type XCPReverseCorrelation map[string][]XCPResourceRef

// WriteXCPCorrelation writes/upserts correlation.json in xcpconfigs/<ns>/<kind>/
// mapping the XCP resource to its produced Istio resources.
func WriteXCPCorrelation(xcpBasePath, ns, kind, name string, istioRefs []IstioOutputRef) error {
	dir := filepath.Join(xcpBasePath, ns, strings.ToLower(kind))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	corrPath := filepath.Join(dir, "correlation.json")
	corr := make(XCPCorrelation)
	if raw, err := os.ReadFile(corrPath); err == nil {
		json.Unmarshal(raw, &corr)
	}

	corr[name] = istioRefs

	data, err := json.MarshalIndent(corr, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(corrPath, data, 0644)
}

// WriteXCPReverseCorrelation writes/upserts xcp-correlation.json in
// istioconfigs/<ns>/<istioKind>/ mapping the Istio resource back to its XCP source.
func WriteXCPReverseCorrelation(istioBasePath string, istioRef IstioOutputRef, xcpRef XCPResourceRef) error {
	dir := filepath.Join(istioBasePath, istioRef.Namespace, strings.ToLower(istioRef.Kind))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	corrPath := filepath.Join(dir, "xcp-correlation.json")
	corr := make(XCPReverseCorrelation)
	if raw, err := os.ReadFile(corrPath); err == nil {
		json.Unmarshal(raw, &corr)
	}

	// Upsert: replace if same Kind+Namespace+Name, else append
	entries := corr[istioRef.Name]
	updated := false
	for i, e := range entries {
		if e.Kind == xcpRef.Kind && e.Namespace == xcpRef.Namespace && e.Name == xcpRef.Name {
			entries[i] = xcpRef
			updated = true
			break
		}
	}
	if !updated {
		entries = append(entries, xcpRef)
	}
	corr[istioRef.Name] = entries

	data, err := json.MarshalIndent(corr, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(corrPath, data, 0644)
}

// RemoveXCPCorrelation removes an XCP resource's entry from correlation.json
// when the resource is deleted.
func RemoveXCPCorrelation(xcpBasePath, ns, kind, name string) {
	corrPath := filepath.Join(xcpBasePath, ns, strings.ToLower(kind), "correlation.json")
	raw, err := os.ReadFile(corrPath)
	if err != nil {
		return
	}
	var corr XCPCorrelation
	if err := json.Unmarshal(raw, &corr); err != nil {
		return
	}
	delete(corr, name)
	if len(corr) == 0 {
		os.Remove(corrPath)
		return
	}
	data, _ := json.MarshalIndent(corr, "", "  ")
	os.WriteFile(corrPath, data, 0644)
}
