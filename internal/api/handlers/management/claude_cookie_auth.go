package management

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
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
	result, err := h.saveClaudeSessionKeyAuth(ctx, claudeSessionImportAuthRequest{
		SessionKey: sessionKey,
		ProxyURL:   proxyURL,
		Prefix:     req.Prefix,
		Note:       req.Note,
	})
	if err != nil {
		log.WithError(err).Warn("anthropic cookie auth failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            "ok",
		"auth_file":         result.AuthFile,
		"path":              result.Path,
		"email":             result.Email,
		"auth_source":       result.AuthSource,
		"auth_method_label": result.AuthMethodLabel,
		"token_endpoint":    result.TokenEndpoint,
		"redirect_uri":      result.RedirectURI,
	})
}
