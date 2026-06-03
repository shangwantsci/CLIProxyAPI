package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// 已伪装请求:auditClaudeMimicryForGuard 应写入全局快照(驱动徽章)。
func TestAuditForGuard_CloakedUpdatesLatest(t *testing.T) {
	resetClaudeDeviceProfileCache()
	ResetClaudeMimicryAuditForTest()
	t.Cleanup(ResetClaudeMimicryAuditForTest)

	payload := buildSignedClaudeMimicryTestPayload(t)
	req := newClaudeHeaderTestRequest(t, nil)
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})

	auditClaudeMimicryForGuard("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{}, true /*cloaked*/)
	if _, ok := LatestClaudeMimicryAudit(); !ok {
		t.Fatalf("cloaked request must update latest snapshot")
	}
}

// 未伪装请求:不写全局快照(不拉低徽章),但仍返回有效 snapshot 供 guard 评估。
func TestAuditForGuard_UncloakedDoesNotUpdateLatest(t *testing.T) {
	resetClaudeDeviceProfileCache()
	ResetClaudeMimicryAuditForTest()
	t.Cleanup(ResetClaudeMimicryAuditForTest)

	dirtyBody := []byte(`{"model":"claude-sonnet-4-6","messages":[]}`)
	snapshot := auditClaudeMimicryForGuard("claude-sonnet-4-6", "/v1/messages", dirtyBody, http.Header{}, &config.Config{}, false /*uncloaked*/)

	if _, ok := LatestClaudeMimicryAudit(); ok {
		t.Fatalf("uncloaked request must NOT write global latest snapshot")
	}
	if snapshot.Status == "" {
		t.Fatalf("uncloaked audit must still return a scored snapshot for guard evaluation")
	}
}

// 未伪装请求仍被 guard 评估:strict 模式下脏请求仍进入 block 路径。
func TestAuditForGuard_UncloakedStillEvaluatedByGuard(t *testing.T) {
	resetClaudeDeviceProfileCache()
	ResetClaudeMimicryAuditForTest()
	t.Cleanup(ResetClaudeMimicryAuditForTest)

	cfg := &config.Config{}
	cfg.ClaudeMimicryGuard.Mode = "strict"
	dirtyBody := []byte(`{"model":"claude-sonnet-4-6","messages":[]}`)
	audit := auditClaudeMimicryForGuard("claude-sonnet-4-6", "/v1/messages", dirtyBody, http.Header{}, cfg, false /*uncloaked*/)
	decision := EvaluateClaudeMimicryGuard(audit, cfg)
	if !decision.Blocked {
		t.Fatalf("strict guard must still block an uncloaked dirty request; cloaked=false must NOT bypass guard. decision=%#v", decision)
	}
}
