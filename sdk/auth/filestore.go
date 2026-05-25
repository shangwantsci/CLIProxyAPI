package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// FileTokenStore persists token records and auth metadata using the filesystem as backing storage.
type FileTokenStore struct {
	mu      sync.Mutex
	dirLock sync.RWMutex
	baseDir string
}

// NewFileTokenStore creates a token store that saves credentials to disk through the
// TokenStorage implementation embedded in the token record.
func NewFileTokenStore() *FileTokenStore {
	return &FileTokenStore{}
}

// SetBaseDir updates the default directory used for auth JSON persistence when no explicit path is provided.
func (s *FileTokenStore) SetBaseDir(dir string) {
	s.dirLock.Lock()
	s.baseDir = strings.TrimSpace(dir)
	s.dirLock.Unlock()
}

// Save persists token storage and metadata to the resolved auth file path.
func (s *FileTokenStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}

	path, err := s.resolveAuthPath(auth)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("auth filestore: missing file path attribute for %s", auth.ID)
	}
	s.attachResolvedPath(auth, path)

	if auth.Disabled {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return "", nil
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("auth filestore: create dir failed: %w", err)
	}

	// metadataSetter is a private interface for TokenStorage implementations that support metadata injection.
	type metadataSetter interface {
		SetMetadata(map[string]any)
	}

	switch {
	case auth.Storage != nil:
		syncRuntimeStateMetadata(auth)
		if setter, ok := auth.Storage.(metadataSetter); ok {
			setter.SetMetadata(auth.Metadata)
		}
		if err = auth.Storage.SaveTokenToFile(path); err != nil {
			return "", err
		}
	case auth.Metadata != nil:
		syncRuntimeStateMetadata(auth)
		raw, errMarshal := json.Marshal(auth.Metadata)
		if errMarshal != nil {
			return "", fmt.Errorf("auth filestore: marshal metadata failed: %w", errMarshal)
		}
		if existing, errRead := os.ReadFile(path); errRead == nil {
			if jsonEqual(existing, raw) {
				return path, nil
			}
			file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
			if errOpen != nil {
				return "", fmt.Errorf("auth filestore: open existing failed: %w", errOpen)
			}
			if _, errWrite := file.Write(raw); errWrite != nil {
				_ = file.Close()
				return "", fmt.Errorf("auth filestore: write existing failed: %w", errWrite)
			}
			if errClose := file.Close(); errClose != nil {
				return "", fmt.Errorf("auth filestore: close existing failed: %w", errClose)
			}
			return path, nil
		} else if !os.IsNotExist(errRead) {
			return "", fmt.Errorf("auth filestore: read existing failed: %w", errRead)
		}
		if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
			return "", fmt.Errorf("auth filestore: write file failed: %w", errWrite)
		}
	default:
		return "", fmt.Errorf("auth filestore: nothing to persist for %s", auth.ID)
	}

	return path, nil
}

func syncRuntimeStateMetadata(auth *cliproxyauth.Auth) {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["disabled"] = auth.Disabled
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return
	}
	if auth.Status != "" && auth.Status != cliproxyauth.StatusActive {
		auth.Metadata["status"] = string(auth.Status)
	} else {
		delete(auth.Metadata, "status")
	}
	if strings.TrimSpace(auth.StatusMessage) != "" {
		auth.Metadata["status_message"] = strings.TrimSpace(auth.StatusMessage)
	} else {
		delete(auth.Metadata, "status_message")
	}
	if auth.Unavailable {
		auth.Metadata["unavailable"] = true
	} else {
		delete(auth.Metadata, "unavailable")
	}
	if !auth.NextRetryAfter.IsZero() {
		auth.Metadata["next_retry_after"] = auth.NextRetryAfter.Format(time.RFC3339Nano)
	} else {
		delete(auth.Metadata, "next_retry_after")
	}
	if !auth.NextRefreshAfter.IsZero() {
		auth.Metadata["next_refresh_after"] = auth.NextRefreshAfter.Format(time.RFC3339Nano)
	} else {
		delete(auth.Metadata, "next_refresh_after")
	}
	if auth.Quota.Exceeded || auth.Quota.Reason != "" || !auth.Quota.NextRecoverAt.IsZero() || auth.Quota.BackoffLevel != 0 {
		quota := map[string]any{
			"exceeded": auth.Quota.Exceeded,
		}
		if strings.TrimSpace(auth.Quota.Reason) != "" {
			quota["reason"] = strings.TrimSpace(auth.Quota.Reason)
		}
		if !auth.Quota.NextRecoverAt.IsZero() {
			quota["next_recover_at"] = auth.Quota.NextRecoverAt.Format(time.RFC3339Nano)
		}
		if auth.Quota.BackoffLevel != 0 {
			quota["backoff_level"] = auth.Quota.BackoffLevel
		}
		auth.Metadata["quota"] = quota
	} else {
		delete(auth.Metadata, "quota")
	}
	if auth.LastError != nil {
		lastError := map[string]any{
			"message":   strings.TrimSpace(auth.LastError.Message),
			"retryable": auth.LastError.Retryable,
		}
		if strings.TrimSpace(auth.LastError.Code) != "" {
			lastError["code"] = strings.TrimSpace(auth.LastError.Code)
		}
		if auth.LastError.HTTPStatus != 0 {
			lastError["http_status"] = auth.LastError.HTTPStatus
		}
		auth.Metadata["last_error"] = lastError
	} else {
		delete(auth.Metadata, "last_error")
	}
}

