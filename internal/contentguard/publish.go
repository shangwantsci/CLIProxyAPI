package contentguard

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	ExecutorType     = "content_guard"
	FailCodePrefix   = "content_policy_violation"
	publishMaxPerKey = 20
	publishWindow    = time.Minute
)

type publishFolder struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

var defaultPublishFolder = &publishFolder{hits: make(map[string][]time.Time)}

func publishHit(ctx context.Context, hit *Hit, enforce bool, model, requestedModel string) {
	if hit == nil {
		return
	}
	apiKey := apiKeyFromContext(ctx)
	if !defaultPublishFolder.allow(apiKey, time.Now()) {
		return
	}
	alias := strings.TrimSpace(requestedModel)
	if alias == "" {
		alias = strings.TrimSpace(model)
	}
	status := 0
	if enforce {
		status = http.StatusForbidden
	}
	coreusage.PublishRecord(ctx, coreusage.Record{
		ExecutorType: ExecutorType,
		Model:        strings.TrimSpace(model),
		Alias:        alias,
		APIKey:       apiKey,
		AuthID:       "",
		AuthIndex:    "",
		Source:       "",
		Failed:       enforce,
		Fail: coreusage.Failure{
			StatusCode: status,
			Body:       FailBody(hit),
		},
		RequestedAt: time.Now(),
	})
}

// FailBody is the usage fail_summary code: content_policy_violation:<category>:<strength>.
func FailBody(hit *Hit) string {
	if hit == nil {
		return FailCodePrefix
	}
	category := strings.TrimSpace(hit.Category)
	strength := strings.TrimSpace(hit.Strength)
	if category == "" {
		category = "unknown"
	}
	if strength == "" {
		strength = config.ContentGuardStrengthStandard
	}
	return fmt.Sprintf("%s:%s:%s", FailCodePrefix, category, strength)
}

func (f *publishFolder) allow(apiKey string, now time.Time) bool {
	if f == nil {
		return true
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		key = "anonymous"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hits == nil {
		f.hits = make(map[string][]time.Time)
	}
	cutoff := now.Add(-publishWindow)
	kept := f.hits[key][:0]
	for _, ts := range f.hits[key] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= publishMaxPerKey {
		f.hits[key] = kept
		return false
	}
	f.hits[key] = append(kept, now)
	return true
}

type apiKeyGetter interface {
	Get(string) (any, bool)
}

func apiKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value("gin")
	getter, ok := raw.(apiKeyGetter)
	if !ok || getter == nil {
		return ""
	}
	value, exists := getter.Get("userApiKey")
	if !exists || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
