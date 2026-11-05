package httpapi

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lilya/practice/internal/auth"
	"github.com/lilya/practice/internal/store"
)

type credentials struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name,omitempty"`
	// Client is "web" (default: HttpOnly cookie) or "mobile" / "telegram"
	// (token returned in the body, to be sent as "Authorization: Bearer").
	Client string `json:"client,omitempty"`
}

type authResponse struct {
	User  store.User `json:"user"`
	Token string     `json:"token,omitempty"`
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func validEmail(e string) bool {
	if len(e) > 254 || !strings.Contains(e, "@") {
		return false
	}
	a, err := mail.ParseAddress(e)
	return err == nil && a.Address == e
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.AllowSignup {
		writeError(w, http.StatusForbidden, "signup_disabled", "registration is closed")
		return
	}
	var in credentials
	if !decodeJSON(w, r, &in) {
		return
	}
	if !s.loginLimiter.Allow("register-ip:" + s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
		return
	}
	in.Email = normalizeEmail(in.Email)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if !validEmail(in.Email) {
		writeError(w, http.StatusBadRequest, "invalid_email", "please enter a valid email")
		return
	}
	if utf8.RuneCountInString(in.DisplayName) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_name", "name is too long")
		return
	}
	if err := auth.ValidatePassword(in.Password); err != nil {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	user, err := s.Store.CreateUser(r.Context(), in.Email, in.DisplayName, store.RoleMember, hash)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "email_taken", "an account with this email already exists")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.startSession(w, r, user, in.Client, http.StatusCreated)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Email = normalizeEmail(in.Email)
	ipKey := "login-ip:" + s.clientIP(r)
	emailKey := "login-email:" + in.Email
	// Only failed attempts count towards the limit.
	if s.loginLimiter.Blocked(ipKey, emailKey) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
		return
	}
	if in.Email == "" || in.Password == "" || len(in.Password) > 4*auth.MaxPasswordLen {
		s.loginLimiter.Hit(ipKey, emailKey)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "wrong email or password")
		return
	}
	user, err := s.Store.UserByEmail(r.Context(), in.Email)
	if errors.Is(err, store.ErrNotFound) {
		auth.BurnVerifyTime(in.Password)
		s.loginLimiter.Hit(ipKey, emailKey)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "wrong email or password")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ok, err := auth.VerifyPassword(in.Password, user.PasswordHash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok || user.DisabledAt != nil {
		s.loginLimiter.Hit(ipKey, emailKey)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "wrong email or password")
		return
	}
	s.loginLimiter.Reset(emailKey)
	s.startSession(w, r, user, in.Client, http.StatusOK)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user store.User, client string, status int) {
	switch client {
	case "", "web":
		client = "web"
	case "mobile", "telegram":
	default:
		writeError(w, http.StatusBadRequest, "invalid_client", "client must be web, mobile or telegram")
		return
	}
	token, hash, err := auth.NewToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	expires := time.Now().Add(s.Cfg.SessionTTL)
	if _, err := s.Store.CreateSession(r.Context(), user.ID, hash, client, r.UserAgent(), s.clientIP(r), expires); err != nil {
		s.internalError(w, r, err)
		return
	}
	resp := authResponse{User: user}
	if client == "web" {
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    token,
			Path:     "/",
			Expires:  expires,
			MaxAge:   int(s.Cfg.SessionTTL.Seconds()),
			HttpOnly: true,
			Secure:   s.Cfg.CookieSecure,
			SameSite: http.SameSiteLaxMode,
		})
	} else {
		resp.Token = token
	}
	writeJSON(w, status, resp)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := sessionFrom(r.Context()); ok {
		if err := s.Store.DeleteSession(r.Context(), sess.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	sess, _ := sessionFrom(r.Context())
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if !s.loginLimiter.Allow("pwchange-user:" + u.Email) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
		return
	}
	ok, err := auth.VerifyPassword(in.CurrentPassword, u.PasswordHash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_credentials", "current password is wrong")
		return
	}
	if err := auth.ValidatePassword(in.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	hash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.ID, hash); err != nil {
		s.internalError(w, r, err)
		return
	}
	// Sign out every other device, including the Telegram bot.
	if err := s.Store.DeleteUserSessions(r.Context(), u.ID, sess.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.Store.UnlinkUserTelegram(r.Context(), u.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
