package diff

import "testing"

func TestCompute_Identical(t *testing.T) {
	data := []byte("line1\nline2\nline3\n")
	result := Compute(data, data)
	if result != "" {
		t.Errorf("expected empty diff for identical data, got: %q", result)
	}
}

func TestCompute_Empty(t *testing.T) {
	result := Compute(nil, nil)
	if result != "" {
		t.Errorf("expected empty diff for nil data, got: %q", result)
	}
}

func TestCompute_Added(t *testing.T) {
	old := []byte("line1\nline2\n")
	new := []byte("line1\nline2\nline3\n")
	result := Compute(old, new)
	if result == "" {
		t.Error("expected non-empty diff when lines are added")
	}
	if !containsString(result, "+ line3") {
		t.Errorf("expected added line in diff, got:\n%s", result)
	}
}

func TestCompute_Removed(t *testing.T) {
	old := []byte("line1\nline2\nline3\n")
	new := []byte("line1\nline2\n")
	result := Compute(old, new)
	if result == "" {
		t.Error("expected non-empty diff when lines are removed")
	}
	if !containsString(result, "- line3") {
		t.Errorf("expected removed line in diff, got:\n%s", result)
	}
}

func TestCompute_Modified(t *testing.T) {
	old := []byte("line1\nold_line\nline3\n")
	new := []byte("line1\nnew_line\nline3\n")
	result := Compute(old, new)
	if result == "" {
		t.Error("expected non-empty diff when lines are modified")
	}
	if !containsString(result, "- old_line") {
		t.Errorf("expected removed old line in diff, got:\n%s", result)
	}
	if !containsString(result, "+ new_line") {
		t.Errorf("expected added new line in diff, got:\n%s", result)
	}
}

func TestComputeJSON_IgnorePath(t *testing.T) {
	old := []byte(`{"last_updated":"2026-03-13T10:00:00Z","config":"v1"}`)
	new := []byte(`{"last_updated":"2026-03-13T10:00:05Z","config":"v1"}`)

	// Without ignore: should detect diff
	if Compute(old, new) == "" {
		t.Error("expected diff when last_updated changes without ignore")
	}

	// With ignore: should be empty
	if result := ComputeJSON(old, new, []string{"last_updated"}); result != "" {
		t.Errorf("expected no diff when last_updated is ignored, got:\n%s", result)
	}
}

func TestComputeJSON_DetectsRealChange(t *testing.T) {
	old := []byte(`{"last_updated":"2026-03-13T10:00:00Z","config":"v1"}`)
	new := []byte(`{"last_updated":"2026-03-13T10:00:05Z","config":"v2"}`)

	if result := ComputeJSON(old, new, []string{"last_updated"}); result == "" {
		t.Error("expected diff when config changes even with last_updated ignored")
	}
}

func TestComputeLCS(t *testing.T) {
	a := []string{"a", "b", "c", "d"}
	b := []string{"a", "c", "d", "e"}
	lcs := computeLCS(a, b)
	expected := []string{"a", "c", "d"}
	if len(lcs) != len(expected) {
		t.Fatalf("LCS length: got %d, want %d", len(lcs), len(expected))
	}
	for i, v := range lcs {
		if v != expected[i] {
			t.Errorf("LCS[%d]: got %q, want %q", i, v, expected[i])
		}
	}
}

func containsString(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 && indexOf(haystack, needle) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
