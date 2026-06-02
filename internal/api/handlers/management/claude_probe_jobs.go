package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	defaultClaudeProbeConcurrency = 8
	maxClaudeProbeConcurrency     = 24
	claudeProbeAccountTimeout     = 25 * time.Second
	claudeProbeJobRetention       = 6 * time.Hour
)

var (
	claudeOAuthProfileURL = "https://api.anthropic.com/api/oauth/profile"
	claudeOAuthUsageURL   = "https://api.anthropic.com/api/oauth/usage"
)

type claudeProbeJobRequest struct {
	Names           []string `json:"names"`
	IncludeDisabled *bool    `json:"include_disabled"`
	Concurrency     int      `json:"concurrency"`
}

type claudeProbeJob struct {
	ID                string
	Status            string
	CreatedAt         time.Time
	StartedAt         time.Time
	FinishedAt        time.Time
	Total             int
	Completed         int
	OK                int
	Failed            int
	Disabled          int
	PermanentDisabled int
	ManualDisabled    int
	AuthExpired       int
	Quota             int
	RateLimited       int
	Error             string
	Results           []claudeProbeResult
	cancel            context.CancelFunc
}

type claudeProbeJobSnapshot struct {
	ID                string              `json:"id"`
	Status            string              `json:"status"`
	CreatedAt         time.Time           `json:"created_at"`
	StartedAt         time.Time           `json:"started_at,omitempty"`
	FinishedAt        time.Time           `json:"finished_at,omitempty"`
	Total             int                 `json:"total"`
	Completed         int                 `json:"completed"`
	OK                int                 `json:"ok"`
	Failed            int                 `json:"failed"`
	Disabled          int                 `json:"disabled"`
	PermanentDisabled int                 `json:"permanent_disabled"`
	ManualDisabled    int                 `json:"manual_disabled"`
	AuthExpired       int                 `json:"auth_expired"`
	Quota             int                 `json:"quota_cooldown"`
	RateLimited       int                 `json:"rate_limited"`
	Error             string              `json:"error,omitempty"`
	Results           []claudeProbeResult `json:"results"`
}

