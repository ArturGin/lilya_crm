package analysis

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type Transcript struct {
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Language string    `json:"language"`
	Text     string    `json:"text"`
	Segments []Segment `json:"segments"`
}

func (t *Transcript) Empty() bool { return t == nil || strings.TrimSpace(t.Text) == "" }

// ForPrompt renders the transcript as timestamped lines ("[12:30] text").
func (t *Transcript) ForPrompt() string {
	if len(t.Segments) == 0 {
		return t.Text
	}
	var b strings.Builder
	for _, s := range t.Segments {
		txt := strings.TrimSpace(s.Text)
		if txt == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s] %s\n", fmtClock(int(s.Start)), txt)
	}
	return b.String()
}

// Transcriber turns an audio file into text with timestamps. Implementations:
// any OpenAI-compatible /audio/transcriptions endpoint (OpenAI Whisper, Groq,
// self-hosted faster-whisper via speaches, ...). Add others (local
// whisper.cpp CLI, Deepgram, AssemblyAI) by implementing this interface.
type Transcriber interface {
	Provider() string
	Model() string
	Transcribe(ctx context.Context, audioPath, language string) (*Transcript, error)
}

// TranscribeStep extracts the audio track, transcribes it in chunks and caches
// the transcript per video.
type TranscribeStep struct {
	T            Transcriber
	Language     string // hint, e.g. "ru"; "" = auto-detect
	ChunkSeconds int    // split long audio (API upload limits, timeouts)
}

func (s *TranscribeStep) Info() StepInfo {
	return StepInfo{
		Name:        "transcribe",
		Title:       "Speech transcription",
		Description: "Transcribes the teacher's voice (Whisper-compatible API). The transcript is searchable and feeds later steps.",
		Provider:    s.T.Provider(),
		Model:       s.T.Model(),
		Uses:        []string{"audio"},
		Produces:    []string{"transcript"},
	}
}

func (s *TranscribeStep) Run(ctx context.Context, sc *StepContext) (*StepOutput, error) {
	if sc.Cache != nil {
		cached, err := sc.Cache.GetTranscript(ctx, sc.AssetID, s.T.Provider(), s.T.Model())
		if err != nil {
			return nil, err
		}
		if cached != nil {
			sc.Logger().Info("using cached transcript", "provider", cached.Provider, "model", cached.Model, "words", wordCount(cached.Text))
			return &StepOutput{Transcript: cached, Note: fmt.Sprintf("cached transcript, %d words", wordCount(cached.Text))}, nil
		}
	}
	probe, err := sc.Media.Probe(ctx, sc.VideoPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	if probe.AudioCodec == "" {
		return nil, skipf("video has no audio track")
	}

	dir, err := os.MkdirTemp(sc.TempDir, "audio-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	chunk := float64(s.ChunkSeconds)
	if chunk <= 0 {
		chunk = 1200
	}
	total := probe.DurationSeconds
	n := max(1, int(math.Ceil(total/chunk)))
	out := &Transcript{Provider: s.T.Provider(), Model: s.T.Model(), Language: s.Language}
	var texts []string
	log := sc.Logger()
	log.Info("transcribing audio", "duration_s", int(total), "chunks", n, "chunk_s", int(chunk),
		"provider", s.T.Provider(), "model", s.T.Model(), "language", s.Language)
	for i := range n {
		start := float64(i) * chunk
		path := filepath.Join(dir, fmt.Sprintf("chunk-%03d.mp3", i))
		clog := log.With("chunk", fmt.Sprintf("%d/%d", i+1, n), "from_s", int(start))
		t0 := time.Now()
		if err := sc.Media.ExtractAudio(ctx, sc.VideoPath, path, start, chunk); err != nil {
			return nil, err
		}
		var sizeKB int64
		if st, err := os.Stat(path); err == nil {
			sizeKB = st.Size() >> 10
		}
		clog.Info("audio chunk extracted, sending to transcriber", "size_kb", sizeKB, "took", time.Since(t0).Round(time.Millisecond).String())
		t1 := time.Now()
		part, err := s.T.Transcribe(ctx, path, s.Language)
		if err != nil {
			clog.Warn("transcription request failed", "err", err, "took", time.Since(t1).Round(time.Millisecond).String())
			return nil, err
		}
		clog.Info("audio chunk transcribed", "words", wordCount(part.Text), "segments", len(part.Segments),
			"language", part.Language, "took", time.Since(t1).Round(time.Millisecond).String())
		if part.Language != "" {
			out.Language = part.Language
		}
		texts = append(texts, strings.TrimSpace(part.Text))
		for _, seg := range part.Segments {
			seg.Start += start
			seg.End += start
			out.Segments = append(out.Segments, seg)
		}
	}
	out.Text = strings.TrimSpace(strings.Join(texts, " "))

	if sc.Cache != nil {
		if err := sc.Cache.PutTranscript(ctx, sc.AssetID, out); err != nil {
			return nil, err
		}
	}
	note := fmt.Sprintf("%d words, %d segments", wordCount(out.Text), len(out.Segments))
	if out.Empty() {
		note = "no speech detected"
	}
	return &StepOutput{Transcript: out, Note: note}, nil
}

func wordCount(s string) int { return len(strings.Fields(s)) }
