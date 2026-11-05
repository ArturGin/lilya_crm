package analysis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lilya/practice/internal/media"
)

// Step is one pluggable analysis stage. To add a new tool, model or approach
// (pose estimation, another LLM, a human-review import, ...), implement Step
// and register it in cmd/lilya — nothing else needs to change.
type Step interface {
	Info() StepInfo
	Run(ctx context.Context, sc *StepContext) (*StepOutput, error)
}

// StepInfo describes a step to admins and in stored run records.
type StepInfo struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Provider    string   `json:"provider"`
	Model       string   `json:"model,omitempty"`
	Uses        []string `json:"uses"`     // inputs: "audio", "frames", "transcript", "hints"
	Produces    []string `json:"produces"` // outputs: "transcript", "tags", "summary"
}

// MergeMode controls how a step's result combines with earlier steps.
type MergeMode int

const (
	// Union adds the fragment's findings to what earlier steps found.
	Union MergeMode = iota
	// Refine replaces earlier findings: the step saw them as hints and
	// returns its own judgement (e.g. an LLM confirming keyword matches).
	Refine
)

type StepOutput struct {
	Result     *Result
	Mode       MergeMode
	Transcript *Transcript
	Usage      Usage
	Note       string // short human-readable remark shown to admins
}

// ErrSkip means the step had nothing to do (e.g. no transcript to read).
var ErrSkip = errors.New("skipped")

func skipf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrSkip}, args...)...)
}

// TranscriptCache stores transcripts per asset so re-analysis does not pay
// for transcription again.
type TranscriptCache interface {
	GetTranscript(ctx context.Context, assetID int64, provider, model string) (*Transcript, error)
	PutTranscript(ctx context.Context, assetID int64, t *Transcript) error
}

// StepContext carries the inputs available to steps and accumulates the
// outputs of earlier steps.
type StepContext struct {
	AssetID         int64
	Title           string
	Description     string
	DurationSeconds int
	Lang            string

	VideoPath string
	Media     media.FFmpeg
	TempDir   string
	Cache     TranscriptCache
	Log       *slog.Logger // optional

	// Filled in as steps run.
	Transcript *Transcript
	Current    Result

	framesMu sync.Mutex
	frames   map[[2]int][]Frame
}

// Logger returns the run's logger (a no-op one if none was set).
func (sc *StepContext) Logger() *slog.Logger {
	if sc.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return sc.Log
}

// Frames samples n evenly spaced frames scaled to width px (memoised, so
// several steps can share them).
func (sc *StepContext) Frames(ctx context.Context, n, width int) ([]Frame, error) {
	sc.framesMu.Lock()
	defer sc.framesMu.Unlock()
	key := [2]int{n, width}
	if f, ok := sc.frames[key]; ok {
		return f, nil
	}
	start := time.Now()
	f, err := sampleFrames(ctx, sc.Media, sc.VideoPath, sc.DurationSeconds, n, width)
	if err != nil {
		return nil, err
	}
	sc.Logger().Info("frames sampled", "frames", len(f), "width", width, "took", time.Since(start).Round(time.Millisecond).String())
	if sc.frames == nil {
		sc.frames = map[[2]int][]Frame{}
	}
	sc.frames[key] = f
	return f, nil
}

