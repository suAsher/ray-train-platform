package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ray-train-platform-backend/assistant"
	"ray-train-platform-backend/domain"
	"ray-train-platform-backend/helpdocs"
	"ray-train-platform-backend/observability"
)

func TestAssistantRetrievalAnswersCommonPublishedQuestions(t *testing.T) {
	docs, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	for i := range docs {
		docs[i].UpdatedBy = helpdocs.PlatformSeedActor
	}
	h := assistantTestHandler()
	h.helpDocuments = helpArticleListStore{articles: helpdocs.ProjectHelpArticles(docs)}
	for _, tc := range []struct{ question, first string }{
		{"CLI 安装后怎样登录？", "cli-onboarding-v2"},
		{"日志里有 loss 但页面没有曲线，怎么办？", "telemetry-boundary"},
		{"大文件上传失败，报 413 怎么处理？", "uploads"},
		{"如何查看团队 GPU 配额和剩余额度？", "quota"},
	} {
		t.Run(tc.first, func(t *testing.T) {
			got, available := h.assistantDocuments(context.Background(), tc.question)
			if !available || len(got) == 0 || got[0].ID != tc.first {
				t.Fatalf("question %q: expected first %s, got %+v", tc.question, tc.first, got)
			}
		})
	}
}

func TestAssistantRetrievalRejectsCoincidentalChineseFragments(t *testing.T) {
	h := assistantTestHandler()
	h.helpDocuments = helpArticleListStore{articles: []domain.HelpArticle{
		{HelpDocument: domain.HelpDocument{ID: "quota", Title: "我的 GPU 配额在哪里看？", Markdown: "我的训练任务显示 GPU 配额，操作失败怎么办请看使用说明。"}},
		{HelpDocument: domain.HelpDocument{ID: "logs", Title: "日志分析和排查", Markdown: "分析日志中的失败原因。"}},
	}}
	for _, question := range []string{"我应该怎么办？", "我的 GPU 账单怎么报销？", "用 R 语言做生存分析", "明天天气怎么样", "这是什么意思？"} {
		got, available := h.assistantDocuments(context.Background(), question)
		if !available || len(got) != 0 {
			t.Errorf("unrelated question %q forced citations: %+v", question, got)
		}
	}
}

func TestAssistantRetrievalKeepsPrerequisiteCommandAndVerificationTogether(t *testing.T) {
	h := assistantTestHandler()
	h.helpDocuments = helpArticleListStore{articles: []domain.HelpArticle{{
		HelpDocument: domain.HelpDocument{ID: "cli-login", Title: "CLI 登录步骤", Markdown: "## CLI 登录\n\n先在账户与安全创建自己的 PAT，不要发送给助手。\n\n1. 在本机终端运行：\n\n```bash\nspk-rayjob login --server https://raytrain.wellspiking.ai\n```\n\n2. 输入自己的 PAT。\n\n3. 运行 spk-rayjob whoami 核对当前团队。\n\n## 无关的历史\n\n旧流程已弃用。"},
		Keywords:     []string{"CLI", "登录"},
	}}}
	got, _ := h.assistantDocuments(context.Background(), "CLI 如何登录？")
	if len(got) != 1 {
		t.Fatalf("expected one complete section, got %+v", got)
	}
	for _, marker := range []string{"## CLI 登录", "先在账户与安全", "spk-rayjob login --server https://raytrain.wellspiking.ai", "输入自己的 PAT", "spk-rayjob whoami"} {
		if !strings.Contains(got[0].Excerpt, marker) {
			t.Errorf("lost required command context %q: %s", marker, got[0].Excerpt)
		}
	}
	if strings.Contains(got[0].Excerpt, "旧流程已弃用") {
		t.Fatal("unrelated section included")
	}
	if strings.Count(got[0].Excerpt, "```") != 2 {
		t.Fatal("code fence was split")
	}
}

