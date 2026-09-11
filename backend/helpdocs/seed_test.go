package helpdocs

import (
	"strings"
	"testing"
)

func TestRayDataHelpPreventsNestedTrainer(t *testing.T) {
	docs, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		if doc.ID != "ray-data" {
			continue
		}
		for _, marker := range []string{"get_dataset_shard", "不要再调用 ray.init()", "不要再构造 TorchTrainer", ".rayignore", "rtx4090"} {
			if !strings.Contains(doc.Markdown, marker) {
				t.Fatalf("ray-data help is missing %q", marker)
			}
		}
		return
	}
	t.Fatal("ray-data help document is missing")
}

func TestEmbeddedDocumentsAreValidStableAndIndependent(t *testing.T) {
	docs, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 31 {
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

func TestPortalUserFeatureMapCoversDailyUserWorkflows(t *testing.T) {
	docs, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		if doc.ID != "portal-user-feature-map" {
			continue
		}
		for _, marker := range []string{
			"新 Portal",
			"旧前端",
			"导出全量日志",
			"spk-rayjob connect",
			"MLflow 详情",
			"版本化数据集",
			"账户与安全",
		} {
			if !strings.Contains(doc.Markdown, marker) {
				t.Fatalf("portal user feature map is missing %q", marker)
			}
		}
		return
	}
	t.Fatal("portal user feature map document is missing")
}
