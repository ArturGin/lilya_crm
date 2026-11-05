package analysis

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// KeywordStep finds poses, focus and body areas mentioned in the transcript
// using the pose dictionary and keyword lists. Free, fast, deterministic and
// explainable — but literal: a pose that is shown but never named is missed,
// and a pose that is only mentioned in passing is reported. Later steps
// (e.g. Claude) receive these hits as hints and can confirm or reject them.
type KeywordStep struct{}

func (KeywordStep) Info() StepInfo {
	return StepInfo{
		Name:        "keywords",
		Title:       "Keyword matching",
		Description: "Finds poses, focus and body areas named in the transcript using the built-in pose dictionary (RU/EN/Sanskrit). No API cost.",
		Provider:    "built-in",
		Uses:        []string{"transcript"},
		Produces:    []string{"tags"},
	}
}

// Focus / body-area phrases that are specific enough to count. Generic
// instruction words ("колени", "позвоночник", "дыхание") are deliberately
// absent: teachers say them in every class.
var focusKeywords = map[string][]string{
	"hip-opening":   {"раскрыт* тбс", "раскрыт* тазобедр*", "раскрыт* таз*", "hip open*", "hip opener*"},
	"chest-opening": {"раскрыт* груд*", "chest open*", "heart open*"},
	"backbends":     {"прогиб*", "backbend*"},
	"forward-folds": {"наклон* вперед", "forward fold*"},
	"twists":        {"скрутк*", "twist*"},
	"side-bends":    {"боков* вытяж*", "боков* наклон*", "side bend*"},
	"inversions":    {"перевернут* асан*", "инверси*", "inversion*"},
	"arm-balances":  {"баланс* на рук*", "arm balance*"},
	"balance":       {"равновеси*", "баланс* на одн*"},
	"flexibility":   {"растяжк*", "гибкост*", "stretching"},
	"mobility":      {"мобильност*", "суставн* гимнастик*", "mobility"},
	"posture":       {"осанк*", "posture"},
	"relaxation":    {"расслаблени*", "релакс*", "relaxation"},
	"breathwork":    {"пранаям*", "дыхательн* практик*", "уджайи", "капалабхат*", "нади шодхан*", "pranayama", "breathwork"},
	"meditation":    {"медитаци*", "meditation"},
}

var bodyAreaKeywords = map[string][]string{
	"hips":           {"тбс", "тазобедрен*", "hips"},
	"hip-flexors":    {"сгибател* бедр*", "подвздошн* поясничн*", "псоас*", "hip flexor*", "psoas"},
	"hamstrings":     {"задн* поверхност* бедр*", "hamstring*"},
	"adductors":      {"внутренн* поверхност* бедр*", "приводящ*", "adductor*"},
	"thoracic-spine": {"грудн* отдел*", "thoracic*"},
	"pelvic-floor":   {"тазов* дн*", "pelvic floor"},
	"glutes":         {"ягодиц*", "glute*"},
}

// A focus phrase also names its body area ("раскрытие ТБС" → hips).
var focusImpliesArea = map[string]string{
	"hip-opening":   "hips",
	"chest-opening": "thoracic-spine",
}

type kwPattern struct {
	kind, slug string
	tokens     []string
}

var kwPatterns = func() []kwPattern {
	var out []kwPattern
	add := func(kind, slug string, phrases []string) {
		for _, ph := range phrases {
			out = append(out, kwPattern{kind: kind, slug: slug, tokens: strings.Fields(normText(ph))})
		}
	}
	for _, p := range Poses {
		add("pose", p.Slug, p.Match)
	}
	for slug, ph := range focusKeywords {
		add("focus", slug, ph)
	}
	for slug, ph := range bodyAreaKeywords {
		add("body_area", slug, ph)
	}
	// Longest patterns first so "боковая планка" beats "планка".
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].tokens) > len(out[j].tokens) })
	return out
}()

func normText(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "ё", "е")
}

type timedWord struct {
	w  string
	at float64
}

