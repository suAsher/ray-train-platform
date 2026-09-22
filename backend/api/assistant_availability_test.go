package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"ray-train-platform-backend/assistant"
	"ray-train-platform-backend/domain"
)

func TestAssistantUnconfiguredIsNotAnAvailableBot(t *testing.T) {
	empty, err := assistant.NewRouter(assistant.Config{})
	if err != nil { t.Fatal(err) }
	for _, engine := range []assistant.Engine{nil, empty} {
		h := assistantTestHandler()
		h.assistant = engine
		r := assistantTestRouter(h, assistantTestPrincipal())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/capabilities", nil))
		var response struct { Data assistant.Capabilities `json:"data"` }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Data.Enabled || len(response.Data.Modes) != 0 { t.Fatalf("unconfigured bot exposed: %s", w.Body.String()) }
		for _, mode := range []string{"auto", "docs", "local"} {
			w = assistantRequest(r, `{"question":"日志怎么看","mode":"`+mode+`"}`)
			if w.Code != 503 || !strings.Contains(w.Body.String(), "ASSISTANT_NOT_CONFIGURED") { t.Fatalf("unconfigured bot answered: %d %s", w.Code, w.Body.String()) }
		}
	}
}

type assistantUnavailableArticleStore struct { helpArticleListStore }
func (assistantUnavailableArticleStore) ListHelpArticles(context.Context) ([]domain.HelpArticle, error) { return nil, errors.New("private-db-error") }

func TestAssistantKnowledgeFailureDoesNotMasqueradeAsNoMatchingDocs(t *testing.T) {
	h := assistantTestHandler()
	e := &assistantTestEngine{}
	h.assistant = e
	h.helpDocuments = assistantUnavailableArticleStore{}
	w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), `{"question":"日志怎么看"}`)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "ASSISTANT_KNOWLEDGE_UNAVAILABLE") || strings.Contains(w.Body.String(), "private-db-error") || strings.Contains(w.Body.String(), "没有找到") || e.calls != 0 { t.Fatalf("knowledge failure concealed: %d %s", w.Code, w.Body.String()) }
}

func TestAssistantProviderFailureDoesNotReturnRetrievalAsAnAnswer(t *testing.T) {
	for _, result := range []assistant.Result{{Mode:"docs", Reason:"budget_exhausted"}, {Mode:"local", Answer:""}} {
		h := assistantTestHandler()
		h.assistant = &assistantTestEngine{result:result}
		w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), `{"question":"日志怎么看"}`)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "ASSISTANT_MODEL_UNAVAILABLE") || strings.Contains(w.Body.String(), "根据本次检索") { t.Fatalf("model failure concealed: %d %s", w.Code, w.Body.String()) }
	}
}

func TestAssistantNoSearchMatchStillAllowsModelClarification(t *testing.T) {
	h := assistantTestHandler()
	e := &assistantTestEngine{result:assistant.Result{Mode:"local", Answer:"请说明你要完成的操作，或提供具体任务 ID。", Reason:"selected"}}
	h.assistant = e
	w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), `{"question":"我接下来怎么办"}`)
	if w.Code != 200 || e.calls != 1 || len(e.input.Evidence) != 0 || !strings.Contains(w.Body.String(), "请说明") || strings.Contains(w.Body.String(), "没有找到足够") { t.Fatalf("model clarification was skipped: %d %s", w.Code, w.Body.String()) }
}

func TestAssistantPreviewUsesExactSubjectBeforeReadingEvidence(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		e := &assistantTestEngine{result:assistant.Result{Mode:"local", Answer:"answer", Reason:"selected"}}
		h := NewHandler(&assistantTestAuditStore{fakeJobRepository:&fakeJobRepository{}}, Options{Assistant:e, AssistantPreviewSubjects:[]string{"preview-subject"}})
		h.helpDocuments = assistantTestHandler().helpDocuments
		p := assistantTestPrincipal()
		p.Username = "preview-subject" // A username alone must not grant preview access.
		if allowed { p.Subject = "preview-subject" }
		r := assistantTestRouter(h, p)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/assistant/capabilities", nil))
		var response struct { Data assistant.Capabilities `json:"data"` }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Data.Enabled != allowed { t.Fatalf("incorrect preview capabilities: %s", w.Body.String()) }
		w = assistantRequest(r, `{"question":"日志怎么看"}`)
		if allowed && (w.Code != 200 || e.calls != 1) || !allowed && (w.Code != 403 || e.calls != 0 || strings.Contains(w.Body.String(), "preview-subject")) { t.Fatalf("preview gate failed: %d %s", w.Code, w.Body.String()) }
	}
}
