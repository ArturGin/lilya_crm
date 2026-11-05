package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lilya/practice/internal/analysis"
	"github.com/lilya/practice/internal/db"
	"github.com/lilya/practice/internal/media"
	"github.com/lilya/practice/internal/storage"
	"github.com/lilya/practice/internal/store"
	"github.com/lilya/practice/internal/worker"
)

// fakeTranscriber returns a fixed transcript and counts calls.
type fakeTranscriber struct{ calls int }

func (f *fakeTranscriber) Provider() string { return "fake" }
func (f *fakeTranscriber) Model() string    { return "fake-whisper" }
func (f *fakeTranscriber) Transcribe(_ context.Context, path, _ string) (*analysis.Transcript, error) {
	f.calls++
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		return nil, fmt.Errorf("audio chunk missing: %v", err)
	}
	return &analysis.Transcript{Language: "ru", Text: "Начинаем в позе голубя, раскрываем тазобедренные суставы. Потом капотасана.",
		Segments: []analysis.Segment{{Start: 2, End: 6, Text: "Начинаем в позе голубя, раскрываем тазобедренные суставы."}, {Start: 6, End: 8, Text: "Потом капотасана."}}}, nil
}

// fakeLLM refines earlier findings (like the Claude step) and records what it saw.
type fakeLLM struct {
	sawTranscript bool
	frames        int
}

func (f *fakeLLM) Info() analysis.StepInfo { return analysis.StepInfo{Name: "llm", Provider: "fake"} }
func (f *fakeLLM) Run(ctx context.Context, sc *analysis.StepContext) (*analysis.StepOutput, error) {
	f.sawTranscript = !sc.Transcript.Empty()
	frames, err := sc.Frames(ctx, 10, 320)
	if err != nil {
		return nil, err
	}
	f.frames = len(frames)
	r := analysis.Result{
		Summary:   "Мягкая практика на раскрытие тазобедренных суставов",
		Level:     "beginner",
		Focus:     []analysis.Emphasis{{Slug: "hip-opening", Primary: true}, {Slug: "backbends"}},
		BodyAreas: []analysis.Emphasis{{Slug: "hips", Primary: true}},
		Poses:     append([]analysis.Pose{}, sc.Current.Poses...),
	}
	return &analysis.StepOutput{Result: &r, Mode: analysis.Refine, Usage: analysis.Usage{InputTokens: 100, OutputTokens: 10}}, nil
}

