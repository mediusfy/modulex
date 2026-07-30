package verify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mediusfy/modulex/provenance"
)

// RenderText renders results as a human-readable, multi-line summary,
// grouped by provenance.VerificationCategory, suitable for pasting into a
// PR comment or printing to a terminal. This is the "human-readable"
// counterpart to results' existing machine-readable form
// ([]provenance.VerificationResult, already JSON-marshalable as-is).
//
// Within each category, results are rendered in the order they appear in
// results (Run preserves the order of its input checks, so this is
// typically focused-checks-then-full-gates order); categories themselves
// are rendered in alphabetical order for stable output regardless of how
// results was assembled.
func RenderText(results []provenance.VerificationResult) string {
	if len(results) == 0 {
		return "no verification results\n"
	}

	byCategory := make(map[provenance.VerificationCategory][]provenance.VerificationResult)
	var categories []provenance.VerificationCategory
	for _, r := range results {
		if _, ok := byCategory[r.Category]; !ok {
			categories = append(categories, r.Category)
		}
		byCategory[r.Category] = append(byCategory[r.Category], r)
	}
	sort.Slice(categories, func(i, j int) bool { return categories[i] < categories[j] })

	var b strings.Builder
	for _, cat := range categories {
		fmt.Fprintf(&b, "=== %s ===\n", cat)
		for _, r := range byCategory[cat] {
			fmt.Fprintf(&b, "[%s] %s (%s)", strings.ToUpper(string(r.Status)), r.Name, r.Duration)
			if r.Reason != "" {
				fmt.Fprintf(&b, " — %s", r.Reason)
			}
			b.WriteString("\n")
			if r.Message != "" {
				indented := strings.ReplaceAll(strings.TrimRight(r.Message, "\n"), "\n", "\n    ")
				fmt.Fprintf(&b, "    %s\n", indented)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
