package managementasset

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLatestAssetUsesPanelGitHubToken(t *testing.T) {
	t.Setenv("MANAGEMENT_PANEL_GITHUB_TOKEN", "panel-token")

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assets":[{"name":"management.html","browser_download_url":"https://example.test/management.html","digest":"sha256:abc123"}]}`))
	}))
	defer server.Close()

	asset, _, err := fetchLatestAsset(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchLatestAsset returned error: %v", err)
	}
	if asset == nil || asset.Name != managementAssetName {
		t.Fatalf("asset = %#v, want management.html", asset)
	}
	if gotAuth != "Bearer panel-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer panel-token")
	}
}

func TestDownloadAssetUsesPanelGitHubToken(t *testing.T) {
	t.Setenv("MANAGEMENT_PANEL_GITHUB_TOKEN", "panel-token")

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html>"))
	}))
	defer server.Close()

	data, _, err := downloadAsset(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("downloadAsset returned error: %v", err)
	}
	if string(data) != "<!doctype html>" {
		t.Fatalf("data = %q", string(data))
	}
	if gotAuth != "Bearer panel-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer panel-token")
	}
}
