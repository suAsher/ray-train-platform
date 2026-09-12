package helpdocs

import (
	"strings"
	"testing"

	"ray-train-platform-backend/domain"
)

func TestHelpArticleMetadataCoversUserSeedDocuments(t *testing.T) {
	seed, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	known := KnownHelpArticleIDs()
	if len(known) != 34 {
		t.Fatalf("known public article count got %d want 34", len(known))
	}
	adminOnly := map[string]bool{
		"admin-node-onboarding": true,
		"admin-team-retirement": true,
		"idc-sync-lifecycle":    true,
	}
	seenCategories := map[string]bool{}
	for _, source := range seed {
		_, mapped := HelpArticleMetaForID(source.ID)
		if adminOnly[source.ID] {
			if mapped {
				t.Fatalf("admin seed %s should not be a public article", source.ID)
			}
			continue
		}
		if !mapped {
			t.Fatalf("user seed %s (%s) has no article metadata", source.ID, source.Title)
		}
		article := ProjectHelpArticle(domain.HelpDocument{ID: source.ID, Title: source.Title, Category: source.Category, SortOrder: source.SortOrder, Markdown: source.Markdown, UpdatedBy: PlatformSeedActor})
		if article.CategoryID == "" || article.Summary == "" || len(article.Keywords) == 0 || len(article.RelatedIDs) == 0 {
			t.Fatalf("article %s is missing public metadata: %+v", source.ID, article)
		}
		seenCategories[article.CategoryID] = true
	}
	for _, categoryID := range []string{"start", "data", "training", "debug", "mlflow", "troubleshooting"} {
		if !seenCategories[categoryID] {
			t.Fatalf("category %s has no article", categoryID)
		}
	}
}

func TestProjectHelpArticleKeepsPublicGuideSupplementSectionsComplete(t *testing.T) {
	targetsBySection := map[string][]string{}
	for _, target := range PublicGuideSectionTargets() {
		key := target.GuideID + "/" + target.Heading
		found := false
		for _, guide := range PublicGuides() {
			if guide.ID == target.GuideID && markdownSectionsByHeading(guide.Markdown)[target.Heading] != "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("supplement target references missing source section %s", key)
		}
		targetsBySection[key] = append(targetsBySection[key], target.ArticleID)
	}
	for _, guide := range PublicGuides() {
		for heading, section := range markdownSectionsByHeading(guide.Markdown) {
			key := guide.ID + "/" + heading
			targetArticleIDs := targetsBySection[key]
			if len(targetArticleIDs) != 1 {
				t.Fatalf("public guide section %s has %d targets: %+v", key, len(targetArticleIDs), targetArticleIDs)
			}
			meta, ok := HelpArticleMetaForID(targetArticleIDs[0])
			if !ok {
				t.Fatalf("public guide section %s maps to missing article %s", key, targetArticleIDs[0])
			}
			article := ProjectHelpArticle(domain.HelpDocument{ID: targetArticleIDs[0], Title: meta.Title, Category: meta.Category, SortOrder: meta.SortOrder, Markdown: "source body", UpdatedBy: PlatformSeedActor})
			if !strings.Contains(article.Markdown, section) {
				t.Fatalf("article %s does not retain public guide section %s", targetArticleIDs[0], key)
			}
		}
	}
}

func TestProjectHelpArticleLegacyAnchorsAreStableAndTopLevel(t *testing.T) {
	customEnvironment := ProjectHelpArticle(domain.HelpDocument{ID: "custom-environment", Title: "缺少环境？自定义训练镜像", Category: "02 准备代码和数据", SortOrder: 150, Markdown: "body", UpdatedBy: PlatformSeedActor})
	wantTopics := map[string]bool{"data": true, "training-guide": true}
	for _, anchor := range customEnvironment.LegacyAnchors {
		delete(wantTopics, anchor.TopicID)
		if anchor.SectionID != "section-缺少环境-自定义训练镜像" || anchor.ArticleSectionID != "" {
			t.Fatalf("unexpected custom environment anchor: %+v", anchor)
		}
	}
	if len(wantTopics) != 0 {
		t.Fatalf("custom environment anchors missing topics: %+v in %+v", wantTopics, customEnvironment.LegacyAnchors)
	}

	errors := ProjectHelpArticle(domain.HelpDocument{ID: "errors", Title: "常见错误速查", Category: "05 故障排查", SortOrder: 410, Markdown: "body", UpdatedBy: PlatformSeedActor})
	if len(errors.LegacyAnchors) != 1 || errors.LegacyAnchors[0].TopicID != "troubleshooting" || errors.LegacyAnchors[0].SectionID != "section-常见错误速查" || errors.LegacyAnchors[0].ArticleSectionID != "" {
		t.Fatalf("unexpected errors anchor: %+v", errors.LegacyAnchors)
	}
}

func TestProjectHelpArticleMLflowKeywordsSupportIDSearch(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID: "mlflow", Title: "MLflow", Category: "04 结果与MLflow", SortOrder: 320, Markdown: "body", UpdatedBy: PlatformSeedActor})
	keywords := strings.Join(article.Keywords, " ")
	for _, marker := range []string{"run_id", "job_id", "Run ID", "Job ID"} {
		if !strings.Contains(keywords, marker) {
			t.Fatalf("mlflow keywords missing %q: %+v", marker, article.Keywords)
		}
	}
}
