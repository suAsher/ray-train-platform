package helpdocs

import "testing"

func TestEmbeddedDocumentsAreValidStableAndIndependent(t *testing.T) {
	docs, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 24 {
		t.Fatalf("got %d seeded docs", len(docs))
	}
	seen := map[string]bool{}
	for _, doc := range docs {
		if err := doc.Validate(); err != nil {
			t.Fatalf("%s: %v", doc.ID, err)
		}
		if seen[doc.ID] {
			t.Fatal("duplicate", doc.ID)
		}
		seen[doc.ID] = true
	}
	docs[0].Markdown = "changed"
	again, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Markdown == "changed" {
		t.Fatal("seed reused mutable data")
	}
}
