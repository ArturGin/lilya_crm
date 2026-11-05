// Package analysis turns a practice video into structured, searchable data:
// the poses performed, the body areas targeted and the overall focus.
//
// Analysis is a pipeline of independent steps (see step.go). Each step —
// speech transcription, keyword matching, an LLM, a pose-estimation model,
// ... — contributes a fragment of Result; fragments are merged and every step
// is recorded with its status, provider/model, cost and output.
package analysis

import (
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/lilya/practice/internal/store"
)

// Result is the structured analysis stored with a post (JSON in the DB and
// returned to clients as-is).
type Result struct {
	Summary      string     `json:"summary"`
	PracticeType string     `json:"practice_type"`
	Level        string     `json:"level"`
	Intensity    string     `json:"intensity"`
	Focus        []Emphasis `json:"focus"`
	BodyAreas    []Emphasis `json:"body_areas"`
	Poses        []Pose     `json:"poses"`
	Props        []string   `json:"props"`
	Cautions     string     `json:"cautions"`
}

type Emphasis struct {
	Slug    string `json:"slug"`
	Primary bool   `json:"primary"`
	// Sources lists the analysis steps that reported this item.
	Sources []string `json:"sources,omitempty"`
}

type Pose struct {
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	NameEN       string   `json:"name_en"`
	Sanskrit     string   `json:"sanskrit"`
	StartSeconds int      `json:"start_seconds"`
	BodyAreas    []string `json:"body_areas"`
	Sources      []string `json:"sources,omitempty"`
}

// Frame is one still sampled from the video.
type Frame struct {
	AtSeconds float64
	JPEG      []byte
}

type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// ErrPermanent marks failures that retrying will not fix (bad credentials,
// unsupported input, refusals). A step failing permanently is recorded and
// the pipeline continues with the next step.
var ErrPermanent = errors.New("permanent analysis failure")

var slugRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// Slugify lower-cases and hyphenates arbitrary text (keeps non-latin letters).
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-")
	}
	return s
}

// IsEmpty reports whether the result carries no information at all.
func (r Result) IsEmpty() bool {
	return r.Summary == "" && r.PracticeType == "" && r.Level == "" && r.Intensity == "" &&
		len(r.Focus) == 0 && len(r.BodyAreas) == 0 && len(r.Poses) == 0 && len(r.Props) == 0 && r.Cautions == ""
}

// Normalize drops unknown vocabulary slugs, de-duplicates entries and
// canonicalises pose slugs (mapping known aliases onto the pose dictionary).
func (r *Result) Normalize() {
	r.Focus = normEmphasis(r.Focus, Focuses)
	r.BodyAreas = normEmphasis(r.BodyAreas, BodyAreas)
	if _, ok := lookup(Levels, r.Level); !ok {
		r.Level = ""
	}
	switch r.Intensity {
	case "low", "moderate", "high":
	default:
		r.Intensity = ""
	}
	seen := map[string]int{}
	poses := []Pose{}
	for _, p := range r.Poses {
		p.Slug = Slugify(p.Slug)
		if p.Slug == "" {
			p.Slug = Slugify(p.NameEN)
		}
		if p.Slug == "" {
			continue
		}
		if canon, ok := PoseBySlug(p.Slug); ok {
			p.Slug = canon.Slug
		}
		var areas []string
		for _, a := range p.BodyAreas {
			if _, ok := lookup(BodyAreas, a); ok && !slices.Contains(areas, a) {
				areas = append(areas, a)
			}
		}
		p.BodyAreas = areas
		p.StartSeconds = max(p.StartSeconds, 0)
		p.Sources = uniq(p.Sources)
		if i, ok := seen[p.Slug]; ok {
			poses[i] = mergePose(poses[i], p)
			continue
		}
		seen[p.Slug] = len(poses)
		poses = append(poses, p)
	}
	sort.SliceStable(poses, func(i, j int) bool { return poses[i].StartSeconds < poses[j].StartSeconds })
	r.Poses = poses
	if r.Props == nil {
		r.Props = []string{}
	}
	r.Props = uniq(r.Props)
}

func normEmphasis(in []Emphasis, vocab []Term) []Emphasis {
	seen := map[string]int{}
	out := []Emphasis{}
	for _, e := range in {
		if _, ok := lookup(vocab, e.Slug); !ok {
			continue
		}
		if i, ok := seen[e.Slug]; ok {
			out[i].Primary = out[i].Primary || e.Primary
			out[i].Sources = uniq(append(out[i].Sources, e.Sources...))
			continue
		}
		e.Sources = uniq(e.Sources)
		seen[e.Slug] = len(out)
		out = append(out, e)
	}
	return out
}

