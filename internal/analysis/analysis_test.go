package analysis

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/lilya/practice/internal/store"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Downward-Facing Dog": "downward-facing-dog",
		"  Pigeon (left) ":    "pigeon-left",
		"Голубь":              "голубь",
		"---":                 "",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNormalizeAndTags(t *testing.T) {
	r := Result{
		Level:     "nonsense",
		Focus:     []Emphasis{{Slug: "hip-opening"}, {Slug: "made-up", Primary: true}, {Slug: "hip-opening", Primary: true}},
		BodyAreas: []Emphasis{{Slug: "hips", Primary: true}},
		Poses: []Pose{
			{Slug: "Pigeon", Name: "Голубь", NameEN: "Pigeon", StartSeconds: 300, BodyAreas: []string{"hips", "bogus"}},
			{Slug: "", NameEN: "Child's Pose", Name: "Поза ребёнка", StartSeconds: 10},
			{Slug: "pigeon", Name: "dup", StartSeconds: 400},
		},
	}
	r.Normalize()
	if r.Level != "" {
		t.Errorf("unknown level kept: %q", r.Level)
	}
	if len(r.Focus) != 1 || !r.Focus[0].Primary {
		t.Errorf("focus not deduped/merged: %+v", r.Focus)
	}
	if len(r.Poses) != 2 || r.Poses[0].Slug != "childs-pose" || r.Poses[1].Slug != "pigeon" {
		t.Errorf("poses not normalised/sorted: %+v", r.Poses)
	}
	if len(r.Poses[1].BodyAreas) != 1 {
		t.Errorf("unknown body area kept: %v", r.Poses[1].BodyAreas)
	}
	tags := r.Tags("ru")
	kinds := map[string]int{}
	for _, tg := range tags {
		kinds[tg.Kind]++
		if tg.Name == "" || tg.Slug == "" {
			t.Errorf("empty tag %+v", tg)
		}
	}
	if kinds[store.TagFocus] != 1 || kinds[store.TagBodyArea] != 1 || kinds[store.TagPose] != 2 {
		t.Errorf("unexpected tags %+v", tags)
	}
}

// Structured outputs require every object to list all properties as required
// and to forbid additional properties.
func TestSchemaIsStrict(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(resultSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	var walk func(path string, node map[string]any)
	walk = func(path string, node map[string]any) {
		switch node["type"] {
		case "object":
			if node["additionalProperties"] != false {
				t.Errorf("%s: additionalProperties must be false", path)
			}
			props := node["properties"].(map[string]any)
			var keys, req []string
			for k, v := range props {
				keys = append(keys, k)
				walk(path+"."+k, v.(map[string]any))
			}
			for _, r := range node["required"].([]any) {
				req = append(req, r.(string))
			}
			sort.Strings(keys)
			sort.Strings(req)
			if len(keys) != len(req) {
				t.Errorf("%s: required %v != properties %v", path, req, keys)
			}
		case "array":
			walk(path+"[]", node["items"].(map[string]any))
		}
	}
	walk("$", schema)

	// Result must round-trip with the schema's field names.
	var r Result
	if err := json.Unmarshal([]byte(`{"summary":"s","practice_type":"yin","level":"beginner","intensity":"low",
		"focus":[{"slug":"twists","primary":true}],"body_areas":[],"poses":[{"slug":"cobra","name":"Кобра",
		"name_en":"Cobra","sanskrit":"Bhujangasana","start_seconds":5,"body_areas":["spine"]}],"props":[],"cautions":""}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Poses[0].Sanskrit != "Bhujangasana" || r.Focus[0].Slug != "twists" {
		t.Errorf("decode mismatch: %+v", r)
	}
}
