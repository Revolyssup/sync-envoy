package xcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteXCPCorrelation_CreatesFile(t *testing.T) {
	xcpDir := t.TempDir()

	refs := []IstioOutputRef{
		{Kind: "VirtualService", Name: "foo", Namespace: "default", FilePath: "istioconfigs/default/virtualservice/foo_current.yaml"},
		{Kind: "DestinationRule", Name: "foo", Namespace: "default", FilePath: "istioconfigs/default/destinationrule/foo_current.yaml"},
	}

	if err := WriteXCPCorrelation(xcpDir, "default", "serviceroute", "foo", refs); err != nil {
		t.Fatalf("WriteXCPCorrelation failed: %v", err)
	}

	corrPath := filepath.Join(xcpDir, "default", "serviceroute", "correlation.json")
	raw, err := os.ReadFile(corrPath)
	if err != nil {
		t.Fatalf("correlation.json not written: %v", err)
	}

	var corr XCPCorrelation
	if err := json.Unmarshal(raw, &corr); err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	entries, ok := corr["foo"]
	if !ok {
		t.Fatal("expected key 'foo'")
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(entries))
	}
	if entries[0].Kind != "VirtualService" {
		t.Errorf("kind: got %q", entries[0].Kind)
	}
}

func TestWriteXCPCorrelation_Upsert(t *testing.T) {
	xcpDir := t.TempDir()

	refs1 := []IstioOutputRef{{Kind: "VirtualService", Name: "foo", Namespace: "default"}}
	refs2 := []IstioOutputRef{{Kind: "VirtualService", Name: "foo", Namespace: "default"}, {Kind: "DestinationRule", Name: "foo", Namespace: "default"}}

	WriteXCPCorrelation(xcpDir, "default", "serviceroute", "foo", refs1)
	WriteXCPCorrelation(xcpDir, "default", "serviceroute", "foo", refs2)

	raw, _ := os.ReadFile(filepath.Join(xcpDir, "default", "serviceroute", "correlation.json"))
	var corr XCPCorrelation
	json.Unmarshal(raw, &corr)

	if len(corr["foo"]) != 2 {
		t.Errorf("expected 2 refs after upsert, got %d", len(corr["foo"]))
	}
}

func TestWriteXCPReverseCorrelation_CreatesFile(t *testing.T) {
	istioDir := t.TempDir()

	istioRef := IstioOutputRef{Kind: "VirtualService", Name: "foo", Namespace: "default"}
	xcpRef := XCPResourceRef{Kind: "ServiceRoute", Name: "foo", Namespace: "default", Workspace: "ws1"}

	if err := WriteXCPReverseCorrelation(istioDir, istioRef, xcpRef); err != nil {
		t.Fatalf("WriteXCPReverseCorrelation failed: %v", err)
	}

	corrPath := filepath.Join(istioDir, "default", "virtualservice", "xcp-correlation.json")
	raw, err := os.ReadFile(corrPath)
	if err != nil {
		t.Fatalf("xcp-correlation.json not written: %v", err)
	}

	var corr XCPReverseCorrelation
	if err := json.Unmarshal(raw, &corr); err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	entries, ok := corr["foo"]
	if !ok || len(entries) != 1 {
		t.Fatalf("expected 1 entry for 'foo', got %v", corr)
	}
	if entries[0].Kind != "ServiceRoute" {
		t.Errorf("kind: got %q", entries[0].Kind)
	}
	if entries[0].Workspace != "ws1" {
		t.Errorf("workspace: got %q", entries[0].Workspace)
	}
}

func TestWriteXCPReverseCorrelation_Upsert(t *testing.T) {
	istioDir := t.TempDir()

	istioRef := IstioOutputRef{Kind: "VirtualService", Name: "bar", Namespace: "default"}
	xcpRef1 := XCPResourceRef{Kind: "ServiceRoute", Name: "bar", Namespace: "default"}
	xcpRef2 := XCPResourceRef{Kind: "TrafficSetting", Name: "ts", Namespace: "default"}

	WriteXCPReverseCorrelation(istioDir, istioRef, xcpRef1)
	WriteXCPReverseCorrelation(istioDir, istioRef, xcpRef2)
	// Re-write xcpRef1 — should upsert, not duplicate
	WriteXCPReverseCorrelation(istioDir, istioRef, xcpRef1)

	raw, _ := os.ReadFile(filepath.Join(istioDir, "default", "virtualservice", "xcp-correlation.json"))
	var corr XCPReverseCorrelation
	json.Unmarshal(raw, &corr)

	if len(corr["bar"]) != 2 {
		t.Errorf("expected 2 entries (deduped), got %d: %+v", len(corr["bar"]), corr["bar"])
	}
}

func TestRemoveXCPCorrelation(t *testing.T) {
	xcpDir := t.TempDir()

	refs := []IstioOutputRef{{Kind: "VirtualService", Name: "foo", Namespace: "default"}}
	WriteXCPCorrelation(xcpDir, "default", "serviceroute", "foo", refs)
	WriteXCPCorrelation(xcpDir, "default", "serviceroute", "bar", refs)

	RemoveXCPCorrelation(xcpDir, "default", "serviceroute", "foo")

	raw, _ := os.ReadFile(filepath.Join(xcpDir, "default", "serviceroute", "correlation.json"))
	var corr XCPCorrelation
	json.Unmarshal(raw, &corr)

	if _, ok := corr["foo"]; ok {
		t.Error("expected 'foo' to be removed")
	}
	if _, ok := corr["bar"]; !ok {
		t.Error("expected 'bar' to remain")
	}
}
