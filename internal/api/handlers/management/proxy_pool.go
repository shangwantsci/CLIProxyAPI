package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const proxyPoolFileName = "proxy_pool.json"

type proxyPoolDocument struct {
	Version int              `json:"version"`
	Proxies []proxyPoolEntry `json:"proxies"`
}

type proxyPoolEntry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	URL         string    `json:"url"`
	RedactedURL string    `json:"redacted_url,omitempty"`
	Scheme      string    `json:"scheme,omitempty"`
	Host        string    `json:"host,omitempty"`
	Enabled     bool      `json:"enabled"`
	Note        string    `json:"note,omitempty"`
	UsageCount  int       `json:"usage_count,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type proxyPoolUpsertRequest struct {
	Name    *string `json:"name"`
	URL     *string `json:"url"`
	Enabled *bool   `json:"enabled"`
	Note    *string `json:"note"`
}

func (h *Handler) ListProxyPool(c *gin.Context) {
	h.proxyPoolMu.Lock()
	defer h.proxyPoolMu.Unlock()

	proxies, err := h.loadProxyPool(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"proxies": h.decorateProxyPoolEntries(proxies)})
}

func (h *Handler) CreateProxyPoolEntry(c *gin.Context) {
	var req proxyPoolUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.URL == nil || strings.TrimSpace(*req.URL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}

	h.proxyPoolMu.Lock()
	defer h.proxyPoolMu.Unlock()

	entry, err := h.upsertProxyPoolEntryLocked(c.Request.Context(), *req.URL, stringPtrValue(req.Name), stringPtrValue(req.Note), req.Enabled)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"proxy": h.decorateProxyPoolEntry(entry)})
}

func (h *Handler) PatchProxyPoolEntry(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	var req proxyPoolUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	h.proxyPoolMu.Lock()
	defer h.proxyPoolMu.Unlock()

	proxies, err := h.loadProxyPool(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	idx := -1
	for i := range proxies {
		if proxies[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "proxy not found"})
		return
	}

	entry := proxies[idx]
	if req.URL != nil {
		nextURL := strings.TrimSpace(*req.URL)
		nextEntry, errBuild := buildProxyPoolEntry(nextURL, entry.Name, entry.Note, entry.Enabled, entry.CreatedAt, time.Now())
		if errBuild != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errBuild.Error()})
			return
		}
		if nextEntry.ID != entry.ID {
			for i := range proxies {
				if i != idx && proxies[i].ID == nextEntry.ID {
					c.JSON(http.StatusConflict, gin.H{"error": "proxy already exists"})
					return
				}
			}
		}
		entry.ID = nextEntry.ID
		entry.URL = nextEntry.URL
		entry.Scheme = nextEntry.Scheme
		entry.Host = nextEntry.Host
	}
	if req.Name != nil {
		entry.Name = strings.TrimSpace(*req.Name)
	}
	if req.Note != nil {
		entry.Note = strings.TrimSpace(*req.Note)
	}
	if req.Enabled != nil {
		entry.Enabled = *req.Enabled
	}
	entry.UpdatedAt = time.Now()
	proxies[idx] = entry

	if err := h.saveProxyPool(c.Request.Context(), proxies); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"proxy": h.decorateProxyPoolEntry(entry)})
}

func (h *Handler) DeleteProxyPoolEntry(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	h.proxyPoolMu.Lock()
	defer h.proxyPoolMu.Unlock()

	proxies, err := h.loadProxyPool(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	next := proxies[:0]
	found := false
	for _, entry := range proxies {
		if entry.ID == id {
			found = true
			continue
		}
		next = append(next, entry)
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "proxy not found"})
		return
	}

	if err := h.saveProxyPool(c.Request.Context(), next); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) autoAddProxyURL(ctx context.Context, raw string) error {
	if !shouldStoreProxyURL(raw) {
		return nil
	}

	h.proxyPoolMu.Lock()
	defer h.proxyPoolMu.Unlock()

	_, err := h.upsertProxyPoolEntryLocked(ctx, raw, "", "", nil)
	if err != nil {
		return err
	}
	return nil
}

func (h *Handler) upsertProxyPoolEntryLocked(ctx context.Context, raw, name, note string, enabled *bool) (proxyPoolEntry, error) {
	now := time.Now()
	nextEntry, err := buildProxyPoolEntry(raw, name, note, true, now, now)
	if err != nil {
		return proxyPoolEntry{}, err
	}
	if enabled != nil {
		nextEntry.Enabled = *enabled
	}

	proxies, err := h.loadProxyPool(ctx)
	if err != nil {
		return proxyPoolEntry{}, err
	}
	for i := range proxies {
		if proxies[i].ID != nextEntry.ID {
			continue
		}
		if strings.TrimSpace(name) != "" {
			proxies[i].Name = strings.TrimSpace(name)
		}
		if strings.TrimSpace(note) != "" {
			proxies[i].Note = strings.TrimSpace(note)
		}
		if enabled != nil {
			proxies[i].Enabled = *enabled
		}
		proxies[i].UpdatedAt = now
		if err := h.saveProxyPool(ctx, proxies); err != nil {
			return proxyPoolEntry{}, err
		}
		return proxies[i], nil
	}

	proxies = append(proxies, nextEntry)
	if err := h.saveProxyPool(ctx, proxies); err != nil {
		return proxyPoolEntry{}, err
	}
	return nextEntry, nil
}

func (h *Handler) loadProxyPool(ctx context.Context) ([]proxyPoolEntry, error) {
	_ = ctx
	path := h.proxyPoolPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read proxy pool: %w", err)
	}

	var doc proxyPoolDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse proxy pool: %w", err)
	}

	proxies := make([]proxyPoolEntry, 0, len(doc.Proxies))
	for _, entry := range doc.Proxies {
		entry.URL = strings.TrimSpace(entry.URL)
		if entry.URL == "" {
			continue
		}
		normalized, err := buildProxyPoolEntry(entry.URL, entry.Name, entry.Note, entry.Enabled, entry.CreatedAt, entry.UpdatedAt)
		if err != nil {
			log.WithError(err).Warnf("skip invalid proxy pool entry %s", proxyutil.Redact(entry.URL))
			continue
		}
		proxies = append(proxies, normalized)
	}

	sort.SliceStable(proxies, func(i, j int) bool {
		return proxies[i].CreatedAt.Before(proxies[j].CreatedAt)
	})
	return proxies, nil
}

func (h *Handler) saveProxyPool(ctx context.Context, proxies []proxyPoolEntry) error {
	_ = ctx
	path := h.proxyPoolPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create proxy pool directory: %w", err)
	}

	sort.SliceStable(proxies, func(i, j int) bool {
		return proxies[i].CreatedAt.Before(proxies[j].CreatedAt)
	})

	doc := proxyPoolDocument{Version: 1, Proxies: proxies}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode proxy pool: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("failed to write proxy pool: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed to replace proxy pool: %w", err)
	}
	return nil
}

func (h *Handler) proxyPoolPath() string {
	authDir := "."
	if h != nil && h.cfg != nil && strings.TrimSpace(h.cfg.AuthDir) != "" {
		authDir = strings.TrimSpace(h.cfg.AuthDir)
	}
	return filepath.Join(authDir, proxyPoolFileName)
}

func (h *Handler) decorateProxyPoolEntries(proxies []proxyPoolEntry) []proxyPoolEntry {
	out := make([]proxyPoolEntry, 0, len(proxies))
	for _, entry := range proxies {
		out = append(out, h.decorateProxyPoolEntry(entry))
	}
	return out
}

func (h *Handler) decorateProxyPoolEntry(entry proxyPoolEntry) proxyPoolEntry {
	entry.RedactedURL = proxyutil.Redact(entry.URL)
	if parsed, err := url.Parse(entry.URL); err == nil {
		entry.Scheme = parsed.Scheme
		entry.Host = parsed.Host
	}
	entry.UsageCount = h.proxyPoolUsageCount(entry.URL)
	return entry
}

func (h *Handler) proxyPoolUsageCount(raw string) int {
	if h == nil || h.authManager == nil {
		return 0
	}
	target := strings.TrimSpace(raw)
	if target == "" {
		return 0
	}
	count := 0
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.ProxyURL) == target {
			count++
		}
	}
	return count
}

func buildProxyPoolEntry(raw, name, note string, enabled bool, createdAt, updatedAt time.Time) (proxyPoolEntry, error) {
	proxyURL := strings.TrimSpace(raw)
	setting, err := proxyutil.Parse(proxyURL)
	if err != nil {
		return proxyPoolEntry{}, err
	}
	if setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
		return proxyPoolEntry{}, fmt.Errorf("proxy pool only accepts http://, https://, socks5://, or socks5h:// URLs")
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return proxyPoolEntry{
		ID:        proxyPoolEntryID(proxyURL),
		Name:      strings.TrimSpace(name),
		URL:       proxyURL,
		Scheme:    setting.URL.Scheme,
		Host:      setting.URL.Host,
		Enabled:   enabled,
		Note:      strings.TrimSpace(note),
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func proxyPoolEntryID(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])[:16]
}

func shouldStoreProxyURL(raw string) bool {
	setting, err := proxyutil.Parse(raw)
	return err == nil && setting.Mode == proxyutil.ModeProxy
}

func stringPtrValue(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return strings.TrimSpace(*ptr)
}
