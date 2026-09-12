package helpdocs

import (
	"regexp"
	"sort"
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
	if len(docs) != 37 {
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

func TestHelpCatalogPreservesPublishedTopicsAndReadingOrder(t *testing.T) {
	docs, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	// These IDs have been published. Titles and prose can evolve without breaking
	// bookmarked topics or links from the old frontend and external-submit page.
	published := strings.Fields(`quickstart debug storage code submit data-mode cache
		ray-data streaming observability mlflow diagnose scaling resume artifacts datasets
		access quota scheduling-topology errors preflight uploads custom-environment
		command-recipes cli-onboarding-v2 unified-login-and-roles portal-user-feature-map
		idc-sync-lifecycle portal-browser-tools-and-queue mlflow-framework-metrics
		worker-connect-and-scheduling-boundary mlflow-api-with-pat streaming-validation
		telemetry-boundary`)
	byID := make(map[string]bool, len(docs))
	groups := map[string]int{
		"01 开始使用":      0,
		"02 准备代码和数据":   0,
		"03 提交与运行":     0,
		"04 结果与MLflow": 0,
		"05 故障排查":      0,
		"06 进阶与管理员":    0,
	}
	for _, doc := range docs {
		byID[doc.ID] = true
		if _, ok := groups[doc.Category]; !ok {
			t.Errorf("%s has an unrecognised reading category %q", doc.ID, doc.Category)
		}
		groups[doc.Category]++
	}
	for _, id := range published {
		if !byID[id] {
			t.Errorf("published help topic %s was removed", id)
		}
	}
	for category, count := range groups {
		if count == 0 {
			t.Errorf("reading category %q is empty", category)
		}
	}
	// The repository sorts by SortOrder, not by category. Interleaved categories
	// would fragment the reader's navigation even if all category names are valid.
	sort.Slice(docs, func(i, j int) bool { return docs[i].SortOrder < docs[j].SortOrder })
	if docs[0].ID != "quickstart" {
		t.Errorf("first help topic is %s, want the first-run workflow", docs[0].ID)
	}
	for i := 1; i < len(docs); i++ {
		if docs[i].SortOrder == docs[i-1].SortOrder {
			t.Errorf("ambiguous reading order for %s and %s", docs[i-1].ID, docs[i].ID)
		}
		if docs[i].Category < docs[i-1].Category {
			t.Errorf("reading categories are interleaved between %s and %s", docs[i-1].ID, docs[i].ID)
		}
	}
}

func TestHelpTopicLinksResolveToPublishedDocuments(t *testing.T) {
	docs, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool, len(docs))
	for _, doc := range docs {
		ids[doc.ID] = true
	}
	// Topic fragments are a shared navigation contract, not Markdown heading IDs.
	topicLink := regexp.MustCompile(`\[[^\]\n]+\]\(#([^\s)]+)\)`)
	links := 0
	for _, doc := range docs {
		for _, match := range topicLink.FindAllStringSubmatch(doc.Markdown, -1) {
			links++
			if !ids[match[1]] {
				t.Errorf("%s links to missing topic #%s", doc.ID, match[1])
			}
			if match[1] == doc.ID {
				t.Errorf("%s has a self-referential topic link", doc.ID)
			}
		}
	}
	if links == 0 {
		t.Fatal("no cross-topic navigation found")
	}
}

func TestMLflowAPIHelpKeepsBrowserAndPATBoundariesSeparate(t *testing.T) {
	docs, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		if doc.ID != "mlflow-api-with-pat" {
			continue
		}
		for _, marker := range []string{
			"jobs:read",
			"/api/v1/experiments?limit=100",
			"/api/v1/jobs/${JOB_ID}/experiment",
			"MLFLOW_DASHBOARD_AUTH_REQUIRED",
			"不要改用原生 MLflow API 绕过",
		} {
			if !strings.Contains(doc.Markdown, marker) {
				t.Fatalf("MLflow API help is missing %q", marker)
			}
		}
		return
	}
	t.Fatal("MLflow API help document is missing")
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
