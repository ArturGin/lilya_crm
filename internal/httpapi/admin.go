package httpapi

import (
	"bufio"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/lilya/practice/internal/analysis"
	"github.com/lilya/practice/internal/pipeline"
	"github.com/lilya/practice/internal/storage"
	"github.com/lilya/practice/internal/store"
)

func (s *Server) adminListPosts(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	f, err := parseListFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_filter", err.Error())
		return
	}
	f.UserID = u.ID
	if st := r.URL.Query().Get("status"); st != "" {
		if !store.ValidPostStatus(st) {
			writeError(w, http.StatusBadRequest, "bad_filter", "invalid status")
			return
		}
		f.Statuses = []string{st}
	}
	items, total, err := s.Store.ListPosts(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items, Total: total})
}

type postInput struct {
	Title  *string `json:"title"`
	Body   *string `json:"body"`
	Status *string `json:"status"`
}

func (in *postInput) validate() string {
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		in.Title = &t
		if t == "" || utf8.RuneCountInString(t) > 200 {
			return "title must be 1–200 characters"
		}
	}
	if in.Body != nil && utf8.RuneCountInString(*in.Body) > 20000 {
		return "text is too long (max 20000 characters)"
	}
	if in.Status != nil && !store.ValidPostStatus(*in.Status) {
		return "status must be draft, published or archived"
	}
	return ""
}

func (s *Server) adminCreatePost(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var in postInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Title == nil {
		writeError(w, http.StatusBadRequest, "invalid", "title is required")
		return
	}
	if msg := in.validate(); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid", msg)
		return
	}
	body := ""
	if in.Body != nil {
		body = *in.Body
	}
	id, err := s.Store.CreatePost(r.Context(), *in.Title, body, u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeAdminPost(w, r, id, http.StatusCreated)
}

func (s *Server) adminGetPost(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	s.writeAdminPost(w, r, id, http.StatusOK)
}

func (s *Server) writeAdminPost(w http.ResponseWriter, r *http.Request, id int64, status int) {
	u, _ := userFrom(r.Context())
	p, err := s.Store.GetPost(r.Context(), id, u.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, p)
}

func (s *Server) adminUpdatePost(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var in postInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid", msg)
		return
	}
	err := s.Store.UpdatePost(r.Context(), id, store.PostUpdate{Title: in.Title, Body: in.Body, Status: in.Status})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeAdminPost(w, r, id, http.StatusOK)
}

func (s *Server) adminDeletePost(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	assetIDs, err := s.Store.PostAssetIDs(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.Store.DeletePost(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "post not found")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.Pipeline.DeleteAssets(r.Context(), assetIDs)
	w.WriteHeader(http.StatusNoContent)
}

var videoExts = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".webm": true, ".mkv": true, ".avi": true}

// nextFilePart streams the multipart body up to the part named "file". Other
// small text fields (e.g. "caption") encountered first are collected.
func nextFilePart(r *http.Request, fields map[string]string) (*multipart.Part, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil, err
		}
		if part.FormName() == "file" && part.FileName() != "" {
			return part, nil
		}
		if fields != nil {
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			fields[part.FormName()] = string(b)
		}
		part.Close()
	}
}

func uploadError(s *Server, w http.ResponseWriter, r *http.Request, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "file is too large")
		return
	}
	if errors.Is(err, io.EOF) || errors.Is(err, http.ErrNotMultipart) || errors.Is(err, io.ErrUnexpectedEOF) {
		writeError(w, http.StatusBadRequest, "bad_upload", "expected multipart/form-data with a 'file' field")
		return
	}
	s.internalError(w, r, err)
}

