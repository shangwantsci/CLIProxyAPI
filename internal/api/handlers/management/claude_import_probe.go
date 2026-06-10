package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

var claudeImportProbeURL = "https://api.anthropic.com/v1/messages?beta=true"

const (
	claudeImportProbeFallbackModel = "claude-sonnet-4-5"
	claudeImportProbeGap           = 700 * time.Millisecond
	claudeImportProbeMaxBody       = 1 << 20
	claudeImportProbeAttempts      = 2
	claudeImportProbeTimeout       = 20 * time.Second
)

// claudeImportPermanentError marks an import liveness probe that hit a permanent
// account error (e.g. organization disabled). Carrying Code lets the bulk import
// summary classify the rejection reason; Error() is the single-import message.
type claudeImportPermanentError struct {
	Code    string
	Message string
}

func (e *claudeImportPermanentError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = strings.TrimSpace(e.Code)
	}
	return fmt.Sprintf("认证成功但测试请求失败：%s，未加入账号池", msg)
}

// defaultClaudeProbeModel picks an available claude model so the probe request
// reaches organization-level validation instead of being rejected at model
// lookup (which would mask organization_disabled).
func defaultClaudeProbeModel() string {
	models := registry.GetGlobalRegistry().GetAvailableModels("claude")
	for _, m := range models {
		if id, ok := m["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return claudeImportProbeFallbackModel
}

// probeClaudeImportLiveness sends two minimal /v1/messages probes over the SAME
// proxy the account will use after import. Returns a *claudeImportPermanentError
// if either probe hits a permanent account error; transient failures (timeout,
// 429, 5xx, network, non-permanent errors) return nil so good accounts are not
// rejected.
func (h *Handler) probeClaudeImportLiveness(ctx context.Context, proxyURL, token, model string) error {
	return h.probeClaudeImportLivenessTo(ctx, claudeImportProbeURL, proxyURL, token, model)
}

func (h *Handler) probeClaudeImportLivenessTo(ctx context.Context, endpoint, proxyURL, token, model string) error {
	probeCtx, cancel := context.WithTimeout(ctx, claudeImportProbeTimeout)
	defer cancel()
	for i := 0; i < claudeImportProbeAttempts; i++ {
		if i > 0 {
			select {
			case <-probeCtx.Done():
				return nil // transient: do not reject on cancellation
			case <-time.After(claudeImportProbeGap):
			}
		}
		if permErr := h.singleClaudeImportProbeTo(probeCtx, endpoint, proxyURL, token, model); permErr != nil {
			return permErr
		}
	}
	return nil
}

func (h *Handler) singleClaudeImportProbeTo(ctx context.Context, endpoint, proxyURL, token, model string) error {
	reqBody, errMarshal := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1,
		"messages":   []map[string]any{{"role": "user", "content": "."}},
		"system":     []map[string]any{{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}},
	})
	if errMarshal != nil {
		return nil // transient: cannot build probe payload, do not reject
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil // transient
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	if h != nil && h.cfg != nil {
		if ua := strings.TrimSpace(h.cfg.ClaudeHeaderDefaults.UserAgent); ua != "" {
			req.Header.Set("User-Agent", ua)
		}
	}

	client := &http.Client{}
	if strings.TrimSpace(proxyURL) != "" {
		transport, _, errTransport := proxyutil.BuildHTTPTransport(proxyURL)
		if errTransport != nil {
			return nil // transient: cannot build proxy transport, do not reject
		}
		if transport != nil {
			client.Transport = transport
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil // transient: network error, do not reject
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("probe response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, claudeImportProbeMaxBody))
	code, message := claudeOAuthProbeError(resp.StatusCode, body)
	if isClaudePermanentAccountError(code, message) {
		return &claudeImportPermanentError{Code: code, Message: message}
	}
	if resp.StatusCode == http.StatusBadRequest {
		if strings.TrimSpace(message) == "" {
			message = "Claude import probe returned HTTP 400."
		}
		return &claudeImportPermanentError{Code: "probe_bad_request", Message: message}
	}
	return nil // non-permanent error: allow import
}