type claudeProbeResult struct {
	Name               string    `json:"name"`
	ID                 string    `json:"id,omitempty"`
	AuthIndex          string    `json:"auth_index,omitempty"`
	Email              string    `json:"email,omitempty"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	RouteState         string    `json:"route_state,omitempty"`
	Recoverability     string    `json:"recoverability,omitempty"`
	CleanupRecommended bool      `json:"cleanup_recommended,omitempty"`
	ProfileStatus      int       `json:"profile_status,omitempty"`
	UsageStatus        int       `json:"usage_status,omitempty"`
	Disabled           bool      `json:"disabled"`
	Unavailable        bool      `json:"unavailable"`
	StartedAt          time.Time `json:"started_at"`
	FinishedAt         time.Time `json:"finished_at"`
}

func (h *Handler) PostClaudeProbeJob(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req claudeProbeJobRequest
	if c.Request != nil && c.Request.Body != nil {
		if err := c.ShouldBindJSON(&req); err != nil && err != io.EOF {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
	}

	targets := h.claudeProbeTargets(req)
	concurrency := normalizeClaudeProbeConcurrency(req.Concurrency)
	now := time.Now()
	job := &claudeProbeJob{
		ID:        uuid.NewString(),
		Status:    "running",
		CreatedAt: now,
		StartedAt: now,
		Total:     len(targets),
		Results:   make([]claudeProbeResult, 0, len(targets)),
	}
	if len(targets) == 0 {
		job.Status = "completed"
		job.FinishedAt = now
	}

	ctx, cancel := context.WithCancel(context.Background())
	job.cancel = cancel
	h.storeClaudeProbeJob(job)

	if len(targets) > 0 {
		go h.runClaudeProbeJob(ctx, job.ID, targets, concurrency)
	}

	c.JSON(http.StatusOK, h.mustClaudeProbeJobSnapshot(job.ID))
}

func (h *Handler) GetClaudeProbeJob(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	snapshot, ok := h.claudeProbeJobSnapshot(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func (h *Handler) CancelClaudeProbeJob(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	h.claudeProbeMu.Lock()
	job := h.claudeProbeJobs[id]
	if job == nil {
		h.claudeProbeMu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	if job.cancel != nil && job.Status == "running" {
		job.cancel()
		job.Status = "canceling"
	}
	snapshot := claudeProbeJobSnapshotFromLocked(job)
	h.claudeProbeMu.Unlock()
	c.JSON(http.StatusOK, snapshot)
}

func (h *Handler) claudeProbeTargets(req claudeProbeJobRequest) []*coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	includeDisabled := true
	if req.IncludeDisabled != nil {
		includeDisabled = *req.IncludeDisabled
	}

	seen := make(map[string]struct{})
	targets := make([]*coreauth.Auth, 0)
	add := func(auth *coreauth.Auth) {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
			return
		}
		if h.buildAuthFileEntry(auth) == nil {
			return
		}
		if !includeDisabled && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
			return
		}
		if _, ok := seen[auth.ID]; ok {
			return
		}
		seen[auth.ID] = struct{}{}
		auth.EnsureIndex()
		targets = append(targets, auth)
	}

	if len(req.Names) > 0 {
		for _, name := range req.Names {
			if auth := h.findAuthByNameOrID(name); auth != nil {
				add(auth)
			}
		}
		return targets
	}

	for _, auth := range h.authManager.List() {
		add(auth)
	}
	sort.Slice(targets, func(i, j int) bool {
		return strings.ToLower(targets[i].FileName) < strings.ToLower(targets[j].FileName)
	})
	return targets
}

func normalizeClaudeProbeConcurrency(value int) int {
	if value <= 0 {
		return defaultClaudeProbeConcurrency
	}
	if value > maxClaudeProbeConcurrency {
		return maxClaudeProbeConcurrency
	}
	return value
}

func (h *Handler) storeClaudeProbeJob(job *claudeProbeJob) {
	if h == nil || job == nil {
		return
	}
	h.claudeProbeMu.Lock()
	if h.claudeProbeJobs == nil {
		h.claudeProbeJobs = make(map[string]*claudeProbeJob)
	}
	h.pruneClaudeProbeJobsLocked(time.Now())
	h.claudeProbeJobs[job.ID] = job
	h.claudeProbeMu.Unlock()
}

func (h *Handler) pruneClaudeProbeJobsLocked(now time.Time) {
	for id, job := range h.claudeProbeJobs {
		if job == nil || job.Status == "running" || job.Status == "canceling" || job.FinishedAt.IsZero() {
			continue
		}
		if now.Sub(job.FinishedAt) > claudeProbeJobRetention {
			delete(h.claudeProbeJobs, id)
		}
	}
}

func (h *Handler) runClaudeProbeJob(ctx context.Context, jobID string, targets []*coreauth.Auth, concurrency int) {
	workerSlots := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, auth := range targets {
		select {
		case <-ctx.Done():
			h.finishClaudeProbeJob(jobID, "canceled", "")
			return
		default:
		}

		workerSlots <- struct{}{}
		wg.Add(1)
		go func(auth *coreauth.Auth) {
			defer wg.Done()
			defer func() { <-workerSlots }()
			result := h.runClaudeProbeForAuth(ctx, auth)
			h.appendClaudeProbeResult(jobID, result)
		}(auth)
	}

	wg.Wait()
	status := "completed"
	if ctx.Err() != nil {
		status = "canceled"
	}
	h.finishClaudeProbeJob(jobID, status, "")
}

func (h *Handler) runClaudeProbeForAuth(parent context.Context, auth *coreauth.Auth) claudeProbeResult {
	now := time.Now()
	result := claudeProbeResult{
		ID:        strings.TrimSpace(auth.ID),
		Name:      strings.TrimSpace(auth.FileName),
		AuthIndex: strings.TrimSpace(auth.EnsureIndex()),
		Email:     authEmail(auth),
		Status:    "error",
		StartedAt: now,
	}
	if result.Name == "" {
		result.Name = result.ID
	}

	ctx, cancel := context.WithTimeout(parent, claudeProbeAccountTimeout)
	defer cancel()

	token, errToken := h.resolveTokenForAuth(ctx, auth)
	if errToken != nil && strings.TrimSpace(token) == "" {
		result.Message = errToken.Error()
		h.recordClaudeOAuthProbeTokenError(context.Background(), auth, errToken)
		return h.finalizeClaudeProbeResult(auth, result)
	}
	if strings.TrimSpace(token) == "" {
		errToken = fmt.Errorf("Claude access token missing")
		result.Message = errToken.Error()
		h.recordClaudeOAuthProbeTokenError(context.Background(), auth, errToken)
		return h.finalizeClaudeProbeResult(auth, result)
	}
	if isClaudeSetupTokenAuth(auth) {
		if updated, ok := h.authManager.GetByID(auth.ID); ok && !isClaudeOAuthQuotaCooldown(updated) {
			h.markClaudeProbeHealthy(context.Background(), updated)
		}
		return h.finalizeClaudeProbeResult(auth, result)
	}

	profileURL, errParseProfile := url.Parse(claudeOAuthProfileURL)
	if errParseProfile != nil {
		result.Message = "invalid Claude profile probe URL"
		return h.finalizeClaudeProbeResult(auth, result)
	}
	profileStatus, profileHeader, profileBody, errProfile := h.claudeOAuthProbeGET(ctx, auth, claudeOAuthProfileURL, token)
	result.ProfileStatus = profileStatus
	if errProfile != nil {
		result.Message = errProfile.Error()
		h.recordClaudeOAuthTransientProbeError(context.Background(), auth, "profile_probe_failed", errProfile)
		return h.finalizeClaudeProbeResult(auth, result)
	}
	h.recordClaudeOAuthProbeResult(context.Background(), auth, profileURL, profileStatus, profileHeader, profileBody)
	if profileStatus < http.StatusOK || profileStatus >= http.StatusMultipleChoices {
		h.recordClaudeOAuthProbeHTTPFallback(context.Background(), auth, profileStatus, profileBody)
		return h.finalizeClaudeProbeResult(auth, result)
	}
	if !gjson.ValidBytes(profileBody) {
		h.recordClaudeOAuthTransientProbeError(context.Background(), auth, "profile_probe_invalid_json", fmt.Errorf("Claude profile probe response was not JSON"))
		return h.finalizeClaudeProbeResult(auth, result)
	}

	usageURL, errParseUsage := url.Parse(claudeOAuthUsageURL)
	if errParseUsage != nil {
		result.Message = "invalid Claude usage probe URL"
		return h.finalizeClaudeProbeResult(auth, result)
	}
	usageStatus, usageHeader, usageBody, errUsage := h.claudeOAuthProbeGET(ctx, auth, claudeOAuthUsageURL, token)
	result.UsageStatus = usageStatus
	if errUsage != nil {
		result.Message = errUsage.Error()
		h.recordClaudeOAuthTransientProbeError(context.Background(), auth, "usage_probe_failed", errUsage)
		return h.finalizeClaudeProbeResult(auth, result)
	}
	h.recordClaudeOAuthProbeResult(context.Background(), auth, usageURL, usageStatus, usageHeader, usageBody)
	if usageStatus < http.StatusOK || usageStatus >= http.StatusMultipleChoices {
		h.recordClaudeOAuthProbeHTTPFallback(context.Background(), auth, usageStatus, usageBody)
		return h.finalizeClaudeProbeResult(auth, result)
	}
	if usageState, ok := claudeOAuthUsageQuotaState(usageBody, time.Now(), h.claudeOAuthUsageThresholds()); !ok {
		h.recordClaudeOAuthTransientProbeError(context.Background(), auth, "usage_probe_invalid_json", fmt.Errorf("Claude usage probe response did not contain quota windows"))
		return h.finalizeClaudeProbeResult(auth, result)
	} else if usageState.exceeded {
		return h.finalizeClaudeProbeResult(auth, result)
	}
	if usageStatus >= http.StatusOK && usageStatus < http.StatusMultipleChoices {
		if updated, ok := h.authManager.GetByID(auth.ID); ok && !isClaudeOAuthQuotaCooldown(updated) {
			h.markClaudeProbeHealthy(context.Background(), updated)
		}
	}

	return h.finalizeClaudeProbeResult(auth, result)
}

func (h *Handler) claudeOAuthProbeGET(ctx context.Context, auth *coreauth.Auth, endpoint, token string) (int, http.Header, []byte, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errReq != nil {
		return 0, nil, nil, errReq
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	client := &http.Client{
		Timeout:   claudeProbeAccountTimeout,
		Transport: h.apiCallTransport(auth),
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return 0, nil, nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return resp.StatusCode, resp.Header, nil, errRead
	}
	return resp.StatusCode, resp.Header, body, nil
}

// claudeMessagesProbePOST sends one real /v1/messages probe for a setup-token
// account (which cannot access the OAuth profile/usage endpoints), over the same
// proxy the account uses for business traffic. Returns the HTTP status, headers,
// body, and a transport-level error (network/timeout). Status/body interpretation
// is left to the caller.
func (h *Handler) claudeMessagesProbePOST(ctx context.Context, auth *coreauth.Auth, token string) (int, http.Header, []byte, error) {
	return h.claudeMessagesProbePOSTTo(ctx, claudeImportProbeURL, auth, token)
}

func (h *Handler) claudeMessagesProbePOSTTo(ctx context.Context, endpoint string, auth *coreauth.Auth, token string) (int, http.Header, []byte, error) {
	reqBody, errMarshal := json.Marshal(map[string]any{
		"model":      defaultClaudeProbeModel(),
		"max_tokens": 1,
		"messages":   []map[string]any{{"role": "user", "content": "."}},
		"system":     []map[string]any{{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}},
	})
	if errMarshal != nil {
		return 0, nil, nil, errMarshal
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if errReq != nil {
		return 0, nil, nil, errReq
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

	client := &http.Client{
		Timeout:   claudeProbeAccountTimeout,
		Transport: h.apiCallTransport(auth),
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return 0, nil, nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, claudeImportProbeMaxBody))
	if errRead != nil {
		return resp.StatusCode, resp.Header, nil, errRead
	}
	return resp.StatusCode, resp.Header, body, nil
}

func (h *Handler) markClaudeProbeHealthy(ctx context.Context, auth *coreauth.Auth) {
	if h == nil || h.authManager == nil || auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return
	}
	updated := auth.Clone()
	updated.Status = coreauth.StatusActive
	updated.StatusMessage = ""
	updated.Unavailable = false
	updated.NextRetryAfter = time.Time{}
	updated.Quota = coreauth.QuotaState{}
	updated.LastError = nil
	updated.UpdatedAt = time.Now()
	_, _ = h.authManager.Update(ctx, updated)
}

func (h *Handler) recordClaudeOAuthProbeTokenError(ctx context.Context, auth *coreauth.Auth, err error) {
	if h == nil || h.authManager == nil || auth == nil || err == nil {
		return
	}
	now := time.Now()
	code := "probe_failed"
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "Claude token probe failed"
	}
	disable := isClaudeProbeAuthExpiredError(message)
	if disable {
		code = "auth_expired"
	}
	updated := auth.Clone()
	updated.LastError = &coreauth.Error{
		Code:       code,
		Message:    message,
		Retryable:  !disable,
		HTTPStatus: 0,
	}
	updated.StatusMessage = message
	updated.Unavailable = true
	updated.UpdatedAt = now
	if disable {
		updated.Disabled = true
		updated.Status = coreauth.StatusDisabled
		updated.NextRetryAfter = time.Time{}
	} else if !updated.Disabled && updated.Status != coreauth.StatusDisabled {
		updated.Status = coreauth.StatusError
		updated.NextRetryAfter = now.Add(30 * time.Minute)
	}
	_, _ = h.authManager.Update(ctx, updated)
}

func (h *Handler) recordClaudeOAuthTransientProbeError(ctx context.Context, auth *coreauth.Auth, code string, err error) {
	if h == nil || h.authManager == nil || auth == nil || err == nil {
		return
	}
	now := time.Now()
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "Claude probe failed"
	}
	updated := auth.Clone()
	updated.LastError = &coreauth.Error{
		Code:      strings.TrimSpace(code),
		Message:   message,
		Retryable: true,
	}
	updated.StatusMessage = message
	updated.Unavailable = true
	updated.UpdatedAt = now
	if !updated.Disabled && updated.Status != coreauth.StatusDisabled {
		updated.Status = coreauth.StatusError
		updated.NextRetryAfter = now.Add(30 * time.Minute)
	}
	_, _ = h.authManager.Update(ctx, updated)
}

func (h *Handler) recordClaudeOAuthProbeHTTPFallback(ctx context.Context, auth *coreauth.Auth, statusCode int, body []byte) {
	if h == nil || h.authManager == nil || auth == nil {
		return
	}
	if updated, ok := h.authManager.GetByID(auth.ID); ok && updated.LastError != nil && updated.LastError.HTTPStatus == statusCode {
		return
	}
	code, message := claudeOAuthProbeError(statusCode, body)
	if isClaudeSetupTokenScopeRequirementError(auth, statusCode, code, message, body) {
		return
	}
	if isClaudePermanentAccountError(code, message) {
		return
	}
	now := time.Now()
	updated := auth.Clone()
	updated.LastError = &coreauth.Error{
		Code:       code,
		Message:    message,
		Retryable:  statusCode == http.StatusTooManyRequests || statusCode >= 500,
		HTTPStatus: statusCode,
	}
	updated.StatusMessage = message
	updated.Unavailable = true
	updated.UpdatedAt = now
	if !updated.Disabled && updated.Status != coreauth.StatusDisabled {
		updated.Status = coreauth.StatusError
		if statusCode == http.StatusTooManyRequests {
			updated.NextRetryAfter = claudeOAuthRetryAfter(http.Header{}, now)
		} else {
			updated.NextRetryAfter = now.Add(30 * time.Minute)
		}
	}
	_, _ = h.authManager.Update(ctx, updated)
}

func isClaudeProbeAuthExpiredError(message string) bool {
	raw := strings.ToLower(strings.TrimSpace(message))
	if raw == "" {
		return false
	}
	return strings.Contains(raw, "invalid_grant") ||
		strings.Contains(raw, "invalid authentication credentials") ||
		strings.Contains(raw, "unauthorized") ||
		strings.Contains(raw, "refresh token missing") ||
		strings.Contains(raw, "refresh token not found") ||
		strings.Contains(raw, "refresh token") && strings.Contains(raw, "invalid") ||
		strings.Contains(raw, "access token missing") ||
		strings.Contains(raw, "token missing")
}

func (h *Handler) finalizeClaudeProbeResult(original *coreauth.Auth, result claudeProbeResult) claudeProbeResult {
	now := time.Now()
	result.FinishedAt = now
	auth := original
	if h != nil && h.authManager != nil && original != nil {
		if updated, ok := h.authManager.GetByID(original.ID); ok {
			auth = updated
		}
	}
	if auth == nil {
		if result.Status == "" {
			result.Status = "error"
		}
		return result
	}

	reason := claudeAuthStatusReason(auth, now)
	result.Reason = reason
	result.RouteState = claudeAuthRouteState(auth, now, reason)
	result.Recoverability = claudeAuthRecoverability(reason)
	result.CleanupRecommended = result.Recoverability == "permanent"
	result.Disabled = auth.Disabled || auth.Status == coreauth.StatusDisabled
	result.Unavailable = auth.Unavailable
	if auth.LastError != nil && strings.TrimSpace(result.Message) == "" {
		result.Message = strings.TrimSpace(auth.LastError.Message)
	}
	if strings.TrimSpace(result.Message) == "" {
		result.Message = strings.TrimSpace(auth.StatusMessage)
	}

	switch reason {
	case "healthy":
		result.Status = "ok"
	case "quota_cooldown":
		result.Status = "quota_cooldown"
	case "auth_expired":
		result.Status = "auth_expired"
	case "account_banned", "organization_disabled", "account_disabled":
		result.Status = "permanent_disabled"
	case "manual_disabled", "disabled":
		result.Status = "manual_disabled"
	case "rate_limited", "rpm_cooldown":
		result.Status = "rate_limited"
	case "subscription_issue", "upstream_error", "unavailable":
		result.Status = "repair_required"
	default:
		if result.Disabled {
			result.Status = "manual_disabled"
		} else if auth.LastError != nil && auth.LastError.HTTPStatus == http.StatusTooManyRequests {
			result.Status = "rate_limited"
		} else {
			result.Status = "error"
		}
	}
	return result
}

func (h *Handler) appendClaudeProbeResult(jobID string, result claudeProbeResult) {
	if h == nil {
		return
	}
	h.claudeProbeMu.Lock()
	job := h.claudeProbeJobs[jobID]
	if job == nil {
		h.claudeProbeMu.Unlock()
		return
	}
	job.Completed++
	switch result.Status {
	case "ok":
		job.OK++
	case "quota_cooldown":
		job.Quota++
		job.Failed++
	case "auth_expired":
		job.AuthExpired++
		job.Disabled++
		job.Failed++
	case "permanent_disabled":
		job.PermanentDisabled++
		job.Disabled++
		job.Failed++
	case "manual_disabled":
		job.ManualDisabled++
		job.Disabled++
		job.Failed++
	case "rate_limited":
		job.RateLimited++
		job.Failed++
	default:
		job.Failed++
	}
	job.Results = append(job.Results, result)
	h.claudeProbeMu.Unlock()
}

func (h *Handler) finishClaudeProbeJob(jobID, status, message string) {
	if h == nil {
		return
	}
	h.claudeProbeMu.Lock()
	job := h.claudeProbeJobs[jobID]
	if job != nil {
		if status != "" {
			job.Status = status
		}
		job.FinishedAt = time.Now()
		job.Error = strings.TrimSpace(message)
		job.cancel = nil
	}
	h.claudeProbeMu.Unlock()
}

func (h *Handler) mustClaudeProbeJobSnapshot(id string) *claudeProbeJobSnapshot {
	snapshot, _ := h.claudeProbeJobSnapshot(id)
	return snapshot
}

func (h *Handler) claudeProbeJobSnapshot(id string) (*claudeProbeJobSnapshot, bool) {
	if h == nil {
		return nil, false
	}
	id = strings.TrimSpace(id)
	h.claudeProbeMu.Lock()
	job := h.claudeProbeJobs[id]
	if job == nil {
		h.claudeProbeMu.Unlock()
		return nil, false
	}
	snapshot := claudeProbeJobSnapshotFromLocked(job)
	h.claudeProbeMu.Unlock()
	return snapshot, true
}

func claudeProbeJobSnapshotFromLocked(job *claudeProbeJob) *claudeProbeJobSnapshot {
	if job == nil {
		return nil
	}
	results := append([]claudeProbeResult(nil), job.Results...)
	sort.Slice(results, func(i, j int) bool {
		if results[i].Status != results[j].Status {
			return results[i].Status < results[j].Status
		}
		return strings.ToLower(results[i].Name) < strings.ToLower(results[j].Name)
	})
	return &claudeProbeJobSnapshot{
		ID:                job.ID,
		Status:            job.Status,
		CreatedAt:         job.CreatedAt,
		StartedAt:         job.StartedAt,
		FinishedAt:        job.FinishedAt,
		Total:             job.Total,
		Completed:         job.Completed,
		OK:                job.OK,
		Failed:            job.Failed,
		Disabled:          job.Disabled,
		PermanentDisabled: job.PermanentDisabled,
		ManualDisabled:    job.ManualDisabled,
		AuthExpired:       job.AuthExpired,
		Quota:             job.Quota,
		RateLimited:       job.RateLimited,
		Error:             job.Error,
		Results:           results,
	}
}