func (s *Server) adminUploadVideo(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.Store.GetPost(r.Context(), id, 0); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "post not found")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.Cfg.MaxVideoBytes+1<<20)
	part, err := nextFilePart(r, nil)
	if err != nil {
		uploadError(s, w, r, err)
		return
	}
	defer part.Close()
	filename := filepath.Base(part.FileName())
	ext := strings.ToLower(filepath.Ext(filename))
	if !videoExts[ext] {
		writeError(w, http.StatusBadRequest, "unsupported_type", "supported video formats: mp4, mov, m4v, webm, mkv, avi")
		return
	}

	key := path.Join("videos", storage.NewID(), "original"+ext)
	size, err := s.Storage.Put(r.Context(), key, part)
	if err != nil {
		_ = s.Storage.Delete(r.Context(), key)
		uploadError(s, w, r, err)
		return
	}
	if size == 0 {
		_ = s.Storage.Delete(r.Context(), key)
		writeError(w, http.StatusBadRequest, "bad_upload", "file is empty")
		return
	}

	asset, oldID, err := s.Store.AttachVideo(r.Context(), id, store.NewAsset{
		Kind: store.AssetVideo, StorageKey: key, OriginalFilename: truncate(filename, 255),
		ContentType: "video/" + strings.TrimPrefix(ext, "."), SizeBytes: size,
		Status: store.AssetUploaded, UploadedBy: &u.ID,
	})
	if err != nil {
		_ = s.Storage.Delete(r.Context(), key)
		s.internalError(w, r, err)
		return
	}
	if oldID != nil {
		s.Pipeline.DeleteAssets(r.Context(), []int64{*oldID})
	}
	if err := s.Pipeline.Worker.Enqueue(r.Context(), pipeline.JobProcessVideo, pipeline.ProcessPayload{AssetID: asset.ID}); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.Log.Info("video uploaded, processing queued", "component", "pipeline", "post_id", id, "asset_id", asset.ID,
		"file", filename, "key", key, "size_mb", fmt.Sprintf("%.1f", float64(size)/(1<<20)), "replaced_asset_id", oldID)
	s.writeAdminPost(w, r, id, http.StatusAccepted)
}

var imageTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}

func (s *Server) adminUploadImage(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.Cfg.MaxImageBytes+1<<20)
	fields := map[string]string{}
	part, err := nextFilePart(r, fields)
	if err != nil {
		uploadError(s, w, r, err)
		return
	}
	defer part.Close()

	br := bufio.NewReaderSize(part, 4096)
	head, _ := br.Peek(512)
	ctype := http.DetectContentType(head)
	ext, ok := imageTypes[ctype]
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported_type", "supported image formats: jpeg, png, webp, gif")
		return
	}
	key := path.Join("images", storage.NewID()+ext)
	size, err := s.Storage.Put(r.Context(), key, br)
	if err != nil {
		_ = s.Storage.Delete(r.Context(), key)
		uploadError(s, w, r, err)
		return
	}

	var width, height *int
	if obj, err := s.Storage.Open(r.Context(), key); err == nil {
		if cfg, _, err := image.DecodeConfig(obj); err == nil {
			width, height = &cfg.Width, &cfg.Height
		}
		obj.Close()
	}

	caption := truncate(strings.TrimSpace(fields["caption"]), 500)
	_, err = s.Store.AddAttachment(r.Context(), id, store.NewAsset{
		Kind: store.AssetImage, StorageKey: key, OriginalFilename: truncate(filepath.Base(part.FileName()), 255),
		ContentType: ctype, SizeBytes: size, Status: store.AssetReady, Width: width, Height: height, UploadedBy: &u.ID,
	}, caption)
	if errors.Is(err, store.ErrNotFound) {
		_ = s.Storage.Delete(r.Context(), key)
		writeError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		_ = s.Storage.Delete(r.Context(), key)
		s.internalError(w, r, err)
		return
	}
	s.writeAdminPost(w, r, id, http.StatusCreated)
}

