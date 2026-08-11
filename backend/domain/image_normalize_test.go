package domain

import "testing"

// Digests are pasted from build output, which commonly carries a trailing
// newline. Without trimming, submission fails with a misleading "image is not
// in the allowlist" even though the catalogue holds exactly that image.
func TestNormalizeImageReferenceTrimsPastedWhitespace(t *testing.T) {
	reference := "registry.example/repo@sha256:" + repeatChar('a', 64)
	for _, raw := range []string{reference, reference + "\n", " " + reference + " \t\n"} {
		if got := NormalizeImageReference(raw); got != reference {
			t.Fatalf("expected %q, got %q", reference, got)
		}
	}
}
