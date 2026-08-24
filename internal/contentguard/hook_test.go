package contentguard

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"golang.org/x/net/context"
)

type captureUsagePlugin struct {
	records chan usage.Record
}

func (p *captureUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if record.ExecutorType != ExecutorType {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func waitUsage(t *testing.T, ch <-chan usage.Record) usage.Record {
	t.Helper()
	select {
	case rec := <-ch:
		return rec
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for content_guard usage")
		return usage.Record{}
	}
}

func assertNoUsage(t *testing.T, ch <-chan usage.Record) {
	t.Helper()
	select {
	case rec := <-ch:
		t.Fatalf("unexpected usage record: %+v", rec)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestInterceptObservePublishesWithoutBlocking(t *testing.T) {
	plugin := &captureUsagePlugin{records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin("contentguard-observe", plugin)
	t.Cleanup(func() { usage.RegisterNamedPlugin("contentguard-observe", noopUsage{}) })

	cfg := &config.SDKConfig{ContentGuard: &config.ContentGuardConfig{
		Enabled: boolPtr(true),
		Mode:    config.ContentGuardModeObserve,
	}}
	body := []byte(`{"messages":[{"role":"user","content":"how to kill myself"}]}`)
	if errMsg := Intercept(context.Background(), cfg, "openai", "gpt-4", "gpt-4", body); errMsg != nil {
		t.Fatalf("observe should not block: %+v", errMsg)
	}
	rec := waitUsage(t, plugin.records)
	if rec.Failed {
		t.Fatal("observe must set Failed=false")
	}
	if rec.Source != "" || rec.AuthID != "" || rec.AuthIndex != "" {
		t.Fatalf("identity fields must stay empty: %+v", rec)
	}
	if rec.Fail.Body != "content_policy_violation:self_harm_instructions:standard" {
		t.Fatalf("fail body = %q", rec.Fail.Body)
	}
}

func TestInterceptEnforceBlocks(t *testing.T) {
	plugin := &captureUsagePlugin{records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin("contentguard-enforce", plugin)
	t.Cleanup(func() { usage.RegisterNamedPlugin("contentguard-enforce", noopUsage{}) })

	cfg := &config.SDKConfig{ContentGuard: &config.ContentGuardConfig{
		Enabled: boolPtr(true),
		Mode:    config.ContentGuardModeEnforce,
	}}
	body := []byte(`{"messages":[{"role":"user","content":"how to kill myself"}]}`)
	errMsg := Intercept(context.Background(), cfg, "openai", "gpt-4", "gpt-4", body)
	if errMsg == nil || !errMsg.DirectResponse || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("enforce error = %+v", errMsg)
	}
	if !strings.Contains(string(errMsg.Body), "content_policy_violation") {
		t.Fatalf("client body = %s", errMsg.Body)
	}
	rec := waitUsage(t, plugin.records)
	if !rec.Failed || rec.Fail.StatusCode != http.StatusForbidden {
		t.Fatalf("enforce usage = %+v", rec)
	}
	if rec.Source != "" {
		t.Fatalf("source must be empty, got %q", rec.Source)
	}
}

func TestInterceptDisabledAndPassSkipUsage(t *testing.T) {
	plugin := &captureUsagePlugin{records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin("contentguard-pass", plugin)
	t.Cleanup(func() { usage.RegisterNamedPlugin("contentguard-pass", noopUsage{}) })

	disabled := &config.SDKConfig{ContentGuard: &config.ContentGuardConfig{Enabled: boolPtr(false), Mode: config.ContentGuardModeEnforce}}
	hitBody := []byte(`{"messages":[{"role":"user","content":"how to kill myself"}]}`)
	if errMsg := Intercept(context.Background(), disabled, "openai", "gpt-4", "gpt-4", hitBody); errMsg != nil {
		t.Fatalf("disabled must pass: %+v", errMsg)
	}
	passBody := []byte(`{"messages":[{"role":"user","content":"hello from a normal coding session"}]}`)
	enabled := &config.SDKConfig{ContentGuard: &config.ContentGuardConfig{Enabled: boolPtr(true), Mode: config.ContentGuardModeEnforce}}
	if errMsg := Intercept(context.Background(), enabled, "openai", "gpt-4", "gpt-4", passBody); errMsg != nil {
		t.Fatalf("benign enforce must pass: %+v", errMsg)
	}
	assertNoUsage(t, plugin.records)
}

func TestPublishFolderFoldsPerKey(t *testing.T) {
	t.Parallel()
	folder := &publishFolder{hits: map[string][]time.Time{}}
	now := time.Now()
	for i := 0; i < publishMaxPerKey; i++ {
		if !folder.allow("key-a", now) {
			t.Fatalf("allowed count %d should pass", i)
		}
	}
	if folder.allow("key-a", now) {
		t.Fatal("over-limit key should fold")
	}
	if !folder.allow("key-b", now) {
		t.Fatal("other key should not fold")
	}
}

type noopUsage struct{}

func (noopUsage) HandleUsage(context.Context, usage.Record) {}
