package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lilya/practice/internal/analysis"
	"github.com/lilya/practice/internal/auth"
	"github.com/lilya/practice/internal/db"
	"github.com/lilya/practice/internal/pipeline"
	"github.com/lilya/practice/internal/storage"
	"github.com/lilya/practice/internal/store"
	"github.com/lilya/practice/internal/worker"
)

func TestVideoFileName(t *testing.T) {
	cases := []struct {
		name, mime, want string
		ok               bool
	}{
		{"IMG_1234.MOV", "video/quicktime", "IMG_1234.MOV", true},
		{"", "video/mp4", "telegram-video.mp4", true},
		{"clip", "video/quicktime", "clip.mov", true},
		{"notes.pdf", "application/pdf", "notes.pdf", false},
		{"weird.bin", "video/x-matroska", "weird.mkv", true},
	}
	for _, c := range cases {
		got, ok := videoFileName(c.name, c.mime)
		if got != c.want || ok != c.ok {
			t.Errorf("videoFileName(%q, %q) = %q, %v; want %q, %v", c.name, c.mime, got, ok, c.want, c.ok)
		}
	}
}

func TestCommand(t *testing.T) {
	for in, want := range map[string][2]string{
		"/login":                       {"/login", ""},
		"/LOGIN@lilya_bot a@b.c":       {"/login", "a@b.c"},
		"hello":                        {"", "hello"},
		"  /logout  ":                  {"/logout", ""},
		"/login  someone@example.com ": {"/login", "someone@example.com"},
	} {
		if cmd, args := command(in); cmd != want[0] || args != want[1] {
			t.Errorf("command(%q) = %q, %q; want %q, %q", in, cmd, args, want[0], want[1])
		}
	}
}

func TestLinkable(t *testing.T) {
	for u, want := range map[string]bool{
		"https://practice.example.com": true,
		"http://practice.example.com":  true,
		"https://8.8.8.8":              true,
		"https://localhost":            false,
		"http://localhost:8080":        false,
		"http://127.0.0.1:8080":        false,
		"http://192.168.1.10":          false,
		"http://app:8080":              false,
		"http://lilya.local":           false,
		"ftp://example.com":            false,
	} {
		if got := linkable(u); got != want {
			t.Errorf("linkable(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestTitleBody(t *testing.T) {
	title, body := titleBody("  Утренняя практика \nМягко раскрываем ТБС\nи спину", "fallback")
	if title != "Утренняя практика" || body != "Мягко раскрываем ТБС\nи спину" {
		t.Fatalf("got %q / %q", title, body)
	}
	if title, body := titleBody("", "fallback"); title != "fallback" || body != "" {
		t.Fatalf("empty caption: got %q / %q", title, body)
	}
}

// fakeAPI is a minimal Bot API server.
type fakeAPI struct {
	token   string
	updates []Update
	file    []byte

	mu      sync.Mutex
	sent    []map[string]any
	deleted []float64 // message IDs
}

func (f *fakeAPI) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		out = append(out, m["text"].(string))
	}
	return out
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/file/bot"+f.token+"/videos/file_1.mov" {
		_, _ = w.Write(f.file)
		return
	}
	method, ok := strings.CutPrefix(r.URL.Path, "/bot"+f.token+"/")
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)
	result := func(v any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": v})
	}
	switch method {
	case "getMe":
		result(User{ID: 1, IsBot: true, Username: "lilya_test_bot"})
	case "deleteWebhook":
		result(true)
	case "getUpdates":
		f.mu.Lock()
		ups := f.updates
		f.updates = nil
		f.mu.Unlock()
		if len(ups) == 0 {
			time.Sleep(50 * time.Millisecond) // a short "long poll"
		}
		result(ups)
	case "sendMessage":
		f.mu.Lock()
		f.sent = append(f.sent, params)
		f.mu.Unlock()
		result(map[string]any{"message_id": 99})
	case "deleteMessage":
		f.mu.Lock()
		f.deleted = append(f.deleted, params["message_id"].(float64))
		f.mu.Unlock()
		result(true)
	case "getFile":
		result(File{FileID: params["file_id"].(string), FileSize: int64(len(f.file)), FilePath: "videos/file_1.mov"})
	default:
		_, _ = io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found: method not found"}`)
	}
}

func TestClientRedactsToken(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1", Token: "123:SECRET"}
	_, err := c.GetMe(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("expected a redacted error, got %v", err)
	}
	var apiErr *APIError
	srv := httptest.NewServer(&fakeAPI{token: "other"})
	defer srv.Close()
	c.BaseURL = srv.URL
	if _, err := c.GetMe(context.Background()); !errors.As(err, &apiErr) || apiErr.Code != 401 {
		t.Fatalf("expected APIError 401, got %v", err)
	}
}

func TestOpenFileLocalPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "file_0.mov")
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, local, err := (&Client{}).OpenFile(context.Background(), File{FilePath: p})
	if err != nil || local != p {
		t.Fatalf("OpenFile: %v %q", err, local)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "data" {
		t.Fatalf("got %q", b)
	}
}

