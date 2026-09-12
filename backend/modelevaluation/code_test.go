package modelevaluation

import (
	"errors"
	"strings"
	"testing"
)

func validCodeSnapshot() CodeSnapshot {
	return CodeSnapshot{ID: strings.Repeat("a", 32), SHA256: strings.Repeat("b", 64), SizeBytes: 1024, Format: "zip"}
}

func TestEvaluatorSupportsExactlyOneImmutableCodeSource(t *testing.T) {
	git := validEvaluator()
	if err := ValidateEvaluator(git); err != nil {
		t.Fatal(err)
	}
	code := validCodeSnapshot()
	uploaded := git
	uploaded.Code, uploaded.GitURL, uploaded.GitCommit = &code, "", ""
	if err := ValidateEvaluator(uploaded); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Evaluator){
		func(e *Evaluator) { e.GitURL = git.GitURL },
		func(e *Evaluator) { e.GitCommit = git.GitCommit },
		func(e *Evaluator) { e.Code = nil },
	} {
		e := uploaded
		change(&e)
		if !errors.Is(ValidateEvaluator(e), ErrInvalid) {
			t.Fatalf("ambiguous or absent source accepted: %+v", e)
		}
	}
}

func TestCodeSnapshotBoundsAndIdentifiers(t *testing.T) {
	for _, change := range []func(*CodeSnapshot){
		func(c *CodeSnapshot) { c.ID = "../output" },
		func(c *CodeSnapshot) { c.ID = strings.Repeat("A", 32) },
		func(c *CodeSnapshot) { c.ID += "/source.zip" },
		func(c *CodeSnapshot) { c.ID = "" },
		func(c *CodeSnapshot) { c.ID = strings.Repeat("a", 33) },
		func(c *CodeSnapshot) { c.SHA256 = strings.Repeat("B", 64) },
		func(c *CodeSnapshot) { c.SHA256 = "abc" },
		func(c *CodeSnapshot) { c.SizeBytes = 0 },
		func(c *CodeSnapshot) { c.SizeBytes = MaxEvaluationCodeSize + 1 },
		func(c *CodeSnapshot) { c.Format = "tar" },
	} {
		code := validCodeSnapshot()
		change(&code)
		if !errors.Is(ValidateCodeSnapshot(code), ErrInvalid) {
			t.Fatalf("invalid snapshot accepted: %+v", code)
		}
	}
	code := validCodeSnapshot()
	code.SizeBytes = MaxEvaluationCodeSize
	if err := ValidateCodeSnapshot(code); err != nil {
		t.Fatal(err)
	}
}

func TestComparisonFingerprintIncludesCodeSnapshotIdentity(t *testing.T) {
	e := validEvaluation()
	code := validCodeSnapshot()
	e.Evaluator.Code, e.Evaluator.GitURL, e.Evaluator.GitCommit = &code, "", ""
	baseline := ComparisonFingerprint(e)
	if baseline == "" {
		t.Fatal("valid code evaluator has no fingerprint")
	}
	for _, change := range []func(*CodeSnapshot){
		func(c *CodeSnapshot) { c.ID = strings.Repeat("c", 32) },
		func(c *CodeSnapshot) { c.SHA256 = strings.Repeat("d", 64) },
		func(c *CodeSnapshot) { c.SizeBytes++ },
	} {
		other := e
		different := code
		change(&different)
		other.Evaluator.Code = &different
		if hash := ComparisonFingerprint(other); hash == "" || hash == baseline {
			t.Fatal("code source change did not change fingerprint")
		}
	}
	if ComparisonFingerprint(validEvaluation()) == baseline {
		t.Fatal("uploaded code confused with legacy Git source")
	}
}
