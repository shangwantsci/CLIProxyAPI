package management

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	defaultClaudeSessionImportSourceURL = "https://sessionkeytest.globalpays.shop/accounts"
	defaultClaudeSessionImportAPIPath   = "/api/accounts"
	maxClaudeSessionImportConcurrency   = 20
	maxClaudeSessionImportResults       = 500
	maxClaudeSessionImportBodyBytes     = 10 << 20

	claudeSessionImportStatusRunning   = "running"
	claudeSessionImportStatusCompleted = "completed"
	claudeSessionImportStatusFailed    = "failed"
	claudeSessionImportStatusCanceled  = "canceled"
)

type claudeSessionImportStartRequest struct {
	SourceURL      string   `json:"source_url"`
	APIPath        string   `json:"api_path"`
	SessionKeys    []string `json:"session_keys"`
	ProxyURL       string   `json:"proxy_url"`
	Prefix         string   `json:"prefix"`
	Note           string   `json:"note"`
	Concurrency    int      `json:"concurrency"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	DelayMinMS     int      `json:"delay_min_ms"`
	DelayMaxMS     int      `json:"delay_max_ms"`
}

type normalizedClaudeSessionImportRequest struct {
	SourceURL            string
	APIEndpoint          string
	DisplaySourceURL     string
	DisplayAPIEndpoint   string
	SessionKeys          []string
	SessionKeyDuplicates int
	ProxyURL             string
	ProxyCandidates      []string
	ImportSource         string
	Prefix               string
	Note                 string
	Concurrency          int
	Timeout              time.Duration
	DelayMin             time.Duration
	DelayMax             time.Duration
}

type claudeSessionImportAuthRequest struct {
	SessionKey   string
	ProxyURL     string
	Prefix       string
	Note         string
	ImportSource string
}

type claudeSessionImportAuthResult struct {
	AuthFile        string `json:"auth_file,omitempty"`
	Path            string `json:"path,omitempty"`
	Email           string `json:"email,omitempty"`
	AuthSource      string `json:"auth_source,omitempty"`
	AuthMethodLabel string `json:"auth_method_label,omitempty"`
	TokenEndpoint   string `json:"token_endpoint,omitempty"`
	RedirectURI     string `json:"redirect_uri,omitempty"`
}

type claudeSessionImportResult struct {
	SessionKeyHash  string `json:"session_key_hash"`
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	AuthFile        string `json:"auth_file,omitempty"`
	Email           string `json:"email,omitempty"`
	AuthSource      string `json:"auth_source,omitempty"`
	AuthMethodLabel string `json:"auth_method_label,omitempty"`
}

type claudeSessionImportJobSnapshot struct {
	ID               string                      `json:"id"`
	Status           string                      `json:"status"`
	SourceURL        string                      `json:"source_url"`
	APIEndpoint      string                      `json:"api_endpoint"`
	ProxyURL         string                      `json:"-"`
	RedactedProxyURL string                      `json:"redacted_proxy_url,omitempty"`
	Concurrency      int                         `json:"concurrency"`
	StartedAt        time.Time                   `json:"started_at"`
	UpdatedAt        time.Time                   `json:"updated_at"`
	FinishedAt       *time.Time                  `json:"finished_at,omitempty"`
	TotalFetched     int                         `json:"total_fetched"`
	TotalProcessed   int                         `json:"total_processed"`
	Imported         int                         `json:"imported"`
	Failed           int                         `json:"failed"`
	Duplicate        int                         `json:"duplicate"`
	Error            string                      `json:"error,omitempty"`
	FailureReasons   map[string]int              `json:"failure_reasons,omitempty"`
	Results          []claudeSessionImportResult `json:"results,omitempty"`
}

type claudeSessionImportJob struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	data   claudeSessionImportJobSnapshot
}

func (h *Handler) PostClaudeSessionImportJob(c *gin.Context) {
	var req claudeSessionImportStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	normalized, err := h.normalizeClaudeSessionImportRequest(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if normalized.ProxyURL == "" {
		proxyCandidates, err := h.enabledClaudeSessionImportProxyURLs(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		normalized.ProxyCandidates = proxyCandidates
	}

	ctx, cancel := context.WithCancel(PopulateAuthContext(context.Background(), c))
	now := time.Now().UTC()
	job := &claudeSessionImportJob{
		cancel: cancel,
		data: claudeSessionImportJobSnapshot{
			ID:               fmt.Sprintf("claude-session-import-%d", now.UnixNano()),
			Status:           claudeSessionImportStatusRunning,
			SourceURL:        normalized.DisplaySourceURL,
			APIEndpoint:      normalized.DisplayAPIEndpoint,
			ProxyURL:         normalized.ProxyURL,
			RedactedProxyURL: redactedClaudeSessionImportProxy(normalized),
			Concurrency:      normalized.Concurrency,
			StartedAt:        now,
			UpdatedAt:        now,
			FailureReasons:   map[string]int{},
		},
	}

	h.sessionImportMu.Lock()
	if h.sessionImportJobs == nil {
		h.sessionImportJobs = make(map[string]*claudeSessionImportJob)
	}
	if active := h.sessionImportJobs[h.sessionImportActive]; active != nil {
		activeSnap := active.snapshot()
		if activeSnap.Status == claudeSessionImportStatusRunning {
			h.sessionImportMu.Unlock()
			cancel()
			c.JSON(http.StatusConflict, gin.H{"error": "another session import job is running", "job_id": activeSnap.ID})
			return
		}
	}
	h.sessionImportJobs[job.data.ID] = job
	h.sessionImportActive = job.data.ID
	h.sessionImportMu.Unlock()

	go h.runClaudeSessionImportJob(ctx, job, normalized)

	c.JSON(http.StatusAccepted, gin.H{"status": claudeSessionImportStatusRunning, "job_id": job.data.ID, "job": job.snapshot()})
}

func (h *Handler) GetClaudeSessionImportJob(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	job := h.getClaudeSessionImportJob(id)
	if job == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session import job not found"})
		return
	}
	c.JSON(http.StatusOK, job.snapshot())
}

func (h *Handler) CancelClaudeSessionImportJob(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	job := h.getClaudeSessionImportJob(id)
	if job == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session import job not found"})
		return
	}
	job.cancel()
	c.JSON(http.StatusOK, job.snapshot())
}

func (h *Handler) getClaudeSessionImportJob(id string) *claudeSessionImportJob {
	if h == nil || id == "" {
		return nil
	}
	h.sessionImportMu.Lock()
	defer h.sessionImportMu.Unlock()
	return h.sessionImportJobs[id]
}

func (j *claudeSessionImportJob) snapshot() claudeSessionImportJobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := j.data
	if j.data.FailureReasons != nil {
		out.FailureReasons = make(map[string]int, len(j.data.FailureReasons))
		for key, value := range j.data.FailureReasons {
			out.FailureReasons[key] = value
		}
	}
	if j.data.Results != nil {
		out.Results = append([]claudeSessionImportResult(nil), j.data.Results...)
	}
	return out
}

func (j *claudeSessionImportJob) update(fn func(*claudeSessionImportJobSnapshot)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	fn(&j.data)
	j.data.UpdatedAt = time.Now().UTC()
}

func (h *Handler) normalizeClaudeSessionImportRequest(req claudeSessionImportStartRequest) (normalizedClaudeSessionImportRequest, error) {
	concurrency := req.Concurrency
	if concurrency <= 0 {
		concurrency = 10
	}
	if concurrency > maxClaudeSessionImportConcurrency {
		concurrency = maxClaudeSessionImportConcurrency
	}
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	if timeoutSeconds > 120 {
		timeoutSeconds = 120
	}
	delayMin := time.Duration(req.DelayMinMS) * time.Millisecond
	delayMax := time.Duration(req.DelayMaxMS) * time.Millisecond
	if delayMin < 0 {
		delayMin = 0
	}
	if delayMax < 0 {
		delayMax = 0
	}
	if delayMax < delayMin {
		delayMax = delayMin
	}

	sessionKeys, sessionKeyDuplicates := normalizeClaudeSessionImportKeys(req.SessionKeys)
	isManualSessionKeyImport := len(req.SessionKeys) > 0
	if isManualSessionKeyImport && len(sessionKeys) == 0 {
		return normalizedClaudeSessionImportRequest{}, fmt.Errorf("session_keys must contain at least one session key")
	}

	sourceURL := ""
	apiEndpoint := ""
	displaySourceURL := ""
	displayAPIEndpoint := ""
	importSource := ""
	if !isManualSessionKeyImport {
		sourceURL = strings.TrimSpace(req.SourceURL)
		if sourceURL == "" {
			sourceURL = defaultClaudeSessionImportSourceURL
		}
		source, err := url.Parse(sourceURL)
		if err != nil || source.Scheme == "" || source.Host == "" {
			return normalizedClaudeSessionImportRequest{}, fmt.Errorf("invalid source_url")
		}
		if err := h.validateClaudeSessionImportURL(source); err != nil {
			return normalizedClaudeSessionImportRequest{}, err
		}

		apiPath := strings.TrimSpace(req.APIPath)
		if apiPath == "" {
			apiPath = defaultClaudeSessionImportAPIPath
		}
		apiRef, err := url.Parse(apiPath)
		if err != nil || apiRef.IsAbs() || !strings.HasPrefix(apiRef.Path, "/") {
			return normalizedClaudeSessionImportRequest{}, fmt.Errorf("api_path must be an absolute path on the source host")
		}
		endpoint := *source
		endpoint.Path = apiRef.Path
		endpoint.RawQuery = apiRef.RawQuery
		endpoint.Fragment = ""
		if endpoint.Host != source.Host {
			return normalizedClaudeSessionImportRequest{}, fmt.Errorf("api_path must stay on the source host")
		}
		if err := h.validateClaudeSessionImportURL(&endpoint); err != nil {
			return normalizedClaudeSessionImportRequest{}, err
		}
		sourceURL = source.String()
		apiEndpoint = endpoint.String()
		displaySourceURL = source.String()
		displayAPIEndpoint = endpoint.String()
		if shouldHideClaudeSessionImportSource(source.Hostname()) {
			displaySourceURL = ""
			displayAPIEndpoint = ""
		}
		importSource = claudeImportSourceBulkSessionImport
	}

	return normalizedClaudeSessionImportRequest{
		SourceURL:            sourceURL,
		APIEndpoint:          apiEndpoint,
		DisplaySourceURL:     displaySourceURL,
		DisplayAPIEndpoint:   displayAPIEndpoint,
		SessionKeys:          sessionKeys,
		SessionKeyDuplicates: sessionKeyDuplicates,
		ProxyURL:             strings.TrimSpace(req.ProxyURL),
		ImportSource:         importSource,
		Prefix:               strings.TrimSpace(req.Prefix),
		Note:                 strings.TrimSpace(req.Note),
		Concurrency:          concurrency,
		Timeout:              time.Duration(timeoutSeconds) * time.Second,
		DelayMin:             delayMin,
		DelayMax:             delayMax,
	}, nil
}

func (h *Handler) validateClaudeSessionImportURL(parsed *url.URL) error {
	if parsed == nil || parsed.Host == "" {
		return fmt.Errorf("invalid source_url")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "sessionkeytest.globalpays.shop" {
		if parsed.Scheme != "https" {
			return fmt.Errorf("source_url for sessionkeytest.globalpays.shop must use https")
		}
		return nil
	}
	if h != nil && h.sessionImportAllowPrivateSources {
		return nil
	}
	if isPrivateOrLocalHost(host) {
		return fmt.Errorf("source_url host is not allowed")
	}
	return fmt.Errorf("source_url host is not allowed")
}

func (h *Handler) runClaudeSessionImportJob(ctx context.Context, job *claudeSessionImportJob, req normalizedClaudeSessionImportRequest) {
	keys, duplicates, err := h.sessionKeysForClaudeSessionImport(ctx, req)
	if err != nil {
		finished := time.Now().UTC()
		job.update(func(snapshot *claudeSessionImportJobSnapshot) {
			snapshot.Status = claudeSessionImportStatusFailed
			snapshot.Error = sanitizeClaudeSessionImportError(req, err)
			snapshot.FinishedAt = &finished
		})
		return
	}
	job.update(func(snapshot *claudeSessionImportJobSnapshot) {
		snapshot.TotalFetched = len(keys)
		snapshot.Duplicate = duplicates
	})

	keyCh := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < req.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range keyCh {
				if ctx.Err() != nil {
					return
				}
				sleepClaudeSessionImportDelay(ctx, req.DelayMin, req.DelayMax)
				result := h.processClaudeSessionImportKey(ctx, key, req)
				job.update(func(snapshot *claudeSessionImportJobSnapshot) {
					snapshot.TotalProcessed++
					if result.Status == "imported" {
						snapshot.Imported++
					} else {
						snapshot.Failed++
						if snapshot.FailureReasons == nil {
							snapshot.FailureReasons = map[string]int{}
						}
						reason := result.Reason
						if reason == "" {
							reason = "unknown"
						}
						snapshot.FailureReasons[reason]++
					}
					if len(snapshot.Results) < maxClaudeSessionImportResults {
						snapshot.Results = append(snapshot.Results, result)
					}
				})
			}
		}()
	}

sendLoop:
	for _, key := range keys {
		select {
		case <-ctx.Done():
			break sendLoop
		case keyCh <- key:
		}
	}
	close(keyCh)
	wg.Wait()

	finished := time.Now().UTC()
	job.update(func(snapshot *claudeSessionImportJobSnapshot) {
		if ctx.Err() != nil {
			snapshot.Status = claudeSessionImportStatusCanceled
		} else {
			snapshot.Status = claudeSessionImportStatusCompleted
		}
		snapshot.FinishedAt = &finished
	})
}

func (h *Handler) sessionKeysForClaudeSessionImport(ctx context.Context, req normalizedClaudeSessionImportRequest) ([]string, int, error) {
	if len(req.SessionKeys) > 0 {
		return append([]string(nil), req.SessionKeys...), req.SessionKeyDuplicates, nil
	}
	return h.fetchClaudeSessionKeys(ctx, req)
}

func (h *Handler) fetchClaudeSessionKeys(ctx context.Context, req normalizedClaudeSessionImportRequest) ([]string, int, error) {
	sourceProxyURL := strings.TrimSpace(req.ProxyURL)
	if sourceProxyURL == "" {
		sourceProxyURL = randomClaudeSessionImportProxy(req.ProxyCandidates)
	}

	client := &http.Client{
		Timeout: req.Timeout,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return h.validateClaudeSessionImportURL(next.URL)
		},
	}
	if sourceProxyURL != "" {
		transport, _, errTransport := proxyutil.BuildHTTPTransport(sourceProxyURL)
		if errTransport != nil {
			return nil, 0, fmt.Errorf("configure source fetch proxy: %w", errTransport)
		}
		if transport != nil {
			client.Transport = transport
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.APIEndpoint, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create source request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch source accounts: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, 0, fmt.Errorf("source accounts returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxClaudeSessionImportBodyBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read source accounts: %w", err)
	}
	if len(body) > maxClaudeSessionImportBodyBytes {
		return nil, 0, fmt.Errorf("source accounts response is too large")
	}

	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, 0, fmt.Errorf("parse source accounts JSON: %w", err)
	}
	seen := make(map[string]struct{}, len(rows))
	keys := make([]string, 0, len(rows))
	duplicates := 0
	for _, row := range rows {
		key := strings.TrimSpace(stringField(row, "session_key"))
		if key == "" {
			key = strings.TrimSpace(stringField(row, "sessionKey"))
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			duplicates++
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, duplicates, fmt.Errorf("source accounts did not contain session keys")
	}
	return keys, duplicates, nil
}

func normalizeClaudeSessionImportKeys(input []string) ([]string, int) {
	seen := make(map[string]struct{}, len(input))
	keys := make([]string, 0, len(input))
	duplicates := 0
	for _, raw := range input {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			duplicates++
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys, duplicates
}

func (h *Handler) processClaudeSessionImportKey(ctx context.Context, sessionKey string, req normalizedClaudeSessionImportRequest) claudeSessionImportResult {
	result := claudeSessionImportResult{
		SessionKeyHash: shortSessionKeyHash(sessionKey),
		Status:         "failed",
	}
	proxyURL := strings.TrimSpace(req.ProxyURL)
	if proxyURL == "" {
		proxyURL = randomClaudeSessionImportProxy(req.ProxyCandidates)
	}
	authResult, err := h.authenticateClaudeSessionKey(ctx, claudeSessionImportAuthRequest{
		SessionKey:   sessionKey,
		ProxyURL:     proxyURL,
		Prefix:       req.Prefix,
		Note:         req.Note,
		ImportSource: req.ImportSource,
	})
	if err != nil {
		result.Reason = classifyClaudeSessionImportError(err)
		return result
	}
	result.Status = "imported"
	result.AuthFile = authResult.AuthFile
	result.Email = authResult.Email
	result.AuthSource = authResult.AuthSource
	result.AuthMethodLabel = authResult.AuthMethodLabel
	return result
}

func (h *Handler) authenticateClaudeSessionKey(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
	if h != nil && h.sessionImportAuthenticate != nil {
		return h.sessionImportAuthenticate(ctx, req)
	}
	return h.saveClaudeSessionKeyAuth(ctx, req)
}

func (h *Handler) saveClaudeSessionKeyAuth(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
	sessionKey := strings.TrimSpace(req.SessionKey)
	if sessionKey == "" {
		return claudeSessionImportAuthResult{}, fmt.Errorf("sessionKey is required")
	}
	proxyURL := strings.TrimSpace(req.ProxyURL)
	authSvc := claude.NewClaudeAuthWithProxyURL(h.cfg, proxyURL)
	bundle, err := authSvc.CookieAuth(ctx, sessionKey)
	if err != nil {
		return claudeSessionImportAuthResult{}, err
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
	if importSource := normalizeClaudeImportSource(req.ImportSource); importSource == claudeImportSourceBulkSessionImport {
		metadata["import_source"] = importSource
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
		return claudeSessionImportAuthResult{}, fmt.Errorf("failed to save authentication tokens: %w", err)
	}
	if err := h.autoAddProxyURL(ctx, proxyURL); err != nil {
		log.WithError(err).Warn("failed to auto add session import proxy to proxy pool")
	}
	return claudeSessionImportAuthResult{
		AuthFile:        fileName,
		Path:            savedPath,
		Email:           tokenStorage.Email,
		AuthSource:      tokenStorage.AuthSource,
		AuthMethodLabel: claudeAuthMethodLabel(tokenStorage.AuthSource),
		TokenEndpoint:   tokenStorage.TokenEndpoint,
		RedirectURI:     tokenStorage.RedirectURI,
	}, nil
}

func sleepClaudeSessionImportDelay(ctx context.Context, minDelay, maxDelay time.Duration) {
	delay := randomClaudeSessionImportDelay(minDelay, maxDelay)
	if delay <= 0 {
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func randomClaudeSessionImportDelay(minDelay, maxDelay time.Duration) time.Duration {
	if maxDelay <= minDelay {
		return minDelay
	}
	span := uint64(maxDelay - minDelay)
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return minDelay
	}
	return minDelay + time.Duration(binary.BigEndian.Uint64(buf[:])%span)
}

func randomClaudeSessionImportProxy(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	idx := randomClaudeSessionImportInt(len(candidates))
	return strings.TrimSpace(candidates[idx])
}

func randomClaudeSessionImportInt(max int) int {
	if max <= 0 {
		return 0
	}
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return 0
	}
	return int(binary.BigEndian.Uint64(buf[:]) % uint64(max))
}

func (h *Handler) enabledClaudeSessionImportProxyURLs(ctx context.Context) ([]string, error) {
	if h == nil {
		return nil, nil
	}
	h.proxyPoolMu.Lock()
	defer h.proxyPoolMu.Unlock()

	proxies, err := h.loadProxyPool(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]string, 0, len(proxies))
	for _, entry := range proxies {
		if !entry.Enabled || !shouldStoreProxyURL(entry.URL) {
			continue
		}
		candidates = append(candidates, strings.TrimSpace(entry.URL))
	}
	return candidates, nil
}

func shouldHideClaudeSessionImportSource(host string) bool {
	return strings.EqualFold(strings.TrimSpace(host), "sessionkeytest.globalpays.shop")
}

func redactedClaudeSessionImportProxy(req normalizedClaudeSessionImportRequest) string {
	if strings.TrimSpace(req.ProxyURL) != "" {
		return proxyutil.Redact(req.ProxyURL)
	}
	if len(req.ProxyCandidates) > 0 {
		return fmt.Sprintf("代理池随机 · %d 个已启用代理", len(req.ProxyCandidates))
	}
	return ""
}

func sanitizeClaudeSessionImportError(req normalizedClaudeSessionImportRequest, err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if req.DisplaySourceURL != "" || req.DisplayAPIEndpoint != "" {
		return message
	}
	replacements := []string{
		strings.TrimSpace(req.APIEndpoint),
		strings.TrimSpace(req.SourceURL),
	}
	for _, replacement := range replacements {
		if replacement == "" {
			continue
		}
		message = strings.ReplaceAll(message, replacement, "默认抓取来源")
	}
	return message
}

func shortSessionKeyHash(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return hex.EncodeToString(sum[:])[:12]
}

func classifyClaudeSessionImportError(err error) string {
	if err == nil {
		return ""
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "status 401"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "invalid authentication"):
		return "unauthorized"
	case strings.Contains(lower, "status 403"), strings.Contains(lower, "forbidden"):
		return "forbidden"
	case strings.Contains(lower, "status 429"), strings.Contains(lower, "rate"):
		return "rate_limited"
	case strings.Contains(lower, "not json"), strings.Contains(lower, "bad json"), strings.Contains(lower, "parse"):
		return "bad_json"
	case strings.Contains(lower, "no organizations"), strings.Contains(lower, "organization"):
		return "organization_failed"
	case strings.Contains(lower, "authorization"):
		return "authorize_failed"
	case strings.Contains(lower, "exchange"), strings.Contains(lower, "token"):
		return "token_exchange_failed"
	default:
		return "error"
	}
}

func stringField(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if raw, ok := values[key].(string); ok {
		return raw
	}
	return ""
}

func isPrivateOrLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast()
	}
	return false
}
