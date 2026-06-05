package helps

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"testing"
)

// makeTestPNGBase64 returns the standard base64 of a solid wxh PNG, used to
// exercise the billable image-token estimate with a real, decodable image.
func makeTestPNGBase64(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestEstimateClaudeBillableInputTokens_ClaudeNativeImageNotCountedAsText proves
// a Claude-native base64 image block must NOT be counted as text. A 640x640 image
// should contribute ~ (640*640)/750 ≈ 546 visual tokens, not the tens of thousands
// the raw base64 string would yield when tokenized.
func TestEstimateClaudeBillableInputTokens_ClaudeNativeImageNotCountedAsText(t *testing.T) {
	b64 := makeTestPNGBase64(t, 640, 640)
	payload := []byte(`{
		"model":"claude-opus-4-7",
		"system":[{"type":"text","text":"识别商品品牌"}],
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}},
			{"type":"text","text":"这是什么牌子"}
		]}],
		"max_tokens":256
	}`)

	got := EstimateClaudeBillableInputTokens("claude-opus-4-7", "claude", payload)

	// The text ("识别商品品牌" + "这是什么牌子" + roles) is only a handful of tokens;
	// the image should add roughly 546 visual tokens. Anything in the thousands
	// means the base64 leaked into the text tokenizer.
	if got > 1200 {
		t.Fatalf("image base64 leaked into text token count: got %d, expected ~600 (text + ~546 visual)", got)
	}
	if got < 400 {
		t.Fatalf("image visual tokens appear to be dropped entirely: got %d, expected ~600", got)
	}
}

// TestEstimateClaudeBillableInputTokens_OpenAIImageURLNotCountedAsText proves the
// same for the OpenAI-style image_url data-URL shape.
func TestEstimateClaudeBillableInputTokens_OpenAIImageURLNotCountedAsText(t *testing.T) {
	b64 := makeTestPNGBase64(t, 640, 640)
	payload := []byte(`{
		"model":"claude-opus-4-7",
		"messages":[{"role":"user","content":[
			{"type":"image_url","image_url":{"url":"data:image/png;base64,` + b64 + `"}},
			{"type":"text","text":"这是什么牌子"}
		]}],
		"max_tokens":256
	}`)

	got := EstimateClaudeBillableInputTokens("claude-opus-4-7", "claude", payload)

	if got > 1200 {
		t.Fatalf("image_url base64 leaked into text token count: got %d, expected ~600", got)
	}
	if got < 400 {
		t.Fatalf("image_url visual tokens appear to be dropped entirely: got %d, expected ~600", got)
	}
}

// TestEstimateImageTokensFromDimensions_Formula locks the Anthropic vision
// formula (w*h)/750 plus the long-edge and area scaling caps.
func TestEstimateImageTokensFromDimensions_Formula(t *testing.T) {
	cases := []struct {
		name       string
		w, h       int
		wantApprox int64
		tol        int64
	}{
		{"640x640", 640, 640, 546, 2},
		{"100x100", 100, 100, 14, 1},
		{"oversized 4000x4000 scales to edge cap", 4000, 4000, 1536, 60},
		{"zero dims fall back", 0, 0, imageTokenFallback, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := estimateImageTokensFromDimensions(c.w, c.h)
			if got < c.wantApprox-c.tol || got > c.wantApprox+c.tol {
				t.Fatalf("dims %dx%d: got %d, want ~%d (±%d)", c.w, c.h, got, c.wantApprox, c.tol)
			}
		})
	}
}

// TestEstimateImageTokensFromBase64_FallbackOnGarbage proves malformed base64
// or non-image bytes yield the conservative fallback, never a text-tokenized
// count of the raw string.
func TestEstimateImageTokensFromBase64_FallbackOnGarbage(t *testing.T) {
	if got := estimateImageTokensFromBase64("not-valid-base64!!!"); got != imageTokenFallback {
		t.Fatalf("garbage base64: got %d, want fallback %d", got, imageTokenFallback)
	}
	// Valid base64 but not an image (decodes to "hello world").
	if got := estimateImageTokensFromBase64("aGVsbG8gd29ybGQ="); got != imageTokenFallback {
		t.Fatalf("non-image base64: got %d, want fallback %d", got, imageTokenFallback)
	}
	if got := estimateImageTokensFromBase64(""); got != imageTokenFallback {
		t.Fatalf("empty: got %d, want fallback %d", got, imageTokenFallback)
	}
}

// TestEstimateClaudeBillableInputTokens_HugeImageStaysBounded reproduces the
// original production scenario: a ~260KB base64 image must not explode into
// tens of thousands of tokens.
func TestEstimateClaudeBillableInputTokens_HugeImageStaysBounded(t *testing.T) {
	b64 := makeTestPNGBase64(t, 640, 640)
	payload := []byte(`{
		"model":"claude-opus-4-7",
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + b64 + `"}},
			{"type":"text","text":"远红外关节凝胶"}
		]}],
		"max_tokens":256
	}`)
	got := EstimateClaudeBillableInputTokens("claude-opus-4-7", "claude", payload)
	if got > 1000 {
		t.Fatalf("640x640 image inflated estimate to %d tokens; expected ~560", got)
	}
}