// words tokenises the transcript; each word gets an approximate timestamp
// interpolated within its segment.
func words(t *Transcript) []timedWord {
	split := func(s string) []string {
		return strings.FieldsFunc(normText(s), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '*'
		})
	}
	var out []timedWord
	if len(t.Segments) == 0 {
		for _, w := range split(t.Text) {
			out = append(out, timedWord{w: w})
		}
		return out
	}
	for _, s := range t.Segments {
		ws := split(s.Text)
		for i, w := range ws {
			at := s.Start + (s.End-s.Start)*float64(i)/float64(max(len(ws), 1))
			out = append(out, timedWord{w: w, at: at})
		}
	}
	return out
}

func tokenMatch(pattern, word string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(word, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == word
}

type kwHit struct {
	count int
	first float64
}

// matchKeywords scans left to right; at each word the longest matching
// pattern is taken and its words are consumed.
func matchKeywords(t *Transcript) map[string]*kwHit {
	ws := words(t)
	hits := map[string]*kwHit{}
	for i := 0; i < len(ws); {
		matched := 0
		for _, p := range kwPatterns {
			if i+len(p.tokens) > len(ws) {
				continue
			}
			ok := true
			for k, tok := range p.tokens {
				if !tokenMatch(tok, ws[i+k].w) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			key := p.kind + ":" + p.slug
			if h, found := hits[key]; found {
				h.count++
			} else {
				hits[key] = &kwHit{count: 1, first: ws[i].at}
			}
			matched = len(p.tokens)
			break
		}
		i += max(matched, 1)
	}
	return hits
}

func (KeywordStep) Run(_ context.Context, sc *StepContext) (*StepOutput, error) {
	if sc.Transcript.Empty() {
		return nil, skipf("no transcript")
	}
	hits := matchKeywords(sc.Transcript)

	res := Result{}
	focusScore := map[string]int{}
	areaScore := map[string]int{}
	for key, h := range hits {
		kind, slug, _ := strings.Cut(key, ":")
		switch kind {
		case "pose":
			def, _ := PoseBySlug(slug)
			res.Poses = append(res.Poses, Pose{
				Slug: def.Slug, Name: def.RU, NameEN: def.EN, Sanskrit: def.Sanskrit,
				StartSeconds: int(h.first), BodyAreas: def.BodyAreas,
			})
			// A named pose is good evidence for its focus; its body areas are
			// weaker evidence (most poses touch several areas).
			for _, f := range def.Focus {
				focusScore[f] += 2
			}
			for _, a := range def.BodyAreas {
				areaScore[a]++
			}
		case "focus":
			focusScore[slug] += 2 * h.count
			if area, ok := focusImpliesArea[slug]; ok {
				areaScore[area] += 3 * h.count
			}
		case "body_area":
			// Naming a body part outright is stronger evidence than inferring
			// it from the poses practised.
			areaScore[slug] += 3 * h.count
		}
	}
	res.Focus = scoreToEmphasis(focusScore)
	res.BodyAreas = scoreToEmphasis(areaScore)
	note := fmt.Sprintf("%d poses, %d focus, %d body areas", len(res.Poses), len(res.Focus), len(res.BodyAreas))
	return &StepOutput{Result: &res, Mode: Union, Note: note}, nil
}

// scoreToEmphasis keeps items with score ≥ 2 (an explicit mention, or a
// named pose for focus / two poses for body areas). Up to three items with
// score ≥ 4 and at least 75% of the top score are marked primary.
func scoreToEmphasis(scores map[string]int) []Emphasis {
	top := 0
	for _, s := range scores {
		top = max(top, s)
	}
	var out []Emphasis
	for slug, s := range scores {
		if s >= 2 {
			out = append(out, Emphasis{Slug: slug})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if scores[out[i].Slug] != scores[out[j].Slug] {
			return scores[out[i].Slug] > scores[out[j].Slug]
		}
		return out[i].Slug < out[j].Slug
	})
	for i := range out {
		s := scores[out[i].Slug]
		if i < 3 && s >= 4 && s*4 >= top*3 {
			out[i].Primary = true
		}
	}
	return out
}
