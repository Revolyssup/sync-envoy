package types

import "testing"

func TestEventType_String(t *testing.T) {
	tests := []struct {
		et   EventType
		want string
	}{
		{EventAdd, "ADD"},
		{EventUpdate, "UPDATE"},
		{EventDelete, "DELETE"},
		{EventType(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.et.String(); got != tt.want {
			t.Errorf("EventType(%d).String() = %q, want %q", tt.et, got, tt.want)
		}
	}
}

func TestEvent_Fields(t *testing.T) {
	e := Event{
		Type:    EventUpdate,
		Key:     "default/virtualservice/httpbin",
		NewData: []byte("test"),
		Metadata: map[string]string{
			"kind": "VirtualService",
		},
	}
	if e.Type != EventUpdate {
		t.Errorf("expected EventUpdate, got %v", e.Type)
	}
	if e.Key != "default/virtualservice/httpbin" {
		t.Errorf("unexpected key: %s", e.Key)
	}
	if e.Metadata["kind"] != "VirtualService" {
		t.Errorf("unexpected metadata: %v", e.Metadata)
	}
}
