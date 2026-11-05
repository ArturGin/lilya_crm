// Package pipeline implements the background steps of a video's life:
// upload → process (probe, make web-playable, poster) → analyse → tag.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lilya/practice/internal/analysis"
	"github.com/lilya/practice/internal/media"
	"github.com/lilya/practice/internal/storage"
	"github.com/lilya/practice/internal/store"
	"github.com/lilya/practice/internal/worker"
)

const (
	JobProcessVideo = "video.process"
	JobAnalyzeVideo = "video.analyze"
)

type ProcessPayload struct {
	AssetID int64 `json:"asset_id"`
}

type AnalyzePayload struct {
	AnalysisID int64 `json:"analysis_id"`
}

type Pipeline struct {
	Store    *store.Store
	Storage  storage.Storage
	FFmpeg   media.FFmpeg
	Analysis *analysis.Runner // configured analysis steps (may have none)
	Worker   *worker.Worker
	Log      *slog.Logger
	TempDir  string
	Lang     string
	Notify   Notifier // optional
}

func (p *Pipeline) Register() {
	p.Worker.Register(JobProcessVideo, processHandler{p})
	p.Worker.Register(JobAnalyzeVideo, analyzeHandler{p})
}

// ErrAnalysisDisabled is returned by StartAnalysis when no step is configured.
var ErrAnalysisDisabled = errors.New("video analysis is not configured")

// StartAnalysis records a pending analysis for the post's current video and
// queues it. steps selects analysis steps (nil = configured defaults).
func (p *Pipeline) StartAnalysis(ctx context.Context, postID, assetID int64, steps []string) (store.Analysis, error) {
	if !p.Analysis.Enabled() {
		p.Log.Info("analysis skipped: no analysis steps configured", "post_id", postID, "asset_id", assetID)
		msg := "analysis disabled: no analysis steps configured"
		a, err := p.Store.CreateAnalysis(ctx, postID, assetID, store.AnalysisSkipped, "", "", nil, &msg)
		if err != nil {
			return a, err
		}
		return a, ErrAnalysisDisabled
	}
	resolved, err := p.Analysis.Resolve(steps)
	if err != nil {
		return store.Analysis{}, err
	}
	a, err := p.Store.CreateAnalysis(ctx, postID, assetID, store.AnalysisPending, "pipeline", stepLabel(resolved), steps, nil)
	if err != nil {
		return a, err
	}
	if err := p.Worker.Enqueue(ctx, JobAnalyzeVideo, AnalyzePayload{AnalysisID: a.ID}); err != nil {
		return a, err
	}
	p.Log.Info("analysis queued", "analysis_id", a.ID, "post_id", postID, "asset_id", assetID,
		"requested_steps", steps, "steps", stepLabel(resolved))
	return a, nil
}

// stepLabel renders e.g. "transcribe(whisper-1) → keywords → claude(claude-opus-5-5)".
func stepLabel(steps []analysis.Step) string {
	var parts []string
	for _, s := range steps {
		in := s.Info()
		if in.Model != "" {
			parts = append(parts, in.Name+"("+in.Model+")")
		} else {
			parts = append(parts, in.Name)
		}
	}
	return strings.Join(parts, " → ")
}

func permanent(err error) error { return fmt.Errorf("%w: %v", worker.ErrPermanent, err) }

// ---- video.process ----

type processHandler struct{ p *Pipeline }

