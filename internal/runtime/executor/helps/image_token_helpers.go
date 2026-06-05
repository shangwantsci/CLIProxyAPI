package helps

import (
	"bytes"
	"encoding/base64"
	"image"
	"math"
	"strings"

	// Register decoders so image.DecodeConfig can read width/height from the
	// most common formats Anthropic accepts. WebP is intentionally omitted to
	// avoid pulling in golang.org/x/image; unknown formats use the fallback.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/tidwall/gjson"
)

const (
	// anthropicImageTokenDivisor matches Anthropic's documented vision cost:
	// tokens ≈ (width * height) / 750.
	anthropicImageTokenDivisor = 750.0
	// anthropicImageMaxEdge is the long-edge cap Anthropic applies before
	// counting; larger images are scaled down to fit.
	anthropicImageMaxEdge = 1568.0
	// anthropicImageMaxArea caps the megapixels counted after edge scaling.
	anthropicImageMaxArea = 1_150_000.0
	// imageTokenFallback is used when the image cannot be decoded (unknown
	// format, remote URL, malformed base64). It approximates a ~1000x1000
	// image so we neither wildly overcharge nor drop the cost to zero.
	imageTokenFallback = int64(1334)
)

// estimateImageTokensFromDimensions converts pixel dimensions into Anthropic
// vision tokens, applying the same long-edge and area scaling Anthropic does.
func estimateImageTokensFromDimensions(w, h int) int64 {
	if w <= 0 || h <= 0 {
		return imageTokenFallback
	}
	fw, fh := float64(w), float64(h)
	if fw > anthropicImageMaxEdge || fh > anthropicImageMaxEdge {
		scale := anthropicImageMaxEdge / math.Max(fw, fh)
		fw *= scale
		fh *= scale
	}
	if area := fw * fh; area > anthropicImageMaxArea {
		scale := math.Sqrt(anthropicImageMaxArea / area)
		fw *= scale
		fh *= scale
	}
	tokens := int64(math.Ceil(fw * fh / anthropicImageTokenDivisor))
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// estimateImageTokensFromBase64 decodes only the image header to read its
// dimensions and returns the Anthropic vision token estimate. The raw base64
// is never tokenized as text. Returns the fallback when the payload is empty
// or cannot be decoded as a known image format.
func estimateImageTokensFromBase64(data string) int64 {
	data = strings.TrimSpace(data)
	if data == "" {
		return imageTokenFallback
	}
	// Strip a data: URL prefix (data:image/png;base64,XXXX) if present.
	if strings.HasPrefix(data, "data:") {
		if idx := strings.IndexByte(data, ','); idx >= 0 {
			data = data[idx+1:]
		}
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		// Tolerate raw-std (unpadded) and URL-safe variants before giving up.
		if raw, err = base64.RawStdEncoding.DecodeString(data); err != nil {
			if raw, err = base64.URLEncoding.DecodeString(data); err != nil {
				return imageTokenFallback
			}
		}
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return imageTokenFallback
	}
	return estimateImageTokensFromDimensions(cfg.Width, cfg.Height)
}

// addClaudeImageTokens accounts for a Claude-native image content block
// ({"type":"image","source":{"type":"base64","data":...}}) without letting the
// base64 string leak into the text tokenizer.
func addClaudeImageTokens(source gjson.Result, imageTokens *int64) {
	if imageTokens == nil {
		return
	}
	if source.Get("type").String() == "url" {
		// Remote image: not downloaded on this path, so use the fallback.
		*imageTokens += imageTokenFallback
		return
	}
	*imageTokens += estimateImageTokensFromBase64(source.Get("data").String())
}

// addImageURLTokens accounts for an OpenAI-style image_url value, which may be a
// data: URL (decodable) or a remote URL (fallback).
func addImageURLTokens(url string, imageTokens *int64) {
	if imageTokens == nil {
		return
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	if strings.HasPrefix(url, "data:") {
		*imageTokens += estimateImageTokensFromBase64(url)
		return
	}
	// Remote http(s) URL: not downloaded here, use the fallback.
	*imageTokens += imageTokenFallback
}
