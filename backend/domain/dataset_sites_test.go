package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDatasetSitesCanonicalJSONAndValidation(t *testing.T) {
	var ref DatasetReference
	if err := json.Unmarshal([]byte(`{"dataset":"data","version":"latest","sites":["site-b","site-a","site-b"]}`), &ref); err != nil {
		t.Fatal(err)
	}
	if ref.Sites.JSON() != `["site-a","site-b"]` {
		t.Fatal(ref.Sites)
	}
	encoded, err := json.Marshal(ref)
	if err != nil || !strings.Contains(string(encoded), `"sites":["site-a","site-b"]`) {
		t.Fatalf("%s %v", encoded, err)
	}
	for _, invalid := range []string{`null`, `"site-a"`, `["../a"]`, `[""]`, `[" a"]`, `["a.b"]`, `[1]`} {
		var sites DatasetSites
		if json.Unmarshal([]byte(invalid), &sites) == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
	if _, err := NewDatasetSites(make([]string, 257)); err == nil {
		t.Fatal("accepted too many sites")
	}
	if (DatasetReference{Sites: ref.Sites}).Validate() == nil {
		t.Fatal("sites without dataset accepted")
	}
	if (DatasetProvenance{Sites: ref.Sites}).Validate() == nil {
		t.Fatal("sites without provenance accepted")
	}
}

func TestDatasetSitesCannotStoreNonCanonicalSelection(t *testing.T) {
	if (DatasetSites(`["b","a"]`)).Validate() == nil {
		t.Fatal("accepted noncanonical sites")
	}
	if (DatasetSites("")).JSON() != "[]" {
		t.Fatal("legacy default is not all sites")
	}
}
