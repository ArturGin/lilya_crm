package analysis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"
)

// ClaudeStep asks Claude to produce the final structured analysis from the
// transcript (what the teacher says), sampled frames (what is shown) and the
// findings of earlier steps (as hints it may confirm or reject). Its output
// refines (replaces) earlier findings. The API key is read from
// ANTHROPIC_API_KEY.
type ClaudeStep struct {
	ModelName     string
	Effort        string
	Lang          string
	UseFrames     bool
	UseTranscript bool
	MaxFrames     int
	FrameWidth    int

	client anthropic.Client
}

func NewClaudeStep(c ClaudeStep) *ClaudeStep {
	c.client = anthropic.NewClient()
	return &c
}

func (c *ClaudeStep) Info() StepInfo {
	var uses []string
	if c.UseTranscript {
		uses = append(uses, "transcript")
	}
	if c.UseFrames {
		uses = append(uses, "frames")
	}
	uses = append(uses, "hints")
	return StepInfo{
		Name:        "claude",
		Title:       "Claude synthesis",
		Description: "Claude reads the transcript and looks at frames sampled from the video, checks earlier findings, and writes the summary, focus, body areas and pose list.",
		Provider:    "anthropic",
		Model:       c.ModelName,
		Uses:        uses,
		Produces:    []string{"summary", "tags"},
	}
}

// Models that accept the server-side refusal fallback ("default" routing).
var fallbackModels = map[string]bool{
	"claude-opus-5-5": true, "claude-opus-5": true, "claude-fable-5-1": true, "claude-sonnet-5-5": true,
}

func (c *ClaudeStep) Run(ctx context.Context, sc *StepContext) (*StepOutput, error) {
	hasTranscript := c.UseTranscript && !sc.Transcript.Empty()
	if !hasTranscript && !c.UseFrames {
		return nil, skipf("no transcript and frames are disabled")
	}

	blocks := []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(c.describe(sc, hasTranscript))}
	if hasTranscript {
		blocks = append(blocks, anthropic.NewBetaTextBlock("<transcript>\n"+sc.Transcript.ForPrompt()+"</transcript>"))
	}
	if hints := describeHints(sc.Current); hints != "" {
		blocks = append(blocks, anthropic.NewBetaTextBlock(hints))
	}
	nFrames := 0
	if c.UseFrames {
		frames, err := sc.Frames(ctx, c.MaxFrames, c.FrameWidth)
		if err != nil {
			return nil, err
		}
		nFrames = len(frames)
		for _, f := range frames {
			blocks = append(blocks,
				anthropic.NewBetaTextBlock(fmt.Sprintf("Frame at %s (%d s):", fmtClock(int(f.AtSeconds)), int(f.AtSeconds))),
				anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
					Data:      base64.StdEncoding.EncodeToString(f.JPEG),
					MediaType: anthropic.BetaBase64ImageSourceMediaType("image/jpeg"),
				}),
			)
		}
	}
	blocks = append(blocks, anthropic.NewBetaTextBlock("Analyse the practice now and answer with the JSON object."))

	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.ModelName),
		MaxTokens: 32000,
		System:    []anthropic.BetaTextBlockParam{{Text: systemPrompt(c.Lang)}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(blocks...)},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffort(c.Effort),
			Format: anthropic.BetaJSONOutputFormatParam{Schema: json.RawMessage(resultSchema())},
		},
	}
	if fallbackModels[c.ModelName] {
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}

	// Long inputs (many images, long transcripts): stream to avoid HTTP timeouts.
	sc.Logger().Info("calling Claude", "model", c.ModelName, "effort", c.Effort, "transcript", hasTranscript, "frames", nFrames)
	t0 := time.Now()
	stream := c.client.Beta.Messages.NewStreaming(ctx, params)
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		return nil, classifyAPIError(err)
	}
	usage := Usage{InputTokens: int(msg.Usage.InputTokens), OutputTokens: int(msg.Usage.OutputTokens)}
	sc.Logger().Info("Claude responded", "stop_reason", msg.StopReason, "input_tokens", usage.InputTokens,
		"output_tokens", usage.OutputTokens, "took", time.Since(t0).Round(time.Millisecond).String())

	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return nil, fmt.Errorf("%w: model declined to analyse this video (%s)", ErrPermanent, msg.StopDetails.Category)
	case anthropic.BetaStopReasonMaxTokens:
		return nil, fmt.Errorf("%w: response truncated (max_tokens)", ErrPermanent)
	}

	var text strings.Builder
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	var res Result
	if err := json.Unmarshal([]byte(text.String()), &res); err != nil {
		return nil, fmt.Errorf("decode analysis JSON: %w", err)
	}
	var used []string
	if hasTranscript {
		used = append(used, "transcript")
	}
	if nFrames > 0 {
		used = append(used, fmt.Sprintf("%d frames", nFrames))
	}
	return &StepOutput{Result: &res, Mode: Refine, Usage: usage, Note: "used " + strings.Join(used, " + ")}, nil
}

func classifyAPIError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 400, 401, 403, 404, 413, 422:
			return fmt.Errorf("%w: %v", ErrPermanent, err)
		}
	}
	return err // 429 / 5xx / network: retry
}