func mergePose(a, b Pose) Pose {
	a.StartSeconds = min(a.StartSeconds, b.StartSeconds)
	if a.Name == "" {
		a.Name = b.Name
	}
	if a.NameEN == "" {
		a.NameEN = b.NameEN
	}
	if a.Sanskrit == "" {
		a.Sanskrit = b.Sanskrit
	}
	a.BodyAreas = uniq(append(a.BodyAreas, b.BodyAreas...))
	a.Sources = uniq(append(a.Sources, b.Sources...))
	return a
}

func uniq(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// stamp marks every item of a fragment as coming from source.
func (r *Result) stamp(source string) {
	for i := range r.Focus {
		r.Focus[i].Sources = uniq(append(r.Focus[i].Sources, source))
	}
	for i := range r.BodyAreas {
		r.BodyAreas[i].Sources = uniq(append(r.BodyAreas[i].Sources, source))
	}
	for i := range r.Poses {
		r.Poses[i].Sources = uniq(append(r.Poses[i].Sources, source))
	}
}

// MergeUnion adds a fragment to the accumulated result: lists are unioned,
// scalar fields are only filled when still empty.
func MergeUnion(acc Result, frag Result) Result {
	if acc.Summary == "" {
		acc.Summary = frag.Summary
	}
	if acc.PracticeType == "" {
		acc.PracticeType = frag.PracticeType
	}
	if acc.Level == "" {
		acc.Level = frag.Level
	}
	if acc.Intensity == "" {
		acc.Intensity = frag.Intensity
	}
	if acc.Cautions == "" {
		acc.Cautions = frag.Cautions
	}
	acc.Focus = append(slices.Clone(acc.Focus), frag.Focus...)
	acc.BodyAreas = append(slices.Clone(acc.BodyAreas), frag.BodyAreas...)
	acc.Poses = append(slices.Clone(acc.Poses), frag.Poses...)
	acc.Props = append(slices.Clone(acc.Props), frag.Props...)
	acc.Normalize()
	return acc
}

// MergeRefine replaces the accumulated result with a fragment produced by a
// step that saw everything before it (e.g. an LLM judging keyword hits). Items
// it confirms keep their earlier provenance; items it omits are dropped.
func MergeRefine(acc Result, frag Result) Result {
	prevPose := map[string]Pose{}
	for _, p := range acc.Poses {
		prevPose[p.Slug] = p
	}
	for i, p := range frag.Poses {
		if old, ok := prevPose[p.Slug]; ok {
			frag.Poses[i].Sources = uniq(append(old.Sources, p.Sources...))
			frag.Poses[i].StartSeconds = min(old.StartSeconds, p.StartSeconds)
		}
	}
	keep := func(prev, next []Emphasis) {
		m := map[string][]string{}
		for _, e := range prev {
			m[e.Slug] = e.Sources
		}
		for i, e := range next {
			next[i].Sources = uniq(append(slices.Clone(m[e.Slug]), e.Sources...))
		}
	}
	keep(acc.Focus, frag.Focus)
	keep(acc.BodyAreas, frag.BodyAreas)
	frag.Normalize()
	return frag
}

// Tags converts the result into searchable tags for the post.
func (r Result) Tags(lang string) []store.TagInput {
	var tags []store.TagInput
	for _, f := range r.Focus {
		t, _ := lookup(Focuses, f.Slug)
		tags = append(tags, store.TagInput{Kind: store.TagFocus, Slug: t.Slug, Name: t.Name(lang), Primary: f.Primary,
			Origin: strings.Join(f.Sources, ",")})
	}
	for _, b := range r.BodyAreas {
		t, _ := lookup(BodyAreas, b.Slug)
		tags = append(tags, store.TagInput{Kind: store.TagBodyArea, Slug: t.Slug, Name: t.Name(lang), Primary: b.Primary,
			Origin: strings.Join(b.Sources, ",")})
	}
	if t, ok := lookup(Levels, r.Level); ok {
		tags = append(tags, store.TagInput{Kind: store.TagLevel, Slug: t.Slug, Name: t.Name(lang)})
	}
	for _, p := range r.Poses {
		name := p.Name
		if lang == "en" || name == "" {
			name = p.NameEN
		}
		if canon, ok := PoseBySlug(p.Slug); ok {
			name = canon.DisplayName(lang)
		}
		if name == "" {
			name = p.Slug
		}
		tags = append(tags, store.TagInput{Kind: store.TagPose, Slug: p.Slug, Name: name, Origin: strings.Join(p.Sources, ",")})
	}
	return tags
}

// SearchText is extra free text indexed for full-text search (summary plus
// every alternative pose name, so "pigeon", "голубь" and "kapotasana" all hit).
func (r Result) SearchText() string {
	parts := []string{r.Summary, r.PracticeType, r.Cautions}
	for _, p := range r.Poses {
		parts = append(parts, p.Name, p.NameEN, p.Sanskrit)
		if canon, ok := PoseBySlug(p.Slug); ok {
			parts = append(parts, canon.RU, canon.EN, canon.Sanskrit)
		}
	}
	parts = append(parts, r.Props...)
	return strings.Join(parts, " \n")
}