func (h processHandler) Handle(ctx context.Context, raw json.RawMessage) error {
	p := h.p
	var in ProcessPayload
	if err := json.Unmarshal(raw, &in); err != nil {
		return permanent(err)
	}
	asset, err := p.Store.AssetByID(ctx, in.AssetID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // deleted meanwhile
	}
	if err != nil {
		return err
	}
	log := p.Log.With("asset_id", asset.ID)
	start := time.Now()
	if err := p.Store.SetAssetStatus(ctx, asset.ID, store.AssetProcessing, nil); err != nil {
		return err
	}

	src, release, err := p.Storage.LocalPath(ctx, asset.StorageKey)
	if err != nil {
		return permanent(fmt.Errorf("original upload missing: %w", err))
	}
	defer release()
	log.Info("video processing started", "file", asset.OriginalFilename, "path", src, "size_mb", mb(asset.SizeBytes))

	probe, err := p.FFmpeg.Probe(ctx, src)
	if err != nil {
		return permanent(fmt.Errorf("not a readable video file: %w", err))
	}
	if !probe.HasVideo || probe.DurationSeconds <= 0 {
		return permanent(errors.New("file has no video stream"))
	}
	mode := "transcode"
	if probe.CanRemux() {
		mode = "remux"
	}
	log.Info("video probed", "mode", mode, "duration", fmtDur(probe.DurationSeconds),
		"width", probe.Width, "height", probe.Height, "video_codec", probe.VideoCodec,
		"audio_codec", probe.AudioCodec, "container", probe.FormatName)

	tmp := filepath.Join(p.TempDir, "play-"+storage.NewID()+".mp4")
	defer os.Remove(tmp)
	encStart := time.Now()
	if err := p.FFmpeg.MakePlayable(ctx, src, tmp, probe, progressLogger(log, "make playable "+mode, probe.DurationSeconds)); err != nil {
		return err
	}
	dir := path.Dir(asset.StorageKey)
	playKey := dir + "/play.mp4"
	size, err := p.Storage.PutFile(ctx, playKey, tmp)
	if err != nil {
		return err
	}
	log.Info("playable video stored", "mode", mode, "key", playKey, "size_mb", mb(size),
		"took", time.Since(encStart).Round(time.Second).String())

	// Poster frame (~10% in, capped at 30 s, to skip black intros).
	playPath, releasePlay, err := p.Storage.LocalPath(ctx, playKey)
	if err != nil {
		return err
	}
	defer releasePlay()
	var posterID *int64
	at := math.Min(probe.DurationSeconds*0.1, 30)
	if jpg, err := p.FFmpeg.Frame(ctx, playPath, at, 1280); err == nil && len(jpg) > 0 {
		key := fmt.Sprintf("%s/poster-%s.jpg", dir, storage.NewID())
		if n, err := p.Storage.Put(ctx, key, bytesReader(jpg)); err == nil {
			poster, err := p.Store.CreateAsset(ctx, store.NewAsset{
				Kind: store.AssetImage, StorageKey: key, ContentType: "image/jpeg",
				SizeBytes: n, Status: store.AssetReady, OriginalFilename: "poster.jpg",
			})
			if err != nil {
				return err
			}
			posterID = &poster.ID
		}
	} else if err != nil {
		log.Warn("poster extraction failed", "err", err)
	}

	if err := p.Store.MarkVideoReady(ctx, asset.ID, store.ProcessedVideo{
		PlaybackKey:     playKey,
		SizeBytes:       size,
		DurationSeconds: int(math.Round(probe.DurationSeconds)),
		Width:           probe.Width,
		Height:          probe.Height,
		PosterAssetID:   posterID,
	}); err != nil {
		return err
	}
	if asset.PosterAssetID != nil { // re-processing: drop the previous poster
		p.deleteAssets(ctx, []int64{*asset.PosterAssetID})
	}

	postIDs, err := p.Store.PostIDsByVideo(ctx, asset.ID)
	if err != nil {
		return err
	}
	log.Info("video ready", "post_ids", postIDs, "took", time.Since(start).Round(time.Second).String())
	queued := false
	for _, postID := range postIDs {
		_, err := p.StartAnalysis(ctx, postID, asset.ID, nil)
		if err != nil && !errors.Is(err, ErrAnalysisDisabled) {
			return err
		}
		queued = queued || err == nil
	}
	notice := VideoNotice{AssetID: asset.ID, PostIDs: postIDs, OK: true, AnalysisQueued: queued,
		DurationSeconds: int(math.Round(probe.DurationSeconds)), Width: probe.Width, Height: probe.Height}
	p.notify(func(ctx context.Context, n Notifier) { n.VideoProcessed(ctx, notice) })
	return nil
}

func (h processHandler) OnFinalFailure(ctx context.Context, raw json.RawMessage, err error) {
	var in ProcessPayload
	if json.Unmarshal(raw, &in) == nil {
		h.p.Log.Error("video processing failed", "asset_id", in.AssetID, "err", err)
		msg := err.Error()
		_ = h.p.Store.SetAssetStatus(ctx, in.AssetID, store.AssetFailed, &msg)
		postIDs, _ := h.p.Store.PostIDsByVideo(ctx, in.AssetID)
		notice := VideoNotice{AssetID: in.AssetID, PostIDs: postIDs, Error: msg}
		h.p.notify(func(ctx context.Context, n Notifier) { n.VideoProcessed(ctx, notice) })
	}
}

// DeleteAssets removes asset rows (and their posters) plus stored files.
func (p *Pipeline) DeleteAssets(ctx context.Context, ids []int64) { p.deleteAssets(ctx, ids) }

func (p *Pipeline) deleteAssets(ctx context.Context, ids []int64) {
	deleted, err := p.Store.DeleteAssets(ctx, ids)
	if err != nil {
		p.Log.Error("delete assets", "ids", ids, "err", err)
		return
	}
	for _, a := range deleted {
		keys := []string{a.StorageKey}
		if a.PlaybackKey != nil {
			keys = append(keys, *a.PlaybackKey)
		}
		for _, k := range keys {
			if err := p.Storage.Delete(ctx, k); err != nil {
				p.Log.Warn("delete stored file", "key", k, "err", err)
			}
		}
	}
}

// ---- video.analyze ----

type analyzeHandler struct{ p *Pipeline }

