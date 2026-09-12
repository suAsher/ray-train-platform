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
	if len(known) != 36 {
		t.Fatalf("known public article count got %d want 36", len(known))
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

func TestProjectHelpArticlesMovesPlatformSeedQueueSectionToScheduling(t *testing.T) {
	seed, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	var source domain.HelpDocument
	for _, document := range seed {
		if document.ID == queueAndToolArticleID {
			source = document
			break
		}
	}
	if source.ID == "" {
		t.Fatal("missing queue and tool seed document")
	}
	source.UpdatedBy = PlatformSeedActor
	projectedMarkdown, movedSection, ok := splitSubmittedSuspendedSection(source.Markdown)
	if !ok {
		t.Fatal("seed queue section was not found")
	}
	articles := ProjectHelpArticles(withPlatformSeedActor(seed))
	byID := helpArticleTestByID(articles)
	queueArticle := byID[queueAndToolArticleID]
	schedulingArticle := byID[schedulingArticleID]
	if strings.Contains(queueArticle.Markdown, movedSection) {
		t.Fatalf("queue article still contains moved submitted/suspended section: %q", queueArticle.Markdown)
	}
	if !strings.Contains(queueArticle.Markdown, projectedMarkdown) || !strings.Contains(queueArticle.Markdown, queueMovedNotice) {
		t.Fatalf("queue article did not preserve non-moved source text with notice")
	}
	if !strings.Contains(schedulingArticle.Markdown, movedSection) {
		t.Fatalf("scheduling article does not contain moved section: %q", schedulingArticle.Markdown)
	}
	assertHelpArticleAnchor(t, schedulingArticle, "troubleshooting", SectionAnchorID(submittedSuspendedSectionHeading), SectionAnchorID(submittedSuspendedSectionHeading))
}

func TestProjectHelpArticlesDoesNotSplitManualQueueArticle(t *testing.T) {
	manualSection := submittedSuspendedSectionStart + "\n\nmanual queue body"
	manualMarkdown := "manual intro\n\n" + manualSection + submittedSuspendedSectionEnd + "\n\nmanual tool body"
	articles := ProjectHelpArticles([]domain.HelpDocument{
		{ID: queueAndToolArticleID, Title: "人工工具排查", Category: "05 故障排查", SortOrder: 530, Markdown: manualMarkdown, UpdatedBy: "admin"},
		{ID: schedulingArticleID, Title: "多 Worker、多机与拓扑排队", Category: "06 进阶与管理员", SortOrder: 560, Markdown: "scheduling seed body", UpdatedBy: PlatformSeedActor},
	})
	byID := helpArticleTestByID(articles)
	if !strings.Contains(byID[queueAndToolArticleID].Markdown, manualSection) {
		t.Fatalf("manual queue article was split or lost its section: %q", byID[queueAndToolArticleID].Markdown)
	}
	if strings.Contains(byID[schedulingArticleID].Markdown, "manual queue body") {
		t.Fatalf("manual queue section moved into scheduling: %q", byID[schedulingArticleID].Markdown)
	}
}

func withPlatformSeedActor(documents []domain.HelpDocument) []domain.HelpDocument {
	out := make([]domain.HelpDocument, 0, len(documents))
	for _, document := range documents {
		document.UpdatedBy = PlatformSeedActor
		out = append(out, document)
	}
	return out
}

func helpArticleTestByID(items []domain.HelpArticle) map[string]domain.HelpArticle {
	out := make(map[string]domain.HelpArticle, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func assertHelpArticleAnchor(t *testing.T, article domain.HelpArticle, topicID, sectionID, articleSectionID string) {
	t.Helper()
	for _, anchor := range article.LegacyAnchors {
		if anchor.TopicID == topicID && anchor.SectionID == sectionID && anchor.ArticleSectionID == articleSectionID {
			return
		}
	}
	t.Fatalf("article %s missing anchor %s/%s -> %q: %+v", article.ID, topicID, sectionID, articleSectionID, article.LegacyAnchors)
}

func TestProjectHelpArticlesKeepsQueueSectionWhenSchedulingTargetMissing(t *testing.T) {
	seed, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	var source domain.HelpDocument
	for _, document := range seed {
		if document.ID == queueAndToolArticleID {
			source = document
			break
		}
	}
	if source.ID == "" {
		t.Fatal("missing queue and tool seed document")
	}
	_, movedSection, ok := splitSubmittedSuspendedSection(source.Markdown)
	if !ok {
		t.Fatal("seed queue section was not found")
	}
	source.UpdatedBy = PlatformSeedActor
	articles := ProjectHelpArticles([]domain.HelpDocument{source})
	byID := helpArticleTestByID(articles)
	if !strings.Contains(byID[queueAndToolArticleID].Markdown, movedSection) {
		t.Fatalf("queue section was removed even though scheduling target is missing: %q", byID[queueAndToolArticleID].Markdown)
	}
}

func TestProjectHelpArticlesKeepsQueueSectionWhenSchedulingTargetIsManual(t *testing.T) {
	seed, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	var source domain.HelpDocument
	for _, document := range seed {
		if document.ID == queueAndToolArticleID {
			source = document
			break
		}
	}
	if source.ID == "" {
		t.Fatal("missing queue and tool seed document")
	}
	_, movedSection, ok := splitSubmittedSuspendedSection(source.Markdown)
	if !ok {
		t.Fatal("seed queue section was not found")
	}
	source.UpdatedBy = PlatformSeedActor
	articles := ProjectHelpArticles([]domain.HelpDocument{
		source,
		{ID: schedulingArticleID, Title: "人工排队文档", Category: "03 提交与运行", SortOrder: 240, Markdown: "manual scheduling body", UpdatedBy: "admin"},
	})
	byID := helpArticleTestByID(articles)
	if !strings.Contains(byID[queueAndToolArticleID].Markdown, movedSection) {
		t.Fatalf("queue section was removed even though scheduling target is manual: %q", byID[queueAndToolArticleID].Markdown)
	}
	if strings.Contains(byID[schedulingArticleID].Markdown, movedSection) {
		t.Fatalf("queue section moved into manual scheduling article: %q", byID[schedulingArticleID].Markdown)
	}
}
