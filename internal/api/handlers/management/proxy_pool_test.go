package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestProxyPoolCRUDRedactsAndCountsUsage(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "claude-a.json",
		FileName: "claude-a.json",
		Provider: "claude",
		ProxyURL: "socks5://user:pass@127.0.0.1:1080",
	}
	if _, err := manager.Register(context.Background(), record); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	createBody := `{"name":"local socks","url":"socks5://user:pass@127.0.0.1:1080","note":"primary"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/proxy-pool", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.CreateProxyPoolEntry(ctx)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var createResp struct {
		Proxy proxyPoolEntry `json:"proxy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResp.Proxy.URL != "socks5://user:pass@127.0.0.1:1080" {
		t.Fatalf("proxy url = %q", createResp.Proxy.URL)
	}
	if createResp.Proxy.RedactedURL != "socks5://redacted@127.0.0.1:1080" {
		t.Fatalf("redacted url = %q", createResp.Proxy.RedactedURL)
	}
	if createResp.Proxy.UsageCount != 1 {
		t.Fatalf("usage count = %d, want 1", createResp.Proxy.UsageCount)
	}

	listRec := httptest.NewRecorder()
	listCtx, _ := gin.CreateTestContext(listRec)
	listCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/proxy-pool", nil)
	h.ListProxyPool(listCtx)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Proxies []proxyPoolEntry `json:"proxies"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp.Proxies) != 1 {
		t.Fatalf("proxy count = %d, want 1", len(listResp.Proxies))
	}
	if listResp.Proxies[0].UsageCount != 1 {
		t.Fatalf("listed usage count = %d, want 1", listResp.Proxies[0].UsageCount)
	}

	deleteRec := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(deleteRec)
	deleteCtx.Params = gin.Params{{Key: "id", Value: createResp.Proxy.ID}}
	deleteCtx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/proxy-pool/"+createResp.Proxy.ID, nil)
	h.DeleteProxyPoolEntry(deleteCtx)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
}

func TestPatchAuthFileFieldsAutoAddsProxyToPool(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "claude-a.json",
		FileName: "claude-a.json",
		Provider: "claude",
		Metadata: map[string]any{
			"type": "claude",
		},
	}
	if _, err := manager.Register(context.Background(), record); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"claude-a.json","proxy_url":"http://user:pass@proxy.example.com:8080"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", rec.Code, rec.Body.String())
	}

	proxies, err := h.loadProxyPool(context.Background())
	if err != nil {
		t.Fatalf("load proxy pool: %v", err)
	}
	if len(proxies) != 1 {
		t.Fatalf("proxy pool count = %d, want 1", len(proxies))
	}
	if proxies[0].URL != "http://user:pass@proxy.example.com:8080" {
		t.Fatalf("proxy pool url = %q", proxies[0].URL)
	}
}