func assistantQualityResponse(t *testing.T, h *Handler, question string) assistantQueryResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{"question": question, "jobId": "job-own", "mode": "docs"})
	if err != nil {
		t.Fatal(err)
	}
	w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), string(body))
	if w.Code != 200 {
		t.Fatalf("query failed: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data assistantQueryResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func assistantQualityJobHandler() *Handler {
	h := assistantTestHandler()
	h.repository = &assistantTestAuditStore{fakeJobRepository: &fakeJobRepository{jobs: []domain.TrainingJob{{
		ID: "job-own", TenantID: "team-a", ObservedState: domain.State("FAILED"), StatusReason: "OOMKilled", StatusMessage: "worker exceeded memory password=do-not-send",
		Spec: domain.JobSpec{Resources: domain.Resources{WorkerReplicas: 2, GPUsPerWorker: 1, CPUPerWorker: 4, MemoryPerWorker: "16Gi"},
			Input: domain.DataLocation{Space: domain.DataSpaceTeamShared, RelativePath: "datasets/example"}, Output: domain.DataLocation{Space: domain.DataSpaceMyRuns, RelativePath: "run-one"},
			Entrypoint:         domain.Entrypoint{Command: []string{"sensitive-user-command"}},
			ResolvedDataMounts: domain.ResolvedDataSpaceMounts{Input: &domain.ResolvedDataMount{ClaimName: "hidden-pvc", SubPath: "hidden-internal-root"}},
		},
	}}}}
	return h
}

func TestAssistantTaskFactsAreIntentScopedAndDoNotReadLogs(t *testing.T) {
	h := assistantQualityJobHandler()
	logs := &pagedLogProvider{lines: []observability.LogLine{{Line: "must-not-read-log"}}}
	h.logs = logs
	got := assistantQualityResponse(t, h, "这个任务为什么失败，申请了多少 GPU 和内存？")
	for _, marker := range []string{"OOMKilled", "2", "16Gi"} {
		if !strings.Contains(got.Answer, marker) {
			t.Errorf("missing authorized fact %q: %s", marker, got.Answer)
		}
	}
	for _, forbidden := range []string{"worker exceeded memory", "do-not-send", "must-not-read-log", "sensitive-user-command", "hidden-pvc", "hidden-internal-root", "datasets/example"} {
		if strings.Contains(got.Answer, forbidden) {
			t.Errorf("unrequested/sensitive fact disclosed: %q", forbidden)
		}
	}
	if logs.limit != 0 {
		t.Fatal("logs read without consent")
	}
	status := assistantQualityResponse(t, h, "任务状态是什么？")
	if strings.Contains(status.Answer, "worker exceeded") || strings.Contains(status.Answer, "16Gi") {
		t.Fatal("status-only question disclosed unrelated fields")
	}
}

type assistantQualityExperiments struct {
	fakeExperimentProvider
	experiment observability.JobExperiment
}

func (p *assistantQualityExperiments) QueryJobExperiment(_ context.Context, tenant, jobID string) (observability.JobExperiment, error) {
	p.tenant, p.jobID = tenant, jobID
	return p.experiment, nil
}

func TestAssistantTaskPathsAreLogicalAndMLflowLookupIsScoped(t *testing.T) {
	h := assistantQualityJobHandler()
	provider := &assistantQualityExperiments{experiment: observability.JobExperiment{Run: &observability.ExperimentRun{ID: "run-42", Status: "FINISHED", Params: map[string]string{"secret": "no-params"}}}}
	h.experiments = provider
	got := assistantQualityResponse(t, h, "输入数据和输出路径在哪里，关联哪个 MLflow Run？")
	for _, marker := range []string{"datasets/example", "run-one", "run-42"} {
		if !strings.Contains(got.Answer, marker) {
			t.Errorf("missing logical fact %q: %s", marker, got.Answer)
		}
	}
	for _, forbidden := range []string{"hidden-pvc", "hidden-internal-root", "no-params", "sensitive-user-command"} {
		if strings.Contains(got.Answer, forbidden) {
			t.Errorf("internal field leaked %q", forbidden)
		}
	}
	if provider.tenant != "team-a" || provider.jobID != "job-own" {
		t.Fatal("MLflow lookup not scoped to authorized job")
	}
}

func TestAssistantQuotaReadsOnlyCurrentTeamAndOnlyOnRelevantQuestion(t *testing.T) {
	h := assistantTestHandler()
	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "team-a", GPULimit: 8, GPUUsed: 3, GPUAvailable: 5}}
	h.quota = quota
	w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), `{"question":"我的团队还剩多少 GPU 配额？","mode":"docs"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "8") || !strings.Contains(w.Body.String(), "5") || len(quota.tenantIDs) != 1 || quota.tenantIDs[0] != "team-a" {
		t.Fatalf("quota was not queried in current team: %d %s calls=%v", w.Code, w.Body.String(), quota.tenantIDs)
	}
	_ = assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), `{"question":"日志怎么查看？","mode":"docs"}`)
	if len(quota.tenantIDs) != 1 {
		t.Fatal("quota read for unrelated question")
	}
}

func TestAssistantModelBudgetNeverMasqueradesAsTeamGPUQuota(t *testing.T) {
	h := assistantTestHandler()
	quota := &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "team-a", GPULimit: 8, GPUUsed: 3, GPUAvailable: 5}}
	h.quota = quota
	h.helpDocuments = helpArticleListStore{articles: []domain.HelpArticle{{HelpDocument: domain.HelpDocument{ID: "quota", Title: "GPU 配额和剩余额度", Markdown: "团队GPU限额不等于物理空闲卡。"}}}}
	for _, question := range []string{"API 模型还有多少额度？", "API 配额是多少？", "大模型 token 限额是多少？"} {
		body, _ := json.Marshal(map[string]any{"question": question, "mode": "docs"})
		w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), string(body))
		if w.Code != 200 || len(quota.tenantIDs) != 0 || strings.Contains(w.Body.String(), "current-team-quota") || strings.Contains(w.Body.String(), "#article/quota") {
			t.Fatalf("model budget became GPU quota: %d %s calls=%v", w.Code, w.Body.String(), quota.tenantIDs)
		}
	}
}

func TestAssistantQuotaScopeMismatchAndUnselectedJobDoNotDiscloseFacts(t *testing.T) {
	h := assistantQualityJobHandler()
	h.quota = &fakePlatformQuotaStore{quota: domain.TenantQuota{TenantID: "team-b", GPULimit: 9876, GPUUsed: 1, GPUAvailable: 9875}}
	w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), `{"question":"当前GPU配额是多少？","mode":"docs"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "9876") || strings.Contains(w.Body.String(), "OOMKilled") {
		t.Fatal("unselected task or other-team quota disclosed")
	}
}

