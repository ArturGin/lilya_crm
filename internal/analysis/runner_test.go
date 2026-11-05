package analysis

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type stubStep struct {
	name string
	out  *StepOutput
	err  error
	seen *Result
}

func (s *stubStep) Info() StepInfo { return StepInfo{Name: s.name, Provider: "test"} }
func (s *stubStep) Run(_ context.Context, sc *StepContext) (*StepOutput, error) {
	cur := sc.Current
	s.seen = &cur
	return s.out, s.err
}

func isSkip(err error) bool { return errors.Is(err, ErrSkip) }

func TestRunnerMergeAndRefine(t *testing.T) {
	kw := &stubStep{name: "keywords", out: &StepOutput{Mode: Union, Result: &Result{
		Poses: []Pose{{Slug: "pigeon", StartSeconds: 30}, {Slug: "headstand", StartSeconds: 50}},
		Focus: []Emphasis{{Slug: "hip-opening"}},
	}}}
	broken := &stubStep{name: "vision", err: fmt.Errorf("%w: bad key", ErrPermanent)}
	skip := &stubStep{name: "other", err: skipf("nothing to do")}
	llm := &stubStep{name: "llm", out: &StepOutput{Mode: Refine, Result: &Result{
		Summary: "s",
		Poses:   []Pose{{Slug: "pigeon-pose", StartSeconds: 40}, {Slug: "cobra", StartSeconds: 60}},
		Focus:   []Emphasis{{Slug: "hip-opening", Primary: true}},
	}}}
	r, _ := NewRunner([]Step{kw, broken, skip, llm}, nil)
	steps, _ := r.Resolve(nil)
	res, recs, err := r.Run(context.Background(), steps, &StepContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recs[1].Status != "failed" || recs[2].Status != "skipped" || recs[2].Note != "nothing to do" || recs[3].Status != "done" {
		t.Fatalf("records: %+v", recs)
	}
	if llm.seen == nil || len(llm.seen.Poses) != 2 {
		t.Fatalf("llm did not see keyword hints: %+v", llm.seen)
	}
	// headstand dropped by refine; pigeon alias canonicalised, keeps keyword provenance and earliest time.
	if len(res.Poses) != 2 || res.Poses[0].Slug != "pigeon" || res.Poses[0].StartSeconds != 30 {
		t.Fatalf("poses: %+v", res.Poses)
	}
	if got := res.Poses[0].Sources; len(got) != 2 || got[0] != "keywords" || got[1] != "llm" {
		t.Errorf("pigeon sources = %v", got)
	}
	if got := res.Poses[1].Sources; len(got) != 1 || got[0] != "llm" {
		t.Errorf("cobra sources = %v", got)
	}
	if !res.Focus[0].Primary || len(res.Focus[0].Sources) != 2 {
		t.Errorf("focus: %+v", res.Focus)
	}
}

func TestRunnerTransientAborts(t *testing.T) {
	a := &stubStep{name: "a", err: errors.New("503")}
	b := &stubStep{name: "b", out: &StepOutput{}}
	r, _ := NewRunner([]Step{a, b}, nil)
	steps, _ := r.Resolve(nil)
	_, _, err := r.Run(context.Background(), steps, &StepContext{}, nil)
	if err == nil || errors.Is(err, ErrPermanent) || b.seen != nil {
		t.Fatalf("expected transient abort before b, got %v", err)
	}
}

func TestRunnerAllFail(t *testing.T) {
	a := &stubStep{name: "a", err: fmt.Errorf("%w: x", ErrPermanent)}
	r, _ := NewRunner([]Step{a}, nil)
	steps, _ := r.Resolve(nil)
	if _, _, err := r.Run(context.Background(), steps, &StepContext{}, nil); !errors.Is(err, ErrPermanent) {
		t.Fatalf("expected permanent failure, got %v", err)
	}
}

func TestRunnerSelection(t *testing.T) {
	a, b := &stubStep{name: "a"}, &stubStep{name: "b"}
	if _, err := NewRunner([]Step{a, b}, []string{"c"}); err == nil {
		t.Fatal("unknown default accepted")
	}
	r, _ := NewRunner([]Step{a, b}, []string{"b"})
	steps, _ := r.Resolve(nil)
	if len(steps) != 1 || steps[0].Info().Name != "b" {
		t.Fatal("defaults not applied")
	}
	steps, _ = r.Resolve([]string{"b", "a"}) // execution order is registration order
	if steps[0].Info().Name != "a" {
		t.Fatal("order not preserved")
	}
	if _, err := r.Resolve([]string{"zzz"}); err == nil {
		t.Fatal("unknown step accepted")
	}
}