func (s *FileTokenStore) attachResolvedPath(auth *cliproxyauth.Auth, path string) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["path"] = path

	if strings.TrimSpace(auth.FileName) == "" {
		auth.FileName = auth.ID
	}
}

// List enumerates all auth JSON files under the configured directory.
func (s *FileTokenStore) List(ctx context.Context) ([]*cliproxyauth.Auth, error) {
	dir := s.baseDirSnapshot()
	if dir == "" {
		return nil, fmt.Errorf("auth filestore: directory not configured")
	}
	entries := make([]*cliproxyauth.Auth, 0)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		auth, err := s.readAuthFile(path, dir)
		if err != nil {
			return nil
		}
		if auth != nil {
			entries = append(entries, auth)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// Delete removes the auth file.
func (s *FileTokenStore) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("auth filestore: id is empty")
	}
	path, err := s.resolveDeletePath(id)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("auth filestore: delete failed: %w", err)
	}
	return nil
}

func (s *FileTokenStore) resolveDeletePath(id string) (string, error) {
	if strings.ContainsRune(id, os.PathSeparator) || filepath.IsAbs(id) {
		return id, nil
	}
	dir := s.baseDirSnapshot()
	if dir == "" {
		return "", fmt.Errorf("auth filestore: directory not configured")
	}
	return filepath.Join(dir, id), nil
}

