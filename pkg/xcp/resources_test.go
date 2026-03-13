package xcp

import "testing"

func TestXCPResourceTypes_Count(t *testing.T) {
	if got := len(XCPResourceTypes); got < 20 {
		t.Errorf("expected at least 20 XCP GVRs, got %d", got)
	}
}

func TestXCPToIstioMapping_ServiceRoute(t *testing.T) {
	kinds, ok := XCPToIstioMapping["serviceroute"]
	if !ok {
		t.Fatal("serviceroute not in mapping")
	}
	want := map[string]bool{"VirtualService": true, "DestinationRule": true}
	for _, k := range kinds {
		if !want[k] {
			t.Errorf("unexpected kind %q for serviceroute", k)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("missing kind %q for serviceroute", k)
	}
}

func TestXCPToIstioMapping_SecuritySetting(t *testing.T) {
	kinds, ok := XCPToIstioMapping["securitysetting"]
	if !ok {
		t.Fatal("securitysetting not in mapping")
	}
	want := map[string]bool{"PeerAuthentication": true, "AuthorizationPolicy": true}
	for _, k := range kinds {
		if !want[k] {
			t.Errorf("unexpected kind %q for securitysetting", k)
		}
		delete(want, k)
	}
	for k := range want {
		t.Errorf("missing kind %q for securitysetting", k)
	}
}

func TestIstioGVRForKind(t *testing.T) {
	gvr, ok := IstioGVRForKind("VirtualService")
	if !ok {
		t.Fatal("VirtualService not found")
	}
	if gvr.Resource != "virtualservices" {
		t.Errorf("resource: got %q, want virtualservices", gvr.Resource)
	}

	_, ok = IstioGVRForKind("NonExistent")
	if ok {
		t.Error("expected false for non-existent kind")
	}
}