func (s *Server) adminDeleteImage(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	assetID, ok := idParam(w, r, "assetId")
	if !ok {
		return
	}
	is, err := s.Store.IsAttachment(r.Context(), id, assetID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !is {
		writeError(w, http.StatusNotFound, "not_found", "image not found")
		return
	}
	s.Pipeline.DeleteAssets(r.Context(), []int64{assetID})
	s.writeAdminPost(w, r, id, http.StatusOK)
}

func (s *Server) adminSetTags(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Tags []struct {
			Kind    string `json:"kind"`
			Slug    string `json:"slug"`
			Name    string `json:"name"`
			Primary bool   `json:"primary"`
		} `json:"tags"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Tags) > 200 {
		writeError(w, http.StatusBadRequest, "invalid", "too many tags")
		return
	}
	tags := make([]store.TagInput, 0, len(in.Tags))
	seen := map[string]bool{}
	for _, t := range in.Tags {
		t.Name = strings.TrimSpace(t.Name)
		if !store.ValidTagKind(t.Kind) || t.Name == "" || utf8.RuneCountInString(t.Name) > 100 {
			writeError(w, http.StatusBadRequest, "invalid", "each tag needs a valid kind and a name (≤100 chars)")
			return
		}
		slug := analysis.Slugify(t.Slug)
		if slug == "" {
			slug = analysis.Slugify(t.Name)
		}
		if slug == "" || seen[t.Kind+":"+slug] {
			continue
		}
		seen[t.Kind+":"+slug] = true
		tags = append(tags, store.TagInput{Kind: t.Kind, Slug: slug, Name: t.Name, Primary: t.Primary})
	}
	if err := s.Store.SetPostTags(r.Context(), id, tags); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "post not found")
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeAdminPost(w, r, id, http.StatusOK)
}

// adminRunAnalysis starts a new analysis run. Body (optional):
//
//	{"steps": ["transcribe","keywords","claude"], "retranscribe": false}
//
// steps defaults to the server's ANALYSIS_STEPS; retranscribe drops cached
// transcripts so speech is transcribed again.
func (s *Server) adminRunAnalysis(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Steps        []string `json:"steps"`
		Retranscribe bool     `json:"retranscribe"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &in) {
		return
	}
	u, _ := userFrom(r.Context())
	log := s.Log.With("component", "analysis", "post_id", id, "user_id", u.ID, "req_id", middleware.GetReqID(r.Context()))
	log.Info("run analysis requested", "steps", in.Steps, "retranscribe", in.Retranscribe)
	reject := func(status int, code, msg string, attrs ...any) {
		log.Warn("run analysis rejected", append([]any{"reason", code, "msg", msg}, attrs...)...)
		writeError(w, status, code, msg)
	}
	p, err := s.Store.GetPost(r.Context(), id, 0)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if p.Video == nil {
		reject(http.StatusConflict, "video_not_ready", "upload a video and wait for processing first", "video", "none")
		return
	}
	if p.Video.Status != store.AssetReady {
		reject(http.StatusConflict, "video_not_ready", "upload a video and wait for processing first",
			"asset_id", p.Video.ID, "video_status", p.Video.Status)
		return
	}
	if p.Analysis != nil && (p.Analysis.Status == store.AnalysisPending || p.Analysis.Status == store.AnalysisRunning) {
		reject(http.StatusConflict, "analysis_in_progress", "analysis is already running",
			"analysis_id", p.Analysis.ID, "analysis_status", p.Analysis.Status)
		return
	}
	if !s.Pipeline.Analysis.Enabled() {
		reject(http.StatusConflict, "analysis_disabled", "video analysis is not configured on the server")
		return
	}
	if _, err := s.Pipeline.Analysis.Resolve(in.Steps); err != nil {
		reject(http.StatusBadRequest, "invalid_steps", err.Error())
		return
	}
	if in.Retranscribe {
		if err := s.Store.DeleteTranscripts(r.Context(), p.Video.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
		log.Info("cached transcripts deleted", "asset_id", p.Video.ID)
	}
	if _, err := s.Pipeline.StartAnalysis(r.Context(), id, p.Video.ID, in.Steps); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeAdminPost(w, r, id, http.StatusAccepted)
}

// adminAnalysisSteps lists the configured analysis steps and the defaults.
func (s *Server) adminAnalysisSteps(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"steps": []any{}, "defaults": []string{}}
	if s.Pipeline.Analysis.Enabled() {
		resp["steps"] = s.Pipeline.Analysis.Steps()
		resp["defaults"] = s.Pipeline.Analysis.Defaults()
	}
	writeJSON(w, http.StatusOK, resp)
}

// adminTranscript returns the most recent transcript of the post's video.
func (s *Server) adminTranscript(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	p, err := s.Store.GetPost(r.Context(), id, 0)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.Video == nil) {
		writeError(w, http.StatusNotFound, "not_found", "no transcript")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	t, err := s.Store.LatestTranscript(r.Context(), p.Video.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no transcript")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) adminTags(w http.ResponseWriter, r *http.Request) {
	facets, err := s.Store.TagFacets(r.Context(), false)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": facets})
}

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context(), 500, 0)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