func (s *FileTokenStore) readAuthFile(path, baseDir string) (*cliproxyauth.Auth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	metadata := make(map[string]any)
	if err = json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("unmarshal auth json: %w", err)
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider = "unknown"
	}
	if provider == "antigravity" || provider == "gemini" {
		projectID := ""
		if pid, ok := metadata["project_id"].(string); ok {
			projectID = strings.TrimSpace(pid)
		}
		if projectID == "" {
			accessToken := extractAccessToken(metadata)
			// For gemini type, the stored access_token is likely expired (~1h lifetime).
			// Refresh it using the long-lived refresh_token before querying.
			if provider == "gemini" {
				if tokenMap, ok := metadata["token"].(map[string]any); ok {
					if refreshed, errRefresh := refreshGeminiAccessToken(tokenMap, http.DefaultClient); errRefresh == nil {
						accessToken = refreshed
					}
				}
			}
			if accessToken != "" {
				fetchedProjectID, errFetch := FetchAntigravityProjectID(context.Background(), accessToken, http.DefaultClient)
				if errFetch == nil && strings.TrimSpace(fetchedProjectID) != "" {
					metadata["project_id"] = strings.TrimSpace(fetchedProjectID)
					if raw, errMarshal := json.Marshal(metadata); errMarshal == nil {
						if file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600); errOpen == nil {
							_, _ = file.Write(raw)
							_ = file.Close()
						}
					}
				}
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	id := s.idFor(path, baseDir)
	disabled, _ := metadata["disabled"].(bool)
	status := cliproxyauth.StatusActive
	statusMessage := ""
	unavailable := false
	var lastError *cliproxyauth.Error
	nextRetryAfter := time.Time{}
	nextRefreshAfter := time.Time{}
	quota := cliproxyauth.QuotaState{}
	if strings.EqualFold(provider, "claude") {
		status = statusFromMetadata(metadata, disabled)
		statusMessage, _ = metadata["status_message"].(string)
		unavailable, _ = metadata["unavailable"].(bool)
		lastError = authErrorFromMetadata(metadata["last_error"])
		nextRetryAfter, _ = metadataTimeValue(metadata["next_retry_after"])
		nextRefreshAfter, _ = metadataTimeValue(metadata["next_refresh_after"])
		quota = quotaStateFromMetadata(metadata["quota"])
	}
	if disabled {
		status = cliproxyauth.StatusDisabled
	}
	auth := &cliproxyauth.Auth{
		ID:               id,
		Provider:         provider,
		FileName:         id,
		Label:            s.labelFor(metadata),
		Status:           status,
		StatusMessage:    strings.TrimSpace(statusMessage),
		Disabled:         disabled,
		Unavailable:      unavailable,
		Attributes:       map[string]string{"path": path},
		Metadata:         metadata,
		Quota:            quota,
		LastError:        lastError,
		CreatedAt:        info.ModTime(),
		UpdatedAt:        info.ModTime(),
		LastRefreshedAt:  time.Time{},
		NextRefreshAfter: nextRefreshAfter,
		NextRetryAfter:   nextRetryAfter,
	}
	if email, ok := metadata["email"].(string); ok && email != "" {
		auth.Attributes["email"] = email
	}
	if proxyURL, ok := metadata["proxy_url"].(string); ok {
		auth.ProxyURL = strings.TrimSpace(proxyURL)
	}
	if prefix, ok := metadata["prefix"].(string); ok {
		auth.Prefix = strings.Trim(strings.TrimSpace(prefix), "/")
		if auth.Prefix != "" {
			auth.Attributes["prefix"] = auth.Prefix
		}
	}
	if rawPriority, ok := metadata["priority"]; ok {
		switch v := rawPriority.(type) {
		case float64:
			auth.Attributes["priority"] = strconv.Itoa(int(v))
		case string:
			priority := strings.TrimSpace(v)
			if _, errAtoi := strconv.Atoi(priority); errAtoi == nil {
				auth.Attributes["priority"] = priority
			}
		}
	}
	if rawNote, ok := metadata["note"].(string); ok {
		if note := strings.TrimSpace(rawNote); note != "" {
			auth.Attributes["note"] = note
		}
	}
	applyRuntimeLimitMetadata(auth, metadata)
	applyClaudeCloakMetadata(auth, metadata)
	cliproxyauth.ApplyCustomHeadersFromMetadata(auth)
	return auth, nil
}

func statusFromMetadata(metadata map[string]any, disabled bool) cliproxyauth.Status {
	if disabled {
		return cliproxyauth.StatusDisabled
	}
	raw, _ := metadata["status"].(string)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(cliproxyauth.StatusUnknown):
		return cliproxyauth.StatusUnknown
	case string(cliproxyauth.StatusPending):
		return cliproxyauth.StatusPending
	case string(cliproxyauth.StatusRefreshing):
		return cliproxyauth.StatusRefreshing
	case string(cliproxyauth.StatusError):
		return cliproxyauth.StatusError
	case string(cliproxyauth.StatusDisabled):
		return cliproxyauth.StatusDisabled
	default:
		return cliproxyauth.StatusActive
	}
}

func authErrorFromMetadata(raw any) *cliproxyauth.Error {
	values, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	err := &cliproxyauth.Error{}
	if code, ok := values["code"].(string); ok {
		err.Code = strings.TrimSpace(code)
	}
	if message, ok := values["message"].(string); ok {
		err.Message = strings.TrimSpace(message)
	}
	if retryable, ok := values["retryable"].(bool); ok {
		err.Retryable = retryable
	}
	switch status := values["http_status"].(type) {
	case float64:
		err.HTTPStatus = int(status)
	case int:
		err.HTTPStatus = status
	case string:
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(status)); parseErr == nil {
			err.HTTPStatus = parsed
		}
	}
	if err.Code == "" && err.Message == "" && err.HTTPStatus == 0 {
		return nil
	}
	return err
}

