package contentguard

import "testing"

func TestExtractLatestUserTextProtocols(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		format string
		body   string
		want   string
	}{
		{
			name:   "openai last user",
			format: "openai",
			body:   `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"latest turn"}]}`,
			want:   "latest turn",
		},
		{
			name:   "openai completions prompt",
			format: "openai",
			body:   `{"prompt":"complete this"}`,
			want:   "complete this",
		},
		{
			name:   "openai content parts",
			format: "openai",
			body:   `{"messages":[{"role":"user","content":[{"type":"text","text":"part one"},{"type":"text","text":"part two"}]}]}`,
			want:   "part one\npart two",
		},
		{
			name:   "responses string input",
			format: "openai-response",
			body:   `{"input":"responses latest"}`,
			want:   "responses latest",
		},
		{
			name:   "responses array latest user",
			format: "openai-response",
			body:   `{"input":[{"role":"user","content":"old"},{"role":"assistant","content":"ack"},{"type":"message","role":"user","content":[{"type":"input_text","text":"new turn"}]}]}`,
			want:   "new turn",
		},
		{
			name:   "claude last user",
			format: "claude",
			body:   `{"messages":[{"role":"user","content":"old"},{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"text","text":"claude latest"}]}]}`,
			want:   "claude latest",
		},
		{
			name:   "gemini last user parts",
			format: "gemini",
			body:   `{"contents":[{"role":"user","parts":[{"text":"old"}]},{"role":"model","parts":[{"text":"ok"}]},{"role":"user","parts":[{"text":"gemini latest"}]}]}`,
			want:   "gemini latest",
		},
		{
			name:   "image prompt",
			format: "openai-image",
			body:   `{"prompt":"draw a lake"}`,
			want:   "draw a lake",
		},
		{
			name:   "alpha search query",
			format: SourceFormatAlphaSearch,
			body:   `{"query":"search latest"}`,
			want:   "search latest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractLatestUserText(tt.format, []byte(tt.body))
			if got != tt.want {
				t.Fatalf("ExtractLatestUserText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractIgnoresHistoricalHitAndCapsScan(t *testing.T) {
	t.Parallel()
	body := `{"messages":[{"role":"user","content":"how to kill myself"},{"role":"assistant","content":"please seek help"},{"role":"user","content":"thank you, I will call a helpline"}]}`
	got := ExtractLatestUserText("openai", []byte(body))
	if got != "thank you, I will call a helpline" {
		t.Fatalf("latest turn = %q", got)
	}

	long := make([]byte, MaxScanBytes+32)
	for i := range long {
		long[i] = 'a'
	}
	capped := ExtractLatestUserText("openai", []byte(`{"messages":[{"role":"user","content":"`+string(long)+`"}]}`))
	if len(capped) != MaxScanBytes {
		t.Fatalf("capped length = %d, want %d", len(capped), MaxScanBytes)
	}
}

func TestExtractInvalidPayloadFailsOpen(t *testing.T) {
	t.Parallel()
	if got := ExtractLatestUserText("openai", []byte(`not-json`)); got != "" {
		t.Fatalf("invalid json = %q, want empty", got)
	}
	if got := ExtractLatestUserText("openai", nil); got != "" {
		t.Fatalf("nil payload = %q, want empty", got)
	}
}
