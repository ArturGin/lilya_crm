package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenAICompatTranscriber calls an OpenAI-compatible
// POST {BaseURL}/audio/transcriptions endpoint. Works with:
//   - OpenAI          BaseURL=https://api.openai.com/v1   Model=whisper-1
//   - Groq            BaseURL=https://api.groq.com/openai/v1 Model=whisper-large-v3
//   - speaches (self-hosted faster-whisper) BaseURL=http://whisper:8000/v1 Model=Systran/faster-whisper-small
type OpenAICompatTranscriber struct {
	ProviderName   string // label stored with transcripts, e.g. "openai", "speaches"
	BaseURL        string
	APIKey         string
	ModelName      string
	Prompt         string // vocabulary hint (Whisper "prompt": ≤ ~224 tokens)
	ResponseFormat string // "verbose_json" (segments) or "json" (text only)
	HTTP           *http.Client
}

func (t *OpenAICompatTranscriber) Provider() string { return t.ProviderName }
func (t *OpenAICompatTranscriber) Model() string    { return t.ModelName }

func (t *OpenAICompatTranscriber) Transcribe(ctx context.Context, audioPath, language string) (*Transcript, error) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fields := map[string]string{"model": t.ModelName, "response_format": t.ResponseFormat, "temperature": "0"}
	if fields["response_format"] == "" {
		fields["response_format"] = "verbose_json"
	}
	if language != "" {
		fields["language"] = language
	}
	if t.Prompt != "" {
		fields["prompt"] = t.Prompt
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if fields["response_format"] == "verbose_json" {
		_ = mw.WriteField("timestamp_granularities[]", "segment")
	}
	fw, err := mw.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return nil, err
	}
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(fw, f)
	f.Close()
	if err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/audio/transcriptions", body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.APIKey)
	}
	client := t.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute} // CPU whisper can be slow
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("transcription request: %w", err) // network: retry
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Errorf("transcription API %s: %s", resp.Status, truncateStr(string(raw), 400))
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429 {
			return nil, fmt.Errorf("%w: %v", ErrPermanent, msg)
		}
		return nil, msg
	}

	var parsed struct {
		Text     string    `json:"text"`
		Language string    `json:"language"`
		Segments []Segment `json:"segments"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: decode transcription response: %v", ErrPermanent, err)
	}
	out := &Transcript{Provider: t.ProviderName, Model: t.ModelName, Language: normLang(parsed.Language), Text: strings.TrimSpace(parsed.Text)}
	for _, s := range parsed.Segments {
		s.Text = strings.TrimSpace(s.Text)
		if s.Text != "" {
			out.Segments = append(out.Segments, s)
		}
	}
	return out, nil
}

// normLang maps "russian" → "ru" etc. (verbose_json returns full names).
func normLang(l string) string {
	switch strings.ToLower(l) {
	case "russian":
		return "ru"
	case "english":
		return "en"
	}
	return strings.ToLower(l)
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