func (c *ClaudeStep) describe(sc *StepContext, hasTranscript bool) string {
	var b strings.Builder
	b.WriteString("Practice title: " + sc.Title + "\n")
	if strings.TrimSpace(sc.Description) != "" {
		b.WriteString("Teacher's description (may be partial):\n" + sc.Description + "\n")
	}
	fmt.Fprintf(&b, "Total duration: %s.\n", fmtClock(sc.DurationSeconds))
	if hasTranscript {
		b.WriteString("A speech-to-text transcript of the teacher follows (automatic, may contain recognition errors, timestamps in [m:ss]).\n")
	}
	if c.UseFrames {
		b.WriteString("Frames sampled at regular intervals follow after the transcript, each labelled with its timestamp.\n")
	}
	b.WriteString("\nKnown poses (use these slugs when a pose matches one of them; otherwise invent a canonical English kebab-case slug):\n")
	b.WriteString(poseSlugList())
	return b.String()
}

// describeHints renders earlier steps' findings for the model to verify.
func describeHints(r Result) string {
	if r.IsEmpty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("<earlier_findings>\nAutomatic findings from earlier analysis steps (e.g. keyword matching on the transcript). They can contain false positives — a pose that is only mentioned, not practised — and miss poses that are shown but not named. Confirm, correct or drop them.\n")
	for _, p := range r.Poses {
		fmt.Fprintf(&b, "- pose %s at %s (from %s)\n", p.Slug, fmtClock(p.StartSeconds), strings.Join(p.Sources, ","))
	}
	for _, f := range r.Focus {
		fmt.Fprintf(&b, "- focus %s (from %s)\n", f.Slug, strings.Join(f.Sources, ","))
	}
	for _, a := range r.BodyAreas {
		fmt.Fprintf(&b, "- body area %s (from %s)\n", a.Slug, strings.Join(a.Sources, ","))
	}
	b.WriteString("</earlier_findings>")
	return b.String()
}

func fmtClock(sec int) string {
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func systemPrompt(lang string) string {
	language := "Russian (use the names Russian-speaking yoga teachers commonly use)"
	if lang == "en" {
		language = "English"
	}
	return `You are an experienced yoga and movement teacher who catalogues recorded practices for a members-only video library. Students use your analysis to find practices by what they work on (e.g. hip opening, backbends, chest opening) and by the poses included, so accuracy matters more than completeness.

You may receive the teacher's spoken instructions as a transcript, still frames sampled from the video, and findings from earlier automatic steps. Use the transcript for what the teacher names and intends; use the frames to confirm what is actually practised. Work out what the practice actually does.

How to fill the fields:
- summary: 2–4 sentences for students: what the practice focuses on, how it is structured (warm-up, main part, final relaxation), and who it suits. No marketing language.
- practice_type: a short label such as "hatha", "vinyasa", "yin", "stretching", "joint mobility", "strength", "breathwork".
- focus and body_areas: choose only from the allowed slugs. Mark primary=true only for the one to three things the practice is mainly about; other clearly worked items are primary=false. Leave out anything only touched on in passing.
- poses: list each distinct pose that is actually practised (held or flowed through), once, without left/right duplicates. Use the known-pose slug when one fits; otherwise a canonical English kebab-case slug without sides or variations. name is the pose name in the output language, name_en the English name, sanskrit the Sanskrit name or "" if none is standard. start_seconds is when it is first practised (from transcript timestamps or frame labels). body_areas are the main areas that pose targets (allowed slugs only).
- Prefer omitting a pose to guessing. A pose that is only mentioned (e.g. "we will not do headstand today") is not practised.
- props: equipment visible or required (mat, blocks, strap, bolster, blanket, chair, wall), in the output language; [] if none.
- cautions: one short sentence about who should be careful (e.g. knee or lower-back issues), or "" if nothing notable.

Write summary, practice_type, name, props and cautions in ` + language + `.`
}

// resultSchema is the JSON schema enforced through structured outputs.
func resultSchema() []byte {
	emphasis := func(vocab []Term) map[string]any {
		return map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"slug", "primary"},
				"properties": map[string]any{
					"slug":    map[string]any{"type": "string", "enum": slugs(vocab)},
					"primary": map[string]any{"type": "boolean"},
				},
			},
		}
	}
	str := map[string]any{"type": "string"}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"summary", "practice_type", "level", "intensity", "focus", "body_areas", "poses", "props", "cautions"},
		"properties": map[string]any{
			"summary":       str,
			"practice_type": str,
			"level":         map[string]any{"type": "string", "enum": slugs(Levels)},
			"intensity":     map[string]any{"type": "string", "enum": []string{"low", "moderate", "high"}},
			"focus":         emphasis(Focuses),
			"body_areas":    emphasis(BodyAreas),
			"poses": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"slug", "name", "name_en", "sanskrit", "start_seconds", "body_areas"},
					"properties": map[string]any{
						"slug":          str,
						"name":          str,
						"name_en":       str,
						"sanskrit":      str,
						"start_seconds": map[string]any{"type": "integer"},
						"body_areas": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string", "enum": slugs(BodyAreas)},
						},
					},
				},
			},
			"props":    map[string]any{"type": "array", "items": str},
			"cautions": str,
		},
	}
	b, _ := json.Marshal(schema)
	return b
}