func quotaStateFromMetadata(raw any) cliproxyauth.QuotaState {
	values, ok := raw.(map[string]any)
	if !ok {
		return cliproxyauth.QuotaState{}
	}
	quota := cliproxyauth.QuotaState{}
	if exceeded, ok := values["exceeded"].(bool); ok {
		quota.Exceeded = exceeded
	}
	if reason, ok := values["reason"].(string); ok {
		quota.Reason = strings.TrimSpace(reason)
	}
	if recoverAt, ok := metadataTimeValue(values["next_recover_at"]); ok {
		quota.NextRecoverAt = recoverAt
	}
	if level, ok := metadataIntValue(values["backoff_level"]); ok {
		quota.BackoffLevel = level
	}
	return quota
}

func metadataTimeValue(raw any) (time.Time, bool) {
	switch value := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return parsed, true
			}
		}
		if unix, err := strconv.ParseInt(trimmed, 10, 64); err == nil && unix > 0 {
			return normalizeMetadataUnixTime(unix), true
		}
	case float64:
		if value > 0 {
			return normalizeMetadataUnixTime(int64(value)), true
		}
	case int64:
		if value > 0 {
			return normalizeMetadataUnixTime(value), true
		}
	case int:
		if value > 0 {
			return normalizeMetadataUnixTime(int64(value)), true
		}
	case json.Number:
		if unix, err := value.Int64(); err == nil && unix > 0 {
			return normalizeMetadataUnixTime(unix), true
		}
	}
	return time.Time{}, false
}

func normalizeMetadataUnixTime(raw int64) time.Time {
	if raw > 1_000_000_000_000 {
		return time.UnixMilli(raw)
	}
	return time.Unix(raw, 0)
}

func metadataIntValue(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err == nil
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func applyClaudeCloakMetadata(auth *cliproxyauth.Auth, metadata map[string]any) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if rawMode, ok := metadata["cloak_mode"].(string); ok {
		mode := strings.ToLower(strings.TrimSpace(rawMode))
		if mode == "auto" || mode == "always" || mode == "never" {
			auth.Attributes["cloak_mode"] = mode
		}
	}
	if rawStrict, ok := metadata["cloak_strict_mode"].(bool); ok {
		auth.Attributes["cloak_strict_mode"] = strconv.FormatBool(rawStrict)
	} else if rawStrict, ok := metadata["cloak_strict_mode"].(string); ok && strings.TrimSpace(rawStrict) != "" {
		auth.Attributes["cloak_strict_mode"] = strconv.FormatBool(strings.EqualFold(strings.TrimSpace(rawStrict), "true") || strings.TrimSpace(rawStrict) == "1")
	}
	if rawCache, ok := metadata["cloak_cache_user_id"].(bool); ok {
		auth.Attributes["cloak_cache_user_id"] = strconv.FormatBool(rawCache)
	} else if rawCache, ok := metadata["cloak_cache_user_id"].(string); ok && strings.TrimSpace(rawCache) != "" {
		auth.Attributes["cloak_cache_user_id"] = strconv.FormatBool(strings.EqualFold(strings.TrimSpace(rawCache), "true") || strings.TrimSpace(rawCache) == "1")
	}
	words := cloakWordsFromMetadata(metadata["cloak_sensitive_words"])
	if len(words) > 0 {
		auth.Attributes["cloak_sensitive_words"] = strings.Join(words, ",")
	}
}

func applyRuntimeLimitMetadata(auth *cliproxyauth.Auth, metadata map[string]any) {
	if auth == nil || metadata == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	for _, key := range []string{"rpm_limit", "max_sessions"} {
		if raw, ok := metadata[key]; ok {
			if value, ok := metadataIntValue(raw); ok && value >= 0 {
				auth.Attributes[key] = strconv.Itoa(value)
			}
		}
	}
}