func TestAssistantPublishedAndModelCommandURLsRequireExactSafeEvidence(t *testing.T) {
	h := assistantTestHandler()
	good := "https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-linux-amd64"
	bad := []string{"https://evil.invalid/install.sh", "https://raytrain.wellspiking.ai/?token=sensitive-value", "https://user:password@raytrain.wellspiking.ai/private"}
	h.helpDocuments = helpArticleListStore{articles: []domain.HelpArticle{{HelpDocument: domain.HelpDocument{ID: "cli-install", Title: "CLI 安装", Markdown: "CLI 安装：curl -fL " + good + "\n" + strings.Join(bad, "\n")}}}}
	h.assistant = &assistantTestEngine{result: assistant.Result{Mode: "api", Answer: "运行 curl -fL " + good + "；不可信 " + strings.Join(bad, " ") + " https://raytrain.wellspiking.ai/unseen-command"}}
	w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), `{"question":"CLI如何安装","mode":"auto"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), good) {
		t.Fatalf("safe command URL lost: %d %s", w.Code, w.Body.String())
	}
	for _, value := range []string{"evil.invalid", "sensitive-value", "user:password", "unseen-command"} {
		if strings.Contains(w.Body.String(), value) {
			t.Errorf("unsafe or invented URL survived: %s", value)
		}
	}
}

func TestAssistantRetrievalNaturalQuestionsAndPublishedTitles(t *testing.T) {
	docs, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	for i := range docs {
		docs[i].UpdatedBy = helpdocs.PlatformSeedActor
	}
	articles := helpdocs.ProjectHelpArticles(docs)
	h := assistantTestHandler()
	h.helpDocuments = helpArticleListStore{articles: articles}
	cases := []struct {
		question string
		ids      []string
	}{
		{"本地电脑的代码怎么提交训练", []string{"code", "submit", "quickstart"}},
		{"我想上传代码开始跑模型", []string{"code", "quickstart", "submit"}},
		{"调试好了怎么保存成训练环境", []string{"debug", "custom-environment"}},
		{"运行中的任务怎么取消", []string{"observability", "command-recipes", "quickstart"}},
		{"训练一直不开始", []string{"scheduling-topology"}},
		{"我的数据文件夹能整个传上去吗", []string{"uploads", "storage"}},
		{"权重怎么发到功能仓", []string{"shared-model-registration"}},
		{"一个任务可以用两台机器吗", []string{"submit", "scheduling-topology"}},
		{"网页怎么看loss曲线", []string{"observability", "telemetry-boundary", "mlflow-framework-metrics"}},
		{"已有环境缺个Python包怎么办", []string{"custom-environment", "debug"}},
		{"其他人能看我的任务吗", []string{"unified-login-and-roles", "quickstart"}},
		{"怎么继续昨天没跑完的训练", []string{"resume"}},
	}
	for _, article := range articles {
		cases = append(cases, struct {
			question string
			ids      []string
		}{article.Title, []string{article.ID}})
	}
	for _, tc := range cases {
		t.Run(tc.question, func(t *testing.T) {
			got, _ := h.assistantDocuments(context.Background(), tc.question)
			if len(got) == 0 {
				t.Fatal("no evidence")
			}
			valid := false
			for _, id := range tc.ids {
				if got[0].ID == id {
					valid = true
				}
			}
			if !valid {
				t.Errorf("first=%s want one of %v", got[0].ID, tc.ids)
			}
		})
	}
}

func TestAssistantQuestionJobIDUsesAuthorizationWithoutLogs(t *testing.T) {
	own := "job-0123456789abcdef01234567"
	other := "job-abcdef0123456789abcdef01"
	for _, tc := range []struct {
		question  string
		code      int
		contextID string
	}{
		{own + " 为什么失败", 200, own}, {own + " 和 " + own + "状态", 200, own},
		{other + " 为什么失败", 404, ""}, {own + " 与 " + other + " 哪个失败", 200, ""},
		{own + "89abcdef 为什么失败", 200, ""}, {"x" + own + " 为什么失败", 200, ""},
	} {
		t.Run(tc.question, func(t *testing.T) {
			h := assistantQualityJobHandler()
			h.repository = &assistantTestAuditStore{fakeJobRepository: &fakeJobRepository{jobs: []domain.TrainingJob{{ID: own, TenantID: "team-a", ObservedState: domain.State("FAILED"), StatusReason: "OOMKilled"}, {ID: other, TenantID: "team-b", ObservedState: domain.State("FAILED")}}}}
			logs := &pagedLogProvider{}
			h.logs = logs
			body, _ := json.Marshal(map[string]any{"question": tc.question, "mode": "docs"})
			w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), string(body))
			if w.Code != tc.code {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.code == 200 {
				var env struct {
					Data assistantQueryResponse `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
					t.Fatal(err)
				}
				if tc.contextID != "" && (env.Data.Context == nil || env.Data.Context.JobID != tc.contextID) {
					t.Fatal("explicit job not authorized/read")
				}
				if tc.contextID == "" && env.Data.Context != nil {
					t.Fatal("ambiguous or malformed job selected")
				}
				if strings.Contains(tc.question, " 与 ") && !strings.Contains(env.Data.Answer, "一个任务") {
					t.Fatal("multiple IDs need precise clarification")
				}
			}
			if logs.limit != 0 {
				t.Fatal("implicit logs read")
			}
		})
	}
}

