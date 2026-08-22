package spkrayjob

import (
	"strings"
	"testing"
)

// readCredentialLine used to be io.ReadAll, which waits for EOF. A user who
// typed a password and pressed Enter got no prompt, no echo and no response —
// the command simply hung until Ctrl-D. One line is the whole credential.
func TestReadCredentialLineReturnsOnNewlineRatherThanWaitingForEOF(t *testing.T) {
	// A reader that never reaches EOF: the second Read would block forever in
	// the old implementation, exactly like a terminal does.
	reader := &blockingAfterFirstLine{line: "hunter2\n"}
	value, err := readCredentialLine(reader)
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if value != "hunter2" {
		t.Fatalf("expected the typed credential, got %q", value)
	}
}

type blockingAfterFirstLine struct {
	line string
	done bool
}

func (r *blockingAfterFirstLine) Read(p []byte) (int, error) {
	if r.done {
		// Simulate a terminal with nothing more to give: any implementation that
		// keeps reading past the newline hangs the test rather than failing it.
		select {}
	}
	r.done = true
	return copy(p, r.line), nil
}

// Piping still works, and a trailing newline is not part of the password.
func TestReadCredentialLineAcceptsAPipedValue(t *testing.T) {
	value, err := readCredentialLine(strings.NewReader("piped-token\n"))
	if err != nil || value != "piped-token" {
		t.Fatalf("expected piped-token, got %q (%v)", value, err)
	}
}

// A value without a trailing newline reaches EOF; that is still a credential.
func TestReadCredentialLineAcceptsAValueWithoutTrailingNewline(t *testing.T) {
	value, err := readCredentialLine(strings.NewReader("no-newline"))
	if err != nil || value != "no-newline" {
		t.Fatalf("expected no-newline, got %q (%v)", value, err)
	}
}

// Passwords may legitimately contain spaces; only surrounding whitespace and
// the line ending are stripped. The previous strings.Fields split on any space
// and then rejected the value as "must contain exactly one value".
func TestReadCredentialLinePreservesInnerSpaces(t *testing.T) {
	value, err := readCredentialLine(strings.NewReader("  pass word with spaces  \n"))
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if value != "pass word with spaces" {
		t.Fatalf("expected inner spaces preserved, got %q", value)
	}
}

func TestReadCredentialLineRejectsAnEmptyValue(t *testing.T) {
	for _, input := range []string{"", "\n", "   \n"} {
		if _, err := readCredentialLine(strings.NewReader(input)); err == nil {
			t.Fatalf("expected an error for %q", input)
		}
	}
}

// A pasted multi-line blob is almost always a mistake; taking only the first
// line silently would log the user in with a truncated secret.
func TestReadCredentialLineRejectsAnOversizedValue(t *testing.T) {
	if _, err := readCredentialLine(strings.NewReader(strings.Repeat("a", 9000))); err == nil {
		t.Fatal("expected an error for an oversized credential")
	}
}
