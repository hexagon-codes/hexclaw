package mathtext

import (
	"strings"
	"testing"
)

func TestEmptyFractionProjectionPreservesOperatorsAndBoxes(t *testing.T) {
	for _, source := range []string{`\(2\times\frac{\square}{\square}\)`, `\(2\times\frac{\Box}{\Box}\)`, `\\(2\\times\\frac{\\square}{\\square}\\)`} {
		got, _ := ProjectReadable(source)
		if strings.Join(strings.Fields(got), "") != "2×□/□" {
			t.Fatalf("projection %q = %q; want multiplication and two empty boxes", source, got)
		}
	}
	got, _ := ProjectReadable(`\(2\times\frac{x}{y}\)`)
	if strings.Join(strings.Fields(got), "") != "2×x/y" {
		t.Fatalf("a fraction expansion merged with its preceding command: %q", got)
	}
	plain := `document path \square remains literal`
	if got, _ := ProjectReadable(plain); got != plain {
		t.Fatalf("a command outside math changed: %q", got)
	}
}
