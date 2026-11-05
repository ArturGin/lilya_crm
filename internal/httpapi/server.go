// Package httpapi exposes the JSON API (/api/v1) consumed by the web app and,
// later, by the Telegram mini-app / mobile clients, and serves the web build.
package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/lilya/practice/internal/config"
	"github.com/lilya/practice/internal/pipeline"
	"github.com/lilya/practice/internal/storage"
	"github.com/lilya/practice/internal/store"
)

type Server struct {
	Cfg      config.Config
	Store    *store.Store
	Storage  storage.Storage
	Pipeline *pipeline.Pipeline
	Log      *slog.Logger
	WebFS    fs.FS // built web app (may be nil)

	loginLimiter *rateLimiter
}

func (s *Server) Handler() http.Handler {
	// 10 attempts per 15 minutes per IP and per email.
	s.loginLimiter = newRateLimiter(10, 15*time.Minute)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(s.recoverer)
	r.Use(s.requestLogger)
	r.Use(securityHeaders)

	r.Get("/healthz", s.health)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.authenticate)
		r.Use(csrfGuard)
		r.Use(noStore)

		r.Get("/config", s.publicConfig)
		r.Post("/auth/register", s.register)
		r.Post("/auth/login", s.login)
		r.Post("/auth/logout", s.logout)

		r.Group(func(r chi.Router) {
			r.Use(requireUser)
			r.Get("/me", s.me)
			r.Post("/me/password", s.changePassword)

			r.Get("/vocabulary", s.vocabulary)
			r.Get("/tags", s.tagFacets)
			r.Get("/posts", s.listPosts)
			r.Get("/posts/{id}", s.getPost)
			r.Post("/posts/{id}/completions", s.addCompletion)
			r.Delete("/posts/{id}/completions/latest", s.removeCompletion)
			r.Get("/media/{id}", s.serveMedia)
		})

		r.Route("/admin", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/posts", s.adminListPosts)
			r.Post("/posts", s.adminCreatePost)
			r.Get("/posts/{id}", s.adminGetPost)
			r.Patch("/posts/{id}", s.adminUpdatePost)
			r.Delete("/posts/{id}", s.adminDeletePost)
			r.Post("/posts/{id}/video", s.adminUploadVideo)
			r.Post("/posts/{id}/images", s.adminUploadImage)
			r.Delete("/posts/{id}/images/{assetId}", s.adminDeleteImage)
			r.Put("/posts/{id}/tags", s.adminSetTags)
			r.Post("/posts/{id}/analysis", s.adminRunAnalysis)
			r.Get("/posts/{id}/transcript", s.adminTranscript)
			r.Get("/analysis/steps", s.adminAnalysisSteps)
			r.Get("/tags", s.adminTags)
			r.Get("/users", s.adminListUsers)
		})

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
		})
	})

	r.Handle("/*", s.spaHandler())
	return r
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := store.Ping(r.Context(), s.Store); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db_unavailable", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) publicConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"allow_signup":     s.Cfg.AllowSignup,
		"analysis_enabled": s.Pipeline.Analysis.Enabled(),
		"content_language": s.Cfg.AnalysisLanguage,
		"max_video_bytes":  s.Cfg.MaxVideoBytes,
		"max_image_bytes":  s.Cfg.MaxImageBytes,
	})
}