func (h analyzeHandler) Handle(ctx context.Context, raw json.RawMessage) error {
	p := h.p
	var in AnalyzePayload
	if err := json.Unmarshal(raw, &in); err != nil {
		return permanent(err)
	}
	a, err := p.Store.AnalysisByID(ctx, in.AnalysisID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if a.Status == store.AnalysisDone {
		return nil
	}
	if !p.Analysis.Enabled() {
		return permanent(ErrAnalysisDisabled)
	}
	steps, err := p.Analysis.Resolve(a.RequestedSteps)
	if err != nil {
		return permanent(err)
	}
	post, err := p.Store.GetPost(ctx, a.PostID, 0)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if post.Video == nil || post.Video.ID != a.AssetID {
		return permanent(errors.New("superseded: the post's video was replaced"))
	}
	asset := post.Video
	if asset.PlaybackKey == nil || asset.DurationSeconds == nil {
		return permanent(errors.New("video is not processed yet"))
	}
	if err := p.Store.SetAnalysisRunning(ctx, a.ID, stepLabel(steps)); err != nil {
		return err
	}

	src, release, err := p.Storage.LocalPath(ctx, *asset.PlaybackKey)
	if err != nil {
		return permanent(err)
	}
	defer release()
	log := p.Log.With("analysis_id", a.ID, "post_id", post.ID, "asset_id", asset.ID)
	start := time.Now()
	log.Info("analysis started", "steps", stepLabel(steps), "path", src,
		"duration", fmtDur(float64(*asset.DurationSeconds)))

	sc := &analysis.StepContext{
		AssetID:         asset.ID,
		Title:           post.Title,
		Description:     post.Body,
		DurationSeconds: *asset.DurationSeconds,
		Lang:            p.Lang,
		VideoPath:       src,
		Media:           p.FFmpeg,
		TempDir:         p.TempDir,
		Cache:           transcriptCache{p.Store},
		Log:             log,
	}
	progress := func(recs []analysis.StepRecord) {
		if b, err := json.Marshal(recs); err == nil {
			_ = p.Store.SetAnalysisSteps(context.Background(), a.ID, b)
		}
	}
	res, records, err := p.Analysis.Run(ctx, steps, sc, progress)
	if err != nil {
		if errors.Is(err, analysis.ErrPermanent) {
			return permanent(err)
		}
		return err
	}

	resJSON, err := json.Marshal(res)
	if err != nil {
		return permanent(err)
	}
	recJSON, _ := json.Marshal(records)
	var inTok, outTok int
	for _, r := range records {
		inTok += r.Usage.InputTokens
		outTok += r.Usage.OutputTokens
	}
	search := res.SearchText()
	if !sc.Transcript.Empty() {
		search += " \n" + truncateRunes(sc.Transcript.Text, 200_000)
	}
	if err := p.Store.CompleteAnalysis(ctx, a.ID, resJSON, recJSON, search, inTok, outTok); err != nil {
		return err
	}
	tags := res.Tags(p.Lang)
	if err := p.Store.ReplaceAITags(ctx, post.ID, tags); err != nil {
		return err
	}
	took := time.Since(start)
	log.Info("analysis done", "tags", len(tags), "poses", len(res.Poses), "input_tokens", inTok,
		"output_tokens", outTok, "took", took.Round(time.Second).String())
	notice := AnalysisNotice{AnalysisID: a.ID, PostID: post.ID, OK: true, Result: &res, Took: took}
	p.notify(func(ctx context.Context, n Notifier) { n.AnalysisFinished(ctx, notice) })
	return nil
}

func (h analyzeHandler) OnFinalFailure(ctx context.Context, raw json.RawMessage, err error) {
	var in AnalyzePayload
	if json.Unmarshal(raw, &in) == nil {
		h.p.Log.Error("analysis failed", "analysis_id", in.AnalysisID, "err", err)
		_ = h.p.Store.FailAnalysis(ctx, in.AnalysisID, err.Error())
		if a, aerr := h.p.Store.AnalysisByID(ctx, in.AnalysisID); aerr == nil {
			notice := AnalysisNotice{AnalysisID: a.ID, PostID: a.PostID, Error: err.Error()}
			h.p.notify(func(ctx context.Context, n Notifier) { n.AnalysisFinished(ctx, notice) })
		}
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// transcriptCache adapts the store to analysis.TranscriptCache.
type transcriptCache struct{ st *store.Store }

func (c transcriptCache) GetTranscript(ctx context.Context, assetID int64, provider, model string) (*analysis.Transcript, error) {
	t, err := c.st.GetTranscript(ctx, assetID, provider, model)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &analysis.Transcript{Provider: t.Provider, Model: t.Model, Language: t.Language, Text: t.Text}
	_ = json.Unmarshal(t.Segments, &out.Segments)
	return out, nil
}

func (c transcriptCache) PutTranscript(ctx context.Context, assetID int64, t *analysis.Transcript) error {
	seg, err := json.Marshal(t.Segments)
	if err != nil {
		return err
	}
	if t.Segments == nil {
		seg = []byte("[]")
	}
	return c.st.PutTranscript(ctx, store.Transcript{AssetID: assetID, Provider: t.Provider, Model: t.Model,
		Language: t.Language, Text: t.Text, Segments: seg})
}
