package management

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
)

// GetClaudeMimicryAudit returns the latest verified outbound Claude request
// shape. It intentionally exposes hashes and counts instead of raw payload text.
func (h *Handler) GetClaudeMimicryAudit(c *gin.Context) {
	audit, ok := executor.LatestClaudeMimicryAudit()
	if !ok {
		model := strings.TrimSpace(c.Query("model"))
		audit = executor.WaitingClaudeMimicryAudit(model, h.cfg)
	}
	c.JSON(200, gin.H{
		"has_recent_request": ok,
		"audit":              audit,
	})
}

// GetClaudeMimicryEvents returns recent redacted guard decisions and per-client
// source counters for production troubleshooting.
func (h *Handler) GetClaudeMimicryEvents(c *gin.Context) {
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
			return
		}
		limit = parsed
		if parsed > 500 {
			limit = 500
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"events":       executor.LatestClaudeMimicryEvents(limit),
		"client_stats": executor.ClaudeMimicryClientStats(),
	})
}
