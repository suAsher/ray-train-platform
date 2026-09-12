package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"ray-train-platform-backend/auth"
	"ray-train-platform-backend/domain"
)

type helpDocumentOnlyStore struct{}

func (helpDocumentOnlyStore) ListHelpDocuments(context.Context, bool) ([]domain.HelpDocument, error) {
	return []domain.HelpDocument{}, nil
}
func (helpDocumentOnlyStore) CreateHelpDocument(context.Context, domain.HelpDocument, string) (domain.HelpDocument, error) {
	return domain.HelpDocument{}, errors.New("not implemented")
}
func (helpDocumentOnlyStore) ChangeHelpDocument(context.Context, string, int64, string, int64, *domain.HelpDocument, string) (domain.HelpDocument, error) {
	return domain.HelpDocument{}, errors.New("not implemented")
}
func (helpDocumentOnlyStore) HelpDocumentHistory(context.Context, string) ([]domain.HelpDocument, error) {
	return nil, errors.New("not implemented")
}

type helpArticleListStore struct {
	helpDocumentOnlyStore
	articles []domain.HelpArticle
}

func (s helpArticleListStore) ListHelpArticles(context.Context) ([]domain.HelpArticle, error) {
	return s.articles, nil
}

func TestHelpArticlesRouteRequiresAuthentication(t *testing.T) {
	h := NewHandler(nil, Options{})
	r := gin.New()
	h.RegisterHelpReadRoutes(r.Group("/api/v1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/help/articles", nil))
	if w.Code != 401 {
		t.Fatalf("got %d want 401: %s", w.Code, w.Body.String())
	}
}

func TestHelpArticlesRouteRequiresArticleCapableStore(t *testing.T) {
	h := NewHandler(nil, Options{})
	h.helpDocuments = helpDocumentOnlyStore{}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "reader", TenantID: "team", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	})
	h.RegisterHelpReadRoutes(r.Group("/api/v1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/help/articles", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "HELP_UNAVAILABLE") {
		t.Fatalf("got %d want 503 HELP_UNAVAILABLE: %s", w.Code, w.Body.String())
	}
}

func TestHelpArticlesRouteReturnsArticleFields(t *testing.T) {
	h := NewHandler(nil, Options{})
	h.helpDocuments = helpArticleListStore{articles: []domain.HelpArticle{{
		HelpDocument: domain.HelpDocument{ID: "mlflow", Title: "如何查看实验、比较 Run，Job ID 和 Run ID 怎么对应？", Category: "MLflow 与 API", SortOrder: 410, Markdown: "body", Version: 2, PublishedVersion: 2},
		CategoryID:   "mlflow",
		Summary:      "summary",
		Keywords:     []string{"run_id", "job_id"},
		RelatedIDs:   []string{"mlflow-api-with-pat"},
		LegacyAnchors: []domain.HelpArticleLegacyAnchor{{
			TopicID:          "mlflow",
			SectionID:        "section-查看训练实验与结果",
			ArticleSectionID: "",
		}},
	}}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("ray-platform-principal", auth.Principal{Subject: "reader", TenantID: "team", Roles: []string{domain.RoleEngineer}, AuthType: auth.AuthTypeLocal})
	})
	h.RegisterHelpReadRoutes(r.Group("/api/v1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/help/articles", nil))
	if w.Code != 200 {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing no-store cache control: %q", w.Header().Get("Cache-Control"))
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Items []domain.HelpArticle `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Success || len(envelope.Data.Items) != 1 {
		t.Fatalf("unexpected response: %+v body=%s", envelope, w.Body.String())
	}
	item := envelope.Data.Items[0]
	if item.ID != "mlflow" || item.CategoryID != "mlflow" || item.Summary != "summary" || len(item.Keywords) != 2 || len(item.RelatedIDs) != 1 || len(item.LegacyAnchors) != 1 {
		t.Fatalf("article fields missing from response: %+v", item)
	}
}
