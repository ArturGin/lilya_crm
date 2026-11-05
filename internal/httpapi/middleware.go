package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/lilya/practice/internal/auth"
	"github.com/lilya/practice/internal/store"
)

const sessionCookie = "lilya_session"

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSession
	ctxAuthMethod
)

func userFrom(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(ctxUser).(store.User)
	return u, ok
}

func sessionFrom(ctx context.Context) (store.Session, bool) {
	s, ok := ctx.Value(ctxSession).(store.Session)
	return s, ok
}

// sessionToken extracts the token from "Authorization: Bearer" (mobile, bots)
// or from the session cookie (web).
func sessionToken(r *http.Request) (token, method string) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), "bearer"
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		return c.Value, "cookie"
	}
	return "", ""
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, method := sessionToken(r)
		ctx := context.WithValue(r.Context(), ctxAuthMethod, method)
		if token != "" && len(token) < 200 {
			sess, user, err := s.Store.SessionUser(ctx, auth.HashToken(token))
			switch {
			case err == nil:
				ctx = context.WithValue(ctx, ctxUser, user)
				ctx = context.WithValue(ctx, ctxSession, sess)
			case errors.Is(err, store.ErrNotFound):
				if method == "cookie" {
					s.clearSessionCookie(w)
				}
			default:
				s.internalError(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// csrfGuard: state-changing requests that may carry the session cookie must
// include X-Requested-With. Browsers cannot add that header cross-origin
// without a CORS preflight (which we never approve), so a forged form post
// from another site is rejected. Bearer-token clients are exempt.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Context().Value(ctxAuthMethod) != "bearer" && r.Header.Get("X-Requested-With") == "" {
				writeError(w, http.StatusForbidden, "csrf", "missing X-Requested-With header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := userFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "please sign in")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "please sign in")
			return
		}
		if !u.IsAdmin() {
			writeError(w, http.StatusForbidden, "forbidden", "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; "+
				"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; "+
				"frame-ancestors 'self'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				s.Log.Error("panic", "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "something went wrong")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/healthz" {
			return
		}
		s.Log.Info("http",
			"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"bytes", ww.BytesWritten(), "dur_ms", time.Since(start).Milliseconds(),
			"ip", s.clientIP(r), "req_id", middleware.GetReqID(r.Context()))
	})
}

// clientIP honours proxy headers only when TRUST_PROXY is enabled.
func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Left-most entry is the original client (as set by our proxy).
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return strings.TrimSpace(xr)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