// Requires TEST_DATABASE_URL (an empty, disposable database) and ffmpeg.
func TestProcessAndAnalyze(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)

	dir := t.TempDir()
	local, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "in.mov")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=300", "-t", "20", "-c:v", "mpeg4", "-c:a", "aac", src).CombinedOutput(); err != nil {
		t.Fatalf("make test video: %v %s", err, out)
	}

	tr := &fakeTranscriber{}
	llm := &fakeLLM{}
	runner, err := analysis.NewRunner([]analysis.Step{
		&analysis.TranscribeStep{T: tr, Language: "ru", ChunkSeconds: 15}, // 20 s video → 2 chunks
		analysis.KeywordStep{},
		llm,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{
		Store: st, Storage: local, FFmpeg: media.FFmpeg{FFmpegPath: "ffmpeg", FFprobePath: "ffprobe"},
		Analysis: runner, Worker: worker.New(st, log, 1), Log: log, TempDir: local.TempDir(), Lang: "ru",
	}

	admin, err := st.CreateUser(ctx, "pipeline-test-"+storage.NewID()+"@example.com", "", store.RoleAdmin, "x")
	if err != nil {
		t.Fatal(err)
	}
	postID, err := st.CreatePost(ctx, "Тестовая практика", "", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(src)
	key := "videos/" + storage.NewID() + "/original.mov"
	size, err := local.Put(ctx, key, f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	asset, _, err := st.AttachVideo(ctx, postID, store.NewAsset{Kind: store.AssetVideo, StorageKey: key, SizeBytes: size, Status: store.AssetUploaded})
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(ProcessPayload{AssetID: asset.ID})
	if err := (processHandler{p}).Handle(ctx, payload); err != nil {
		t.Fatalf("process: %v", err)
	}
	post, err := st.GetPost(ctx, postID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if post.Video.Status != store.AssetReady || post.DurationSeconds == nil || *post.DurationSeconds != 20 || post.Video.PosterAssetID == nil {
		t.Fatalf("video not ready: %+v duration=%v", post.Video, post.DurationSeconds)
	}
	if post.Analysis == nil || post.Analysis.Status != store.AnalysisPending {
		t.Fatalf("analysis not queued: %+v", post.Analysis)
	}

	payload, _ = json.Marshal(AnalyzePayload{AnalysisID: post.Analysis.ID})
	if err := (analyzeHandler{p}).Handle(ctx, payload); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if llm.frames != 8 || !llm.sawTranscript { // 20 s video → minimum of 8 frames
		t.Errorf("llm saw frames=%d transcript=%v", llm.frames, llm.sawTranscript)
	}
	if tr.calls != 2 {
		t.Errorf("expected 2 transcription chunks, got %d", tr.calls)
	}
	post, _ = st.GetPost(ctx, postID, 0)
	if post.Analysis.Status != store.AnalysisDone || len(post.Tags) != 5 {
		t.Fatalf("analysis/tags not stored: status=%s tags=%+v", post.Analysis.Status, post.Tags)
	}
	var recs []analysis.StepRecord
	_ = json.Unmarshal(post.Analysis.Steps, &recs)
	if len(recs) != 3 || recs[0].Status != "done" || recs[1].Status != "done" || recs[2].Status != "done" {
		t.Fatalf("step records: %+v", recs)
	}
	for _, tg := range post.Tags {
		if tg.Kind == store.TagPose && (tg.Slug != "pigeon" || tg.Origin != "keywords,llm") {
			t.Errorf("pose tag %+v: want pigeon from keywords,llm", tg)
		}
	}
	// Spoken words are searchable via the stored transcript.
	_ = st.UpdatePost(ctx, postID, store.PostUpdate{Status: ptr(store.PostPublished)})
	if _, total, _ := st.ListPosts(ctx, store.ListFilter{Query: "начинаем", Statuses: []string{store.PostPublished}}); total < 1 {
		t.Error("transcript text not searchable")
	}

	// A second run reuses the cached transcript.
	a2, err := p.StartAnalysis(ctx, postID, asset.ID, []string{"transcribe", "keywords"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(AnalyzePayload{AnalysisID: a2.ID})
	if err := (analyzeHandler{p}).Handle(ctx, payload); err != nil {
		t.Fatalf("re-analyze: %v", err)
	}
	if tr.calls != 2 {
		t.Errorf("transcript not cached: %d calls", tr.calls)
	}

	// Manual tags survive a re-analysis; AI tags are replaced.
	post, _ = st.GetPost(ctx, postID, 0)
	manual := []store.TagInput{{Kind: store.TagOther, Slug: "утро", Name: "Утро"}}
	for _, tg := range post.Tags {
		manual = append(manual, store.TagInput{Kind: tg.Kind, Slug: tg.Slug, Name: tg.Name, Primary: tg.Primary})
	}
	if err := st.SetPostTags(ctx, postID, manual); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAITags(ctx, postID, nil); err != nil {
		t.Fatal(err)
	}
	post, _ = st.GetPost(ctx, postID, 0)
	if len(post.Tags) != 1 || post.Tags[0].Source != store.TagSourceManual {
		t.Fatalf("expected only the manual tag left, got %+v", post.Tags)
	}

	// Search hits the dictionary's English and Sanskrit names of detected poses.
	for _, q := range []string{"rajakapotasana", "pigeon"} {
		items, total, err := st.ListPosts(ctx, store.ListFilter{Query: q, Statuses: []string{store.PostPublished}})
		if err != nil || total < 1 || !containsPost(items, postID) {
			t.Fatalf("search %q failed: total=%d err=%v", q, total, err)
		}
	}

	_ = st.DeletePost(ctx, postID)
}

func ptr[T any](v T) *T { return &v }

func containsPost(items []store.PostCard, id int64) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}
