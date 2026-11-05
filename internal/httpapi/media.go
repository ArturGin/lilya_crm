package httpapi

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/lilya/practice/internal/storage"
	"github.com/lilya/practice/internal/store"
)

// serveMedia streams an asset with HTTP range support (video seeking).
// Access: admins see everything; members only assets of published posts.
func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	asset, err := s.Store.AssetByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !u.IsAdmin() {
		allowed, err := s.Store.MemberCanViewAsset(r.Context(), id)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !allowed {
			writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
	}

	key, ctype := asset.StorageKey, asset.ContentType
	if asset.Kind == store.AssetVideo {
		if asset.PlaybackKey == nil || asset.Status != store.AssetReady {
			writeError(w, http.StatusConflict, "not_ready", "video is still processing")
			return
		}
		key, ctype = *asset.PlaybackKey, "video/mp4"
	}

	// Remote backends (S3/CDN) can hand out a short-lived direct URL instead
	// of proxying bytes through the app.
	if signer, ok := s.Storage.(storage.URLSigner); ok {
		url, err := signer.SignedURL(r.Context(), key, 15*time.Minute)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	obj, err := s.Storage.Open(r.Context(), key)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "file missing")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeContent(w, r, "", obj.ModTime, obj)
}

// spaHandler serves the built web app; unknown paths fall back to index.html
// so client-side routes work on reload.
func (s *Server) spaHandler() http.Handler {
	if s.WebFS == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "web app not built (run `npm run build` in web/)", http.StatusNotFound)
		})
	}
	if _, err := fs.Stat(s.WebFS, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "web app not built (run `npm run build` in web/)", http.StatusNotFound)
		})
	}
	files := http.FileServerFS(s.WebFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(s.WebFS, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") { // content-hashed by Vite
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(p) != "" { // looks like a missing static file
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.WebFS, "index.html")
	})
}