// Requires TEST_DATABASE_URL (an empty, disposable database).
func TestBotUploadAndNotify(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
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
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := st.CreateUser(ctx, "admin-tg@example.com", "Лиля", store.RoleAdmin, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "member-tg@example.com", "", store.RoleMember, hash); err != nil {
		t.Fatal(err)
	}

	local, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pl := &pipeline.Pipeline{Store: st, Storage: local, Worker: worker.New(st, log, 1), Log: log, TempDir: local.TempDir(), Lang: "ru"}

	const adminTG, memberTG = int64(5550001), int64(5550002)
	msg := func(id, from int64, text string) Update {
		return Update{UpdateID: id, Message: &Message{MessageID: id, From: &User{ID: from}, Chat: Chat{ID: from, Type: "private"}, Text: text}}
	}
	api := &fakeAPI{token: "42:TOKEN", file: []byte("not really a video, but enough to store")}
	video := Update{UpdateID: 20, Message: &Message{MessageID: 20, From: &User{ID: adminTG}, Chat: Chat{ID: adminTG, Type: "private"},
		Caption:  "Утренняя практика\nМягко раскрываем ТБС",
		Document: &Document{FileID: "F1", FileName: "IMG_0001.MOV", MimeType: "video/quicktime", FileSize: int64(len(api.file))}}}
	api.updates = []Update{
		msg(10, adminTG, "/start"),
		msg(11, adminTG, "/login"),
		msg(12, adminTG, "Admin-TG@example.com"),
		msg(13, adminTG, "wrong password"),
		msg(14, adminTG, "/login admin-tg@example.com"),
		msg(15, adminTG, "correct horse battery"),
		msg(16, memberTG, "/login member-tg@example.com"),
		msg(17, memberTG, "correct horse battery"),
		video,
	}
	srv := httptest.NewServer(api)
	defer srv.Close()

	bot := &Bot{Client: &Client{BaseURL: srv.URL, Token: api.token}, Store: st, Pipeline: pl, Log: log,
		PublicURL: "https://practice.example.com", Lang: "ru", MaxVideoBytes: 1 << 30}
	pl.Notify = bot
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { bot.Run(runCtx); close(done) }()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(strings.Join(api.messages(), "\n"), "✅ Видео загружено") {
		if time.Now().After(deadline) {
			t.Fatalf("no upload confirmation; messages: %q", api.messages())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	msgs := strings.Join(api.messages(), "\n---\n")
	for _, want := range []string{"/login", "Введите email", "Теперь пароль", "Неверный email или пароль",
		"Вы вошли как Лиля (администратор)", "Вы вошли как member-tg@example.com", "только администраторам",
		"Видео получено", "Утренняя практика", "/admin/posts/"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("messages lack %q:\n%s", want, msgs)
		}
	}
	api.mu.Lock()
	deleted := api.deleted
	api.mu.Unlock()
	if len(deleted) != 3 || deleted[0] != 13 || deleted[1] != 15 || deleted[2] != 17 {
		t.Errorf("password messages should be deleted, got %v", deleted)
	}
	if ids, err := st.AdminTelegramIDs(ctx); err != nil || len(ids) != 1 || ids[0] != adminTG {
		t.Fatalf("admin telegram ids = %v, %v; want [%d]", ids, err, adminTG)
	}

	posts, total, err := st.ListPosts(ctx, store.ListFilter{UserID: admin.ID, Statuses: []string{store.PostDraft}, Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("posts: %v total=%d", err, total)
	}
	p, err := st.GetPost(ctx, posts[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Утренняя практика" || p.Body != "Мягко раскрываем ТБС" || p.Video == nil ||
		p.Video.Status != store.AssetUploaded || p.Video.OriginalFilename != "IMG_0001.MOV" || p.Video.SizeBytes != int64(len(api.file)) {
		t.Fatalf("unexpected post: %+v video=%+v", p, p.Video)
	}

	// Notifications go to linked admins only.
	api.mu.Lock()
	api.sent = nil
	api.mu.Unlock()
	bot.VideoProcessed(ctx, pipeline.VideoNotice{AssetID: p.Video.ID, PostIDs: []int64{p.ID}, OK: true,
		DurationSeconds: 2067, Width: 1920, Height: 1080, AnalysisQueued: true})
	res := analysis.Result{Summary: "Мягкая практика <на> ТБС", Level: "beginner",
		Focus: []analysis.Emphasis{{Slug: "hip-opening", Primary: true}}, BodyAreas: []analysis.Emphasis{{Slug: "hips"}},
		Poses: []analysis.Pose{{Slug: "pigeon", NameEN: "Pigeon"}}}
	res.Normalize()
	bot.AnalysisFinished(ctx, pipeline.AnalysisNotice{PostID: p.ID, OK: true, Result: &res})
	bot.AnalysisFinished(ctx, pipeline.AnalysisNotice{PostID: p.ID, Error: "transcription API 500: <oops>"})

	api.mu.Lock()
	sent := api.sent
	api.mu.Unlock()
	if len(sent) != 3 {
		t.Fatalf("want 3 notifications, got %d: %q", len(sent), api.messages())
	}
	for _, m := range sent {
		if m["chat_id"].(float64) != float64(adminTG) || m["parse_mode"] != "HTML" {
			t.Fatalf("bad notification params: %v", m)
		}
	}
	all := strings.Join(api.messages(), "\n---\n")
	for _, want := range []string{"Видео обработано", "34:27 · 1920×1080", "Анализ готов", "Мягкая практика &lt;на&gt; ТБС",
		"Направленность:", "Позы:", "черновиках", "Анализ не удался", "&lt;oops&gt;"} {
		if !strings.Contains(all, want) {
			t.Errorf("notifications lack %q:\n%s", want, all)
		}
	}

	// Logging in elsewhere moves the link; /logout and password changes remove it.
	if err := st.LinkTelegram(ctx, admin.ID, memberTG); err != nil {
		t.Fatal(err)
	}
	if u, err := st.UserByTelegramID(ctx, memberTG); err != nil || u.ID != admin.ID {
		t.Fatalf("relink: %v %v", u, err)
	}
	if linked, err := st.UnlinkTelegram(ctx, memberTG); err != nil || !linked {
		t.Fatalf("unlink: %v %v", linked, err)
	}
	if ids, _ := st.AdminTelegramIDs(ctx); len(ids) != 0 {
		t.Fatalf("after logout admin ids = %v", ids)
	}
}
