package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type anthropicCookieAuthRequest struct {
	SessionKey string `json:"session_key"`
	Code       string `json:"code"`
	ProxyURL   string `json:"proxy_url"`
	Prefix     string `json:"prefix"`
	Note       string `json:"note"`
}

func (h *Handler) PostAnthropicCookieAuth(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	var req anthropicCookieAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	sessionKey := strings.TrimSpace(req.SessionKey)
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(req.Code)
	}
	if sessionKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_key is required"})
		return
	}

	proxyURL := strings.TrimSpace(req.ProxyURL)
	authSvc := claude.NewClaudeAuthWithProxyURL(h.cfg, proxyURL)
	bundle, err := authSvc.CookieAuth(ctx, sessionKey)
	if err != nil {
		log.WithError(err).Warn("anthropic cookie auth failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	tokenStorage := authSvc.CreateTokenStorage(bundle)
	accountID := strings.TrimSpace(tokenStorage.Email)
	if accountID == "" {
		accountID = strings.TrimSpace(tokenStorage.AccountUUID)
	}
	if accountID == "" {
		accountID = fmt.Sprintf("cookie-%d", time.Now().Unix())
	}
	fileName := fmt.Sprintf("claude-%s.json", accountID)

	metadata := defaultClaudeAuthMetadata(tokenStorage.Email)
	applyClaudeTokenStorageMetadata(metadata, tokenStorage)
	if proxyURL != "" {
		metadata["proxy_url"] = proxyURL
	}
	if prefix := strings.TrimSpace(req.Prefix); prefix != "" {
		metadata["prefix"] = prefix
	}
	if note := strings.TrimSpace(req.Note); note != "" {
		metadata["note"] = note
	}

	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "claude",
		Prefix:   strings.TrimSpace(req.Prefix),
		FileName: fileName,
		Storage:  tokenStorage,
		ProxyURL: proxyURL,
		Metadata: metadata,
		Attributes: map[string]string{
			"cloak_mode":          "always",
			"cloak_cache_user_id": "true",
		},
	}

	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		log.WithError(err).Error("failed to save anthropic cookie auth token")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save authentication tokens"})
		return
	}
	if err := h.autoAddProxyURL(ctx, proxyURL); err != nil {
		log.WithError(err).Warn("failed to auto add cookie auth proxy to proxy pool")
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            "ok",
		"auth_file":         fileName,
		"path":              savedPath,
		"email":             tokenStorage.Email,
		"auth_source":       tokenStorage.AuthSource,
		"auth_method_label": claudeAuthMethodLabel(tokenStorage.AuthSource),
		"token_endpoint":    tokenStorage.TokenEndpoint,
		"redirect_uri":      tokenStorage.RedirectURI,
	})
}
