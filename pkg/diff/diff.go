package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ComputeJSON is like Compute but unmarshals JSON and removes ignorePaths keys
// (dot-separated, e.g. "last_updated") before diffing, so transient metadata
// fields don't trigger spurious diffs.
func ComputeJSON(old, new []byte, ignorePaths []string) string {
	if len(ignorePaths) == 0 {
		return Compute(old, new)
	}
	stripped := func(data []byte) []byte {
		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			return data
		}
		deleteKeys(m, ignorePaths)
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return data
		}
		return out
	}
	return Compute(stripped(old), stripped(new))
}

// deleteKeys removes dot-separated paths from a nested map.
func deleteKeys(m map[string]interface{}, paths []string) {
	for _, path := range paths {
		parts := strings.SplitN(path, ".", 2)
		if len(parts) == 1 {
			delete(m, parts[0])
		} else {
			if sub, ok := m[parts[0]].(map[string]interface{}); ok {
				deleteKeys(sub, []string{parts[1]})
			}
		}
	}
}

// Compute returns a unified-style diff between old and new byte slices.
// Returns an empty string if the contents are identical.
func Compute(old, new []byte) string {
	if bytes.Equal(old, new) {
		return ""
	}

	oldLines := strings.Split(string(old), "\n")
	newLines := strings.Split(string(new), "\n")

	return unifiedDiff(oldLines, newLines)
}

// unifiedDiff produces a simple unified diff output.
func unifiedDiff(a, b []string) string {
	// Use a simple LCS-based diff
	lcs := computeLCS(a, b)

	var result strings.Builder
	result.WriteString("--- previous\n")
	result.WriteString("+++ current\n")

	ai, bi, li := 0, 0, 0
	hasChanges := false

	for ai < len(a) || bi < len(b) {
		if li < len(lcs) && ai < len(a) && a[ai] == lcs[li] && bi < len(b) && b[bi] == lcs[li] {
			// Common line
			ai++
			bi++
			li++
		} else if li < len(lcs) && ai < len(a) && a[ai] != lcs[li] {
			result.WriteString(fmt.Sprintf("- %s\n", a[ai]))
			ai++
			hasChanges = true
		} else if li < len(lcs) && bi < len(b) && b[bi] != lcs[li] {
			result.WriteString(fmt.Sprintf("+ %s\n", b[bi]))
			bi++
			hasChanges = true
		} else if li >= len(lcs) && ai < len(a) {
			result.WriteString(fmt.Sprintf("- %s\n", a[ai]))
			ai++
			hasChanges = true
		} else if li >= len(lcs) && bi < len(b) {
			result.WriteString(fmt.Sprintf("+ %s\n", b[bi]))
			bi++
			hasChanges = true
		} else {
			break
		}
	}

	if !hasChanges {
		return ""
	}
	return result.String()
}

// computeLCS computes the longest common subsequence of two string slices.
func computeLCS(a, b []string) []string {
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack to find LCS
	lcs := make([]string, 0, dp[m][n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			lcs = append([]string{a[i-1]}, lcs...)
			i--
			j--
		} else if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}

	return lcs
}