// StepRecord is the persisted log of one step within an analysis run.
type StepRecord struct {
	Name       string    `json:"name"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model,omitempty"`
	Status     string    `json:"status"` // running | done | skipped | failed
	Note       string    `json:"note,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	DurationMs int64     `json:"duration_ms"`
	Usage      Usage     `json:"usage"`
	Output     *Result   `json:"output,omitempty"`
}

// Runner holds the configured steps (in execution order) and the default
// selection used for automatic analysis after upload.
type Runner struct {
	steps    []Step
	defaults []string
}

// NewRunner registers steps in execution order. defaults selects which run
// automatically; empty means all.
func NewRunner(steps []Step, defaults []string) (*Runner, error) {
	r := &Runner{steps: steps}
	if len(defaults) == 0 {
		for _, s := range steps {
			r.defaults = append(r.defaults, s.Info().Name)
		}
		return r, nil
	}
	for _, d := range defaults {
		if r.find(d) == nil {
			return nil, fmt.Errorf("ANALYSIS_STEPS: step %q is not available (available: %s)", d, strings.Join(r.names(), ", "))
		}
	}
	r.defaults = defaults
	return r, nil
}

func (r *Runner) find(name string) Step {
	for _, s := range r.steps {
		if s.Info().Name == name {
			return s
		}
	}
	return nil
}

func (r *Runner) names() []string {
	var out []string
	for _, s := range r.steps {
		out = append(out, s.Info().Name)
	}
	return out
}

func (r *Runner) Enabled() bool      { return r != nil && len(r.steps) > 0 }
func (r *Runner) Defaults() []string { return r.defaults }

func (r *Runner) Steps() []StepInfo {
	out := []StepInfo{}
	for _, s := range r.steps {
		out = append(out, s.Info())
	}
	return out
}

// Resolve validates a requested selection and returns the steps in
// execution order. nil/empty selects the defaults.
func (r *Runner) Resolve(requested []string) ([]Step, error) {
	if len(requested) == 0 {
		requested = r.defaults
	}
	want := map[string]bool{}
	for _, n := range requested {
		if r.find(n) == nil {
			return nil, fmt.Errorf("unknown analysis step %q", n)
		}
		want[n] = true
	}
	var out []Step
	for _, s := range r.steps {
		if want[s.Info().Name] {
			out = append(out, s)
		}
	}
	return out, nil
}

// Run executes steps in order. A step that fails permanently is recorded and
// the run continues; a transient failure aborts the run (the job is retried,
// and cached transcripts make the retry cheap). onProgress is called after
// every state change so callers can persist live progress.
func (r *Runner) Run(ctx context.Context, steps []Step, sc *StepContext, onProgress func([]StepRecord)) (Result, []StepRecord, error) {
	records := make([]StepRecord, len(steps))
	for i, s := range steps {
		info := s.Info()
		records[i] = StepRecord{Name: info.Name, Provider: info.Provider, Model: info.Model, Status: "pending"}
	}
	notify := func() {
		if onProgress != nil {
			onProgress(records)
		}
	}
	acc := Result{}
	acc.Normalize()
	done := 0
	for i, s := range steps {
		rec := &records[i]
		rec.Status, rec.StartedAt = "running", time.Now()
		notify()
		log := sc.Logger().With("step", rec.Name, "step_no", fmt.Sprintf("%d/%d", i+1, len(steps)))
		log.Info("analysis step started", "provider", rec.Provider, "model", rec.Model)

		sc.Current = acc
		out, err := s.Run(ctx, sc)
		rec.DurationMs = time.Since(rec.StartedAt).Milliseconds()
		took := time.Since(rec.StartedAt).Round(time.Millisecond).String()
		switch {
		case errors.Is(err, ErrSkip):
			rec.Status, rec.Note = "skipped", strings.TrimPrefix(strings.TrimPrefix(err.Error(), ErrSkip.Error()), ": ")
			log.Info("analysis step skipped", "reason", rec.Note, "took", took)
		case errors.Is(err, ErrPermanent):
			rec.Status, rec.Error = "failed", err.Error()
			log.Error("analysis step failed (permanent, continuing with next step)", "err", err, "took", took)
		case err != nil:
			rec.Status, rec.Error = "failed", err.Error()
			log.Warn("analysis step failed (transient, run will be retried)", "err", err, "took", took)
			notify()
			return acc, records, err // transient: let the job retry
		default:
			rec.Status, rec.Note, rec.Usage = "done", out.Note, out.Usage
			log.Info("analysis step done", "note", out.Note, "input_tokens", out.Usage.InputTokens,
				"output_tokens", out.Usage.OutputTokens, "took", took)
			done++
			if out.Transcript != nil {
				sc.Transcript = out.Transcript
			}
			if out.Result != nil {
				frag := *out.Result
				frag.Normalize()
				frag.stamp(s.Info().Name)
				rec.Output = &frag
				if out.Mode == Refine {
					acc = MergeRefine(acc, frag)
				} else {
					acc = MergeUnion(acc, frag)
				}
			}
		}
		notify()
	}
	if done == 0 {
		return acc, records, fmt.Errorf("%w: no analysis step succeeded", ErrPermanent)
	}
	return acc, records, nil
}
