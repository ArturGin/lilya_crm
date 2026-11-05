package analysis

import (
	"context"
	"testing"
)

// Real output of faster-whisper-small on a Russian TTS recording (segment
// boundaries split phrases: "теперь / собака мордой вниз").
var whisperSample = &Transcript{Segments: []Segment{
	{0.0, 3.6, "Добрый вечер, сегодня практика на раскрытии тазобедренных"},
	{3.6, 7.5, "суставов и мягкие прогибы. Начинаем в позе ребенка, теперь"},
	{7.5, 11.3, "собака мордой вниз, вытягиваем позвоночник, переходим"},
	{11.3, 14.9, "в позу голубя, правая нога вперед, отлично, теперь"},
	{14.9, 19.2, "позу кобры, раскрываем грудной отдел, и завершаем в шавасане."},
}, Text: "x"}

func TestKeywordStepOnWhisperOutput(t *testing.T) {
	out, err := KeywordStep{}.Run(context.Background(), &StepContext{Transcript: whisperSample})
	if err != nil {
		t.Fatal(err)
	}
	r := *out.Result
	r.Normalize()
	got := map[string]int{}
	for _, p := range r.Poses {
		got[p.Slug] = p.StartSeconds
	}
	want := map[string]int{"childs-pose": 5, "downward-facing-dog": 7, "pigeon": 11, "cobra": 14, "corpse": 18}
	for slug, at := range want {
		g, ok := got[slug]
		if !ok {
			t.Errorf("pose %s not found (got %v)", slug, got)
			continue
		}
		if g < at-2 || g > at+2 {
			t.Errorf("pose %s at %ds, want ~%ds", slug, g, at)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected poses: %v", got)
	}
	focus := map[string]bool{}
	for _, f := range r.Focus {
		focus[f.Slug] = true
	}
	for _, f := range []string{"hip-opening", "backbends", "chest-opening"} {
		if !focus[f] {
			t.Errorf("focus %s missing (got %+v)", f, r.Focus)
		}
	}
}

func TestKeywordLongestMatchWins(t *testing.T) {
	tr := &Transcript{Text: "Боковая планка, потом поза счастливого ребёнка и лежачая бабочка"}
	hits := matchKeywords(tr)
	for _, k := range []string{"pose:side-plank", "pose:happy-baby", "pose:reclined-bound-angle"} {
		if hits[k] == nil {
			t.Errorf("missing %s in %v", k, keys(hits))
		}
	}
	for _, k := range []string{"pose:plank", "pose:childs-pose", "pose:butterfly"} {
		if hits[k] != nil {
			t.Errorf("unexpected %s", k)
		}
	}
}

func TestKeywordSkipsWithoutTranscript(t *testing.T) {
	_, err := KeywordStep{}.Run(context.Background(), &StepContext{})
	if err == nil || !isSkip(err) {
		t.Fatalf("expected skip, got %v", err)
	}
}

func keys(m map[string]*kwHit) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Real faster-whisper-small output for a narrated test class, including its
// recognition errors ("лежи", "швасане") and a negated mention (headstand).
var whisperClass = &Transcript{Text: "x", Segments: []Segment{
	{0, 2, "Добро пожаловать на практику!"},
	{2, 7, "Сегодня мы работаем с раскрытием тазобедренных суставов и мягкими прогибами."},
	{7, 12, "Садимся в позу бабочки, стопы вместе, колени в стороны, дышим спокойно."},
	{12, 16, "Теперь переходим на четвереньки, несколько раз кошка корова."},
	{16, 20, "Из этого положения поднимаем таз собаку мордой вниз."},
	{20, 24, "Шаг правой ногой вперед, низкий выпад, раскрываем сгибатели бедра,"},
	{24, 27, "опускаем колено и переходим в позу голубя."},
	{27, 31, "Остаёмся здесь 10 дыханий, раскрытие тазобедренного сустава,"},
	{31, 37, "ложимся на живот, поза сфинкса, затем кобра, раскрываем грудной отдел,"},
	{37, 41, "прогиб без напряжения в пояснице, отдыхаем в позе ребёнка,"},
	{41, 46, "ложимся на спину, мост, поднимаем таз, стойку на голове сегодня не делаем,"},
	{46, 50, "скрутка лежи на обе стороны и завершаем практику в швасане."},
}}

func TestKeywordStepOnNarratedClass(t *testing.T) {
	out, err := KeywordStep{}.Run(context.Background(), &StepContext{Transcript: whisperClass})
	if err != nil {
		t.Fatal(err)
	}
	r := *out.Result
	r.Normalize()
	poses := map[string]bool{}
	for _, p := range r.Poses {
		poses[p.Slug] = true
	}
	for _, slug := range []string{"butterfly", "cat-cow", "downward-facing-dog", "low-lunge", "pigeon", "sphinx",
		"cobra", "childs-pose", "bridge", "supine-twist", "corpse"} {
		if !poses[slug] {
			t.Errorf("missing pose %s", slug)
		}
	}
	// Keyword matching cannot understand negation: documented limitation,
	// resolved by a refining step such as Claude.
	if !poses["headstand"] {
		t.Log("headstand no longer reported (negation handling improved?)")
	}
	var primaryFocus, primaryAreas []string
	for _, f := range r.Focus {
		if f.Primary {
			primaryFocus = append(primaryFocus, f.Slug)
		}
	}
	for _, a := range r.BodyAreas {
		if a.Primary {
			primaryAreas = append(primaryAreas, a.Slug)
		}
	}
	if len(primaryFocus) != 2 || !contains(primaryFocus, "hip-opening") || !contains(primaryFocus, "backbends") {
		t.Errorf("primary focus = %v, want hip-opening + backbends", primaryFocus)
	}
	if len(primaryAreas) != 1 || primaryAreas[0] != "hips" {
		t.Errorf("primary body areas = %v, want [hips]", primaryAreas)
	}
	if len(r.BodyAreas) > 8 {
		t.Errorf("too many body areas (%d): %+v", len(r.BodyAreas), r.BodyAreas)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
