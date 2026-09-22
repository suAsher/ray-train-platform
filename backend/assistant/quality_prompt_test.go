package assistant

import (
	"encoding/json"
	"strings"
	"testing"
)

// These assert the request contract, not the semantic quality of a live model.
// The same synthetic cases are evaluated separately through the real Router.
func TestQualityPromptSupportsEvidenceBasedConclusions(t *testing.T) {
	p := &httpProvider{model: "synthetic"}
	for _, tc := range []struct{ question, evidence, rule string }{
		{"剩1卡能申请4卡吗", "GPU限额24，已用23，剩余1。", "1 < 4"},
		{"这个报错是不是OOM", "RuntimeError: CUDA out of memory", "可以确认显存不足"},
		{"任务失败怎么排查", "当前状态FAILED，没有状态原因。", "只问缺少的具体信息"},
	} {
		body, err := p.requestBody(Input{Question: tc.question, Evidence: []Evidence{{ID: "synthetic-fact", Excerpt: tc.evidence}}})
		if err != nil {
			t.Fatal(err)
		}
		var req struct {
			Messages []struct{ Role, Content string }
		}
		if err = json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if len(req.Messages) != 2 || !strings.Contains(req.Messages[0].Content, tc.rule) || !strings.Contains(req.Messages[1].Content, tc.evidence) {
			t.Errorf("missing actionable conclusion contract for %s", tc.question)
		}
	}
	for _, bad := range []string{"最多追问两个", "任务状态说明和日志不是根因证明"} {
		if strings.Contains(systemPrompt, bad) {
			t.Errorf("blanket hedge remains: %s", bad)
		}
	}
}