func cloakWordsFromMetadata(raw any) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	add := func(raw string) {
		for _, part := range strings.Split(raw, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, trimmed)
		}
	}
	switch v := raw.(type) {
	case string:
		add(v)
	case []string:
		for _, item := range v {
			add(item)
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	return out
}

func (s *FileTokenStore) idFor(path, baseDir string) string {
	id := path
	if baseDir != "" {
		if rel, errRel := filepath.Rel(baseDir, path); errRel == nil && rel != "" {
			id = rel
		}
	}
	// On Windows, normalize ID casing to avoid duplicate auth entries caused by case-insensitive paths.
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func (s *FileTokenStore) resolveAuthPath(auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			return p, nil
		}
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		if filepath.IsAbs(fileName) {
			return fileName, nil
		}
		if dir := s.baseDirSnapshot(); dir != "" {
			return filepath.Join(dir, fileName), nil
		}
		return fileName, nil
	}
	if auth.ID == "" {
		return "", fmt.Errorf("auth filestore: missing id")
	}
	if filepath.IsAbs(auth.ID) {
		return auth.ID, nil
	}
	dir := s.baseDirSnapshot()
	if dir == "" {
		return "", fmt.Errorf("auth filestore: directory not configured")
	}
	return filepath.Join(dir, auth.ID), nil
}

func (s *FileTokenStore) labelFor(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata["label"].(string); ok && v != "" {
		return v
	}
	if v, ok := metadata["email"].(string); ok && v != "" {
		return v
	}
	if project, ok := metadata["project_id"].(string); ok && project != "" {
		return project
	}
	return ""
}

func (s *FileTokenStore) baseDirSnapshot() string {
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	return s.baseDir
}

func extractAccessToken(metadata map[string]any) string {
	if at, ok := metadata["access_token"].(string); ok {
		if v := strings.TrimSpace(at); v != "" {
			return v
		}
	}
	if tokenMap, ok := metadata["token"].(map[string]any); ok {
		if at, ok := tokenMap["access_token"].(string); ok {
			if v := strings.TrimSpace(at); v != "" {
				return v
			}
		}
	}
	return ""
}

func refreshGeminiAccessToken(tokenMap map[string]any, httpClient *http.Client) (string, error) {
	refreshToken, _ := tokenMap["refresh_token"].(string)
	clientID, _ := tokenMap["client_id"].(string)
	clientSecret, _ := tokenMap["client_secret"].(string)
	tokenURI, _ := tokenMap["token_uri"].(string)

	if refreshToken == "" || clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("missing refresh credentials")
	}
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}

	resp, err := httpClient.PostForm(tokenURI, data)
	if err != nil {
		return "", fmt.Errorf("refresh request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refresh failed: status %d", resp.StatusCode)
	}

	var result map[string]any
	if errUnmarshal := json.Unmarshal(body, &result); errUnmarshal != nil {
		return "", fmt.Errorf("decode refresh response: %w", errUnmarshal)
	}

	newAccessToken, _ := result["access_token"].(string)
	if newAccessToken == "" {
		return "", fmt.Errorf("no access_token in refresh response")
	}

	tokenMap["access_token"] = newAccessToken
	return newAccessToken, nil
}

// jsonEqual compares two JSON blobs by parsing them into Go objects and deep comparing.
func jsonEqual(a, b []byte) bool {
	var objA any
	var objB any
	if err := json.Unmarshal(a, &objA); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &objB); err != nil {
		return false
	}
	return deepEqualJSON(objA, objB)
}

func deepEqualJSON(a, b any) bool {
	switch valA := a.(type) {
	case map[string]any:
		valB, ok := b.(map[string]any)
		if !ok || len(valA) != len(valB) {
			return false
		}
		for key, subA := range valA {
			subB, ok1 := valB[key]
			if !ok1 || !deepEqualJSON(subA, subB) {
				return false
			}
		}
		return true
	case []any:
		sliceB, ok := b.([]any)
		if !ok || len(valA) != len(sliceB) {
			return false
		}
		for i := range valA {
			if !deepEqualJSON(valA[i], sliceB[i]) {
				return false
			}
		}
		return true
	case float64:
		valB, ok := b.(float64)
		if !ok {
			return false
		}
		return valA == valB
	case string:
		valB, ok := b.(string)
		if !ok {
			return false
		}
		return valA == valB
	case bool:
		valB, ok := b.(bool)
		if !ok {
			return false
		}
		return valA == valB
	case nil:
		return b == nil
	default:
		return false
	}
}
