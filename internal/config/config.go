// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr  string
	DatabaseURL string
	// PublicURL is the externally visible origin, e.g. https://practice.example.com.
	PublicURL string
	DataDir   string

	CookieSecure bool
	SessionTTL   time.Duration
	AllowSignup  bool
	// TrustProxy makes the server honour X-Forwarded-For / X-Real-IP. Enable
	// only when running behind a reverse proxy you control.
	TrustProxy bool

	MaxVideoBytes int64
	MaxImageBytes int64

	FFmpegPath  string
	FFprobePath string

	WorkerConcurrency int

	// Video analysis pipeline.
	// AnalysisSteps selects which configured steps run automatically, in
	// order (empty = every available step).
	AnalysisSteps    []string
	AnalysisLanguage string

	// Step "transcribe": OpenAI-compatible speech-to-text.
	TranscribeEnabled        bool
	TranscribeProvider       string
	TranscribeBaseURL        string
	TranscribeAPIKey         string
	TranscribeModel          string
	TranscribeLanguage       string
	TranscribeResponseFormat string
	TranscribeChunkSeconds   int
	TranscribePrompt         string

	// Step "keywords": built-in dictionary matching on the transcript.
	KeywordsEnabled bool

	// Step "claude": synthesis with Claude.
	ClaudeEnabled    bool
	ClaudeModel      string
	ClaudeEffort     string
	ClaudeInputs     []string // "transcript", "frames"
	ClaudeMaxFrames  int
	ClaudeFrameWidth int

	// Admin Telegram bot (notifications + video uploads). Off without a token.
	TelegramBotToken string
	// TelegramAPIURL is the Bot API endpoint: the public one (20 MB download
	// limit) or a local telegram-bot-api server started with --local.
	TelegramAPIURL string
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:        env("LISTEN_ADDR", ":8080"),
		DatabaseURL:       env("DATABASE_URL", ""),
		PublicURL:         strings.TrimRight(env("PUBLIC_URL", "http://localhost:8080"), "/"),
		DataDir:           env("DATA_DIR", "./data"),
		CookieSecure:      envBool("COOKIE_SECURE", true),
		SessionTTL:        envDuration("SESSION_TTL", 30*24*time.Hour),
		AllowSignup:       envBool("ALLOW_SIGNUP", false),
		TrustProxy:        envBool("TRUST_PROXY", false),
		MaxVideoBytes:     envInt64("MAX_VIDEO_MB", 4096) << 20,
		MaxImageBytes:     envInt64("MAX_IMAGE_MB", 20) << 20,
		FFmpegPath:        env("FFMPEG_PATH", "ffmpeg"),
		FFprobePath:       env("FFPROBE_PATH", "ffprobe"),
		WorkerConcurrency: int(envInt64("WORKER_CONCURRENCY", 1)),
		AnalysisSteps:     list(env("ANALYSIS_STEPS", "")),
		AnalysisLanguage:  env("ANALYSIS_LANGUAGE", "ru"),

		TranscribeBaseURL:        strings.TrimRight(env("TRANSCRIBE_BASE_URL", ""), "/"),
		TranscribeAPIKey:         env("TRANSCRIBE_API_KEY", ""),
		TranscribeModel:          env("TRANSCRIBE_MODEL", "whisper-1"),
		TranscribeProvider:       env("TRANSCRIBE_PROVIDER", ""),
		TranscribeResponseFormat: env("TRANSCRIBE_RESPONSE_FORMAT", "verbose_json"),
		TranscribeChunkSeconds:   int(envInt64("TRANSCRIBE_CHUNK_SECONDS", 1200)),
		TranscribePrompt:         env("TRANSCRIBE_PROMPT", ""),

		KeywordsEnabled: envBool("KEYWORDS_ENABLED", true),

		ClaudeModel:      env("CLAUDE_MODEL", "claude-opus-5-5"),
		ClaudeEffort:     env("CLAUDE_EFFORT", "high"),
		ClaudeInputs:     list(env("CLAUDE_INPUTS", "transcript,frames")),
		ClaudeMaxFrames:  int(envInt64("CLAUDE_MAX_FRAMES", 90)),
		ClaudeFrameWidth: int(envInt64("CLAUDE_FRAME_WIDTH", 768)),

		TelegramBotToken: env("TELEGRAM_BOT_TOKEN", ""),
		TelegramAPIURL:   strings.TrimRight(env("TELEGRAM_API_URL", "https://api.telegram.org"), "/"),
	}
	c.TranscribeLanguage = env("TRANSCRIBE_LANGUAGE", c.AnalysisLanguage)
	if c.TranscribeLanguage == "auto" {
		c.TranscribeLanguage = ""
	}
	// Transcription is on when an endpoint or key is configured; with only a
	// key, OpenAI is assumed.
	if c.TranscribeBaseURL == "" && c.TranscribeAPIKey != "" {
		c.TranscribeBaseURL = "https://api.openai.com/v1"
	}
	c.TranscribeEnabled = envBool("TRANSCRIBE_ENABLED", c.TranscribeBaseURL != "")
	if c.TranscribeProvider == "" {
		c.TranscribeProvider = providerFromURL(c.TranscribeBaseURL)
	}
	// Claude is on whenever an Anthropic credential is configured.
	hasKey := os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != ""
	c.ClaudeEnabled = envBool("CLAUDE_ENABLED", hasKey)

	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if c.WorkerConcurrency < 1 {
		c.WorkerConcurrency = 1
	}
	if c.ClaudeMaxFrames < 4 {
		c.ClaudeMaxFrames = 4
	}
	switch c.ClaudeEffort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return c, fmt.Errorf("CLAUDE_EFFORT must be one of low|medium|high|xhigh|max")
	}
	for _, in := range c.ClaudeInputs {
		if in != "transcript" && in != "frames" {
			return c, fmt.Errorf("CLAUDE_INPUTS: unknown input %q (use transcript, frames)", in)
		}
	}
	if c.AnalysisLanguage != "ru" && c.AnalysisLanguage != "en" {
		return c, fmt.Errorf("ANALYSIS_LANGUAGE must be ru or en")
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func list(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func providerFromURL(u string) string {
	switch {
	case u == "":
		return ""
	case strings.Contains(u, "api.openai.com"):
		return "openai"
	case strings.Contains(u, "groq.com"):
		return "groq"
	default:
		return "whisper-server"
	}
}

// Has reports whether v is in list.
func Has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