func TestAssistantRelevantExcerptContainsActionNotOnlyCorrectTitle(t *testing.T) {
	docs, err := helpdocs.Documents()
	if err != nil {
		t.Fatal(err)
	}
	for i := range docs {
		docs[i].UpdatedBy = helpdocs.PlatformSeedActor
	}
	h := assistantTestHandler()
	h.helpDocuments = helpArticleListStore{articles: helpdocs.ProjectHelpArticles(docs)}
	for _, tc := range []struct{ question, marker string }{
		{"运行中的任务怎么取消", "spk-rayjob cancel JOB_ID"},
		{"CLI 如何登录", "spk-rayjob login"},
		{"怎么继续昨天没跑完的训练", "checkpoint"},
		{"我想上传代码开始跑模型", "ZIP"},
	} {
		t.Run(tc.question, func(t *testing.T) {
			got, _ := h.assistantDocuments(context.Background(), tc.question)
			if len(got) == 0 || !strings.Contains(got[0].Excerpt, tc.marker) {
				t.Fatalf("missing actionable marker %s in %+v", tc.marker, got)
			}
		})
	}
}

func TestAssistantStatusMessageRequiresLogConsent(t *testing.T) {
	for _, consent := range []bool{false, true} {
		h := assistantQualityJobHandler()
		engine := &assistantTestEngine{result: assistant.Result{Mode: "api", Answer: "根据任务证据检查内存。"}}
		h.assistant = engine
		body, _ := json.Marshal(map[string]any{"question": "这个任务为什么失败？", "jobId": "job-own", "includeLogs": consent, "mode": "api"})
		w := assistantRequest(assistantTestRouter(h, assistantTestPrincipal()), string(body))
		if w.Code != 200 || engine.calls != 1 {
			t.Fatalf("status=%d calls=%d", w.Code, engine.calls)
		}
		input, _ := json.Marshal(engine.input)
		for label, payload := range map[string]string{"response": w.Body.String(), "provider input": string(input)} {
			if strings.Contains(payload, "worker exceeded memory") != consent {
				t.Errorf("%s status message consent=%t was not enforced", label, consent)
			}
			if strings.Contains(payload, "do-not-send") {
				t.Errorf("%s contains credential", label)
			}
		}
	}
}
