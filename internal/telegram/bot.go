package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lilya/practice/internal/auth"
	"github.com/lilya/practice/internal/pipeline"
	"github.com/lilya/practice/internal/store"
)

// cloudFileLimit is the public Bot API's download limit.
const cloudFileLimit = 20 << 20

// Bot long-polls Telegram for messages from linked admins and implements
// pipeline.Notifier.
type Bot struct {
	Client        *Client
	Store         *store.Store
	Pipeline      *pipeline.Pipeline
	Log           *slog.Logger
	PublicURL     string
	Lang          string // ru | en
	MaxVideoBytes int64

	sem chan struct{}
	wg  sync.WaitGroup

	mu     sync.Mutex
	logins map[int64]*loginState // in-progress /login conversations by Telegram user ID
	fails  map[string]*failCount // failed login attempts by "tg:<id>" / "email:<email>"
}

// loginState is an in-progress /login conversation.
type loginState struct {
	email   string // empty while waiting for the email
	expires time.Time
}

type failCount struct {
	n     int
	reset time.Time
}

const (
	loginTTL      = 10 * time.Minute
	maxFails      = 5
	failWindow    = 15 * time.Minute
	maxPasswordIn = 1024
)

var _ pipeline.Notifier = (*Bot)(nil)

// cloud reports whether the public Bot API (20 MB download limit) is used.
func (b *Bot) cloud() bool { return strings.Contains(b.Client.base(), "api.telegram.org") }

func (b *Bot) tr(ru, en string) string {
	if b.Lang == "en" {
		return en
	}
	return ru
}

// Run polls for updates until ctx is cancelled, then waits for in-flight
// uploads to stop.
func (b *Bot) Run(ctx context.Context) {
	b.sem = make(chan struct{}, 2)
	defer b.wg.Wait()

	var me User
	for {
		var err error
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		me, err = b.Client.GetMe(cctx)
		if err == nil {
			err = b.Client.DeleteWebhook(cctx)
		}
		cancel()
		if err == nil {
			break
		}
		b.Log.Error("telegram bot cannot connect, retrying in 30s", "api", b.Client.base(), "err", err)
		if !sleep(ctx, 30*time.Second) {
			return
		}
	}
	b.Log.Info("telegram bot started", "username", "@"+me.Username, "api", b.Client.base(),
		"max_download_mb", map[bool]string{true: "20 (public Bot API)", false: "unlimited (local Bot API server)"}[b.cloud()])
	if !linkable(b.PublicURL) {
		b.Log.Warn("PUBLIC_URL is not a public domain, so Telegram won't make post links clickable; messages show the address as text",
			"public_url", b.PublicURL)
	}

	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		pctx, cancel := context.WithTimeout(ctx, 70*time.Second)
		ups, err := b.Client.GetUpdates(pctx, offset, 50)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.Code == 409 {
				b.Log.Error("telegram getUpdates conflict: another process is polling this bot token", "err", err)
			} else {
				b.Log.Warn("telegram getUpdates failed", "err", err, "retry_in", backoff.String())
			}
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		// Messages are handled in order (a login conversation must see the
		// email before the password); video downloads run in the background.
		for _, u := range ups {
			offset = u.UpdateID + 1
			if u.Message == nil || u.Message.From == nil || u.Message.Chat.Type != "private" {
				continue
			}
			b.handle(ctx, u.Message)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (b *Bot) reply(msg *Message, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.Client.SendMessage(ctx, msg.Chat.ID, text, msg.MessageID); err != nil {
		b.Log.Warn("telegram reply failed", "chat_id", msg.Chat.ID, "err", err)
	}
}

// command splits "/login@my_bot you@example.com" into "/login" and "you@example.com".
func command(text string) (cmd, args string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	cmd, args, _ = strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(cmd, "@")
	return strings.ToLower(cmd), strings.TrimSpace(args)
}

func (b *Bot) handle(ctx context.Context, msg *Message) {
	from := msg.From
	log := b.Log.With("telegram_id", from.ID, "telegram_username", from.Username)
	cmd, args := command(msg.Text)

	switch cmd {
	case "/login":
		b.startLogin(msg, args)
		return
	case "/cancel":
		b.endLogin(from.ID)
		b.reply(msg, b.tr("Отменено.", "Cancelled."))
		return
	case "/logout":
		b.endLogin(from.ID)
		linked, err := b.Store.UnlinkTelegram(ctx, from.ID)
		if err != nil {
			log.Error("telegram: unlink", "err", err)
			b.reply(msg, b.tr("Внутренняя ошибка, попробуйте позже.", "Internal error, please try again later."))
			return
		}
		if linked {
			log.Info("telegram account unlinked")
		}
		b.reply(msg, b.tr("Вы вышли. Уведомления больше не придут. Войти снова: /login",
			"You're logged out and won't get notifications any more. Log in again: /login"))
		return
	}
	if st := b.loginInProgress(from.ID); st != nil && cmd == "" && msg.Text != "" {
		b.continueLogin(ctx, log, msg, st)
		return
	}

	user, err := b.Store.UserByTelegramID(ctx, from.ID)
	if errors.Is(err, store.ErrNotFound) {
		b.reply(msg, b.tr(
			"Привет! Это бот библиотеки практик для администраторов: он присылает уведомления об обработке и анализе видео и принимает видео для загрузки.\n\nВойдите с email и паролем от сайта: /login",
			"Hi! This is the practice library's admin bot: it sends notifications about video processing and analysis, and accepts videos to upload.\n\nLog in with your website email and password: /login"))
		return
	}
	if err != nil {
		log.Error("telegram: look up user", "err", err)
		b.reply(msg, b.tr("Внутренняя ошибка, попробуйте позже.", "Internal error, please try again later."))
		return
	}
	log = log.With("user_id", user.ID)
	if !user.IsAdmin() {
		b.reply(msg, fmt.Sprintf(b.tr(
			"Вы вошли как %s. Уведомления и загрузка видео доступны только администраторам.\nВыйти: /logout",
			"You're logged in as %s. Notifications and video uploads are for admins only.\nLog out: /logout"),
			html.EscapeString(user.Email)))
		return
	}

	var fileID, fileName, mime string
	var size int64
	switch {
	case msg.Video != nil:
		fileID, fileName, mime, size = msg.Video.FileID, msg.Video.FileName, msg.Video.MimeType, msg.Video.FileSize
	case msg.Document != nil:
		fileID, fileName, mime, size = msg.Document.FileID, msg.Document.FileName, msg.Document.MimeType, msg.Document.FileSize
	default: // /start, /help, anything else
		b.reply(msg, b.helpText(user))
		return
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.ingest(ctx, log, msg, user, fileID, fileName, mime, size)
	}()
}

func (b *Bot) startLogin(msg *Message, args string) {
	st := &loginState{expires: time.Now().Add(loginTTL)}
	if strings.Contains(args, "@") {
		st.email = strings.ToLower(args)
	}
	b.mu.Lock()
	if b.logins == nil {
		b.logins = map[int64]*loginState{}
	}
	b.logins[msg.From.ID] = st
	b.mu.Unlock()
	if st.email != "" {
		b.reply(msg, b.passwordPrompt())
		return
	}
	b.reply(msg, b.tr("Введите email вашего аккаунта на сайте (или /cancel):", "Enter your website account email (or /cancel):"))
}

func (b *Bot) passwordPrompt() string {
	return b.tr("Теперь пароль. Сообщение с паролем я сразу удалю из чата.", "Now your password. I'll delete the message with it right away.")
}

func (b *Bot) loginInProgress(tgID int64) *loginState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.logins[tgID]
	if st != nil && time.Now().After(st.expires) {
		delete(b.logins, tgID)
		return nil
	}
	return st
}

func (b *Bot) endLogin(tgID int64) {
	b.mu.Lock()
	delete(b.logins, tgID)
	b.mu.Unlock()
}

// blocked reports whether any key has too many recent failed logins.
func (b *Bot) blocked(keys ...string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		if f := b.fails[k]; f != nil && now.Before(f.reset) && f.n >= maxFails {
			return true
		}
	}
	return false
}

func (b *Bot) failed(keys ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fails == nil {
		b.fails = map[string]*failCount{}
	}
	now := time.Now()
	for k, f := range b.fails { // keep the map small
		if now.After(f.reset) {
			delete(b.fails, k)
		}
	}
	for _, k := range keys {
		f := b.fails[k]
		if f == nil {
			f = &failCount{reset: now.Add(failWindow)}
			b.fails[k] = f
		}
		f.n++
	}
}

func (b *Bot) continueLogin(ctx context.Context, log *slog.Logger, msg *Message, st *loginState) {
	if st.email == "" {
		email := strings.ToLower(strings.TrimSpace(msg.Text))
		if !strings.Contains(email, "@") || len(email) > 320 {
			b.reply(msg, b.tr("Это не похоже на email. Попробуйте ещё раз или /cancel.", "That doesn't look like an email. Try again or /cancel."))
			return
		}
		b.mu.Lock()
		st.email = email
		b.mu.Unlock()
		b.reply(msg, b.passwordPrompt())
		return
	}

	// The message holds the password: remove it from the chat first.
	password := msg.Text
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	if err := b.Client.DeleteMessage(dctx, msg.Chat.ID, msg.MessageID); err != nil {
		log.Warn("telegram: could not delete password message", "err", err)
	}
	cancel()
	b.endLogin(msg.From.ID)
	email := st.email
	log = log.With("email", email)

	tgKey, emailKey := fmt.Sprintf("tg:%d", msg.From.ID), "email:"+email
	if b.blocked(tgKey, emailKey) {
		log.Warn("telegram login rate-limited")
		b.reply(msg, b.tr("Слишком много попыток. Попробуйте через 15 минут.", "Too many attempts. Try again in 15 minutes."))
		return
	}
	wrong := func() {
		b.failed(tgKey, emailKey)
		log.Warn("telegram login failed")
		b.reply(msg, b.tr("Неверный email или пароль. Попробовать снова: /login", "Wrong email or password. Try again: /login"))
	}
	if password == "" || len(password) > maxPasswordIn {
		wrong()
		return
	}
	user, err := b.Store.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		auth.BurnVerifyTime(password) // same timing as a wrong password
		wrong()
		return
	}
	if err != nil {
		log.Error("telegram login: look up user", "err", err)
		b.reply(msg, b.tr("Внутренняя ошибка, попробуйте позже.", "Internal error, please try again later."))
		return
	}
	ok, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil {
		log.Error("telegram login: verify password", "err", err)
		b.reply(msg, b.tr("Внутренняя ошибка, попробуйте позже.", "Internal error, please try again later."))
		return
	}
	if !ok || user.DisabledAt != nil {
		wrong()
		return
	}
	if err := b.Store.LinkTelegram(ctx, user.ID, msg.From.ID); err != nil {
		log.Error("telegram login: link account", "err", err)
		b.reply(msg, b.tr("Внутренняя ошибка, попробуйте позже.", "Internal error, please try again later."))
		return
	}
	b.mu.Lock()
	delete(b.fails, tgKey)
	delete(b.fails, emailKey)
	b.mu.Unlock()
	log.Info("telegram account linked", "user_id", user.ID, "role", user.Role)
	if !user.IsAdmin() {
		b.reply(msg, fmt.Sprintf(b.tr(
			"✅ Вы вошли как %s.\nУведомления и загрузка видео доступны только администраторам — если вам дадут права администратора, уведомления начнут приходить сами.\nВыйти: /logout",
			"✅ Logged in as %s.\nNotifications and video uploads are for admins only. If you're made an admin, notifications will start automatically.\nLog out: /logout"),
			html.EscapeString(user.Email)))
		return
	}
	b.reply(msg, "✅ "+b.helpText(user))
}

func (b *Bot) helpText(u store.User) string {
	name := u.DisplayName
	if name == "" {
		name = u.Email
	}
	text := fmt.Sprintf(b.tr(
		"Вы вошли как %s (администратор).\n\n🔔 Я пишу, когда видео обработано и когда анализ готов.\n\n📤 Пришлите видео — я создам черновик поста и запущу обработку и анализ. Подпись к видео станет заголовком (первая строка) и описанием.\nСовет: отправляйте видео <b>файлом</b> (📎 → Файл), чтобы Telegram его не сжимал.\n\nВыйти: /logout",
		"Logged in as %s (admin).\n\n🔔 I'll message you when a video is processed and when its analysis is ready.\n\n📤 Send me a video: I'll create a draft post and start processing and analysis. The caption becomes the title (first line) and description.\nTip: send the video <b>as a file</b> (📎 → File) so Telegram doesn't compress it.\n\nLog out: /logout"),
		html.EscapeString(name))
	if b.cloud() {
		text += b.tr("\n\n⚠️ Сейчас бот принимает файлы только до 20 МБ (ограничение Telegram). Большие видео загружайте через сайт.",
			"\n\n⚠️ The bot currently accepts files up to 20 MB only (Telegram limit). Upload bigger videos on the website.")
	}
	return text
}

var mimeExts = map[string]string{
	"video/mp4": ".mp4", "video/quicktime": ".mov", "video/x-m4v": ".m4v",
	"video/webm": ".webm", "video/x-matroska": ".mkv", "video/x-msvideo": ".avi",
}

// videoFileName returns a file name with a supported video extension, using
// the MIME type when the name has none.
func videoFileName(name, mime string) (string, bool) {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "/" {
		name = ""
	}
	if pipeline.VideoExts[strings.ToLower(filepath.Ext(name))] {
		return name, true
	}
	ext, ok := mimeExts[strings.ToLower(mime)]
	if !ok {
		return name, false
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if base == "" {
		base = "telegram-video"
	}
	return base + ext, true
}

func (b *Bot) ingest(ctx context.Context, log *slog.Logger, msg *Message, user store.User, fileID, fileName, mime string, size int64) {
	name, ok := videoFileName(fileName, mime)
	if !ok {
		b.reply(msg, b.tr("Это не похоже на видео. Поддерживаются mp4, mov, m4v, webm, mkv, avi.",
			"That doesn't look like a video. Supported: mp4, mov, m4v, webm, mkv, avi."))
		return
	}
	log = log.With("file", name, "size_mb", fmt.Sprintf("%.1f", float64(size)/(1<<20)))
	log.Info("telegram video received")
	if b.MaxVideoBytes > 0 && size > b.MaxVideoBytes {
		b.reply(msg, fmt.Sprintf(b.tr("Файл слишком большой (максимум %d МБ).", "The file is too large (max %d MB)."), b.MaxVideoBytes>>20))
		return
	}
	if b.cloud() && size > cloudFileLimit {
		log.Warn("telegram video over the public Bot API 20 MB download limit")
		b.reply(msg, b.tr(
			"Видео больше 20 МБ — через стандартный Telegram Bot API бот не может его скачать. Загрузите его через сайт или подключите локальный Bot API сервер (см. README, TELEGRAM_API_URL).",
			"The video is over 20 MB, which the standard Telegram Bot API won't let bots download. Upload it on the website, or set up a local Bot API server (see README, TELEGRAM_API_URL)."))
		return
	}

	b.sem <- struct{}{} // limit concurrent downloads
	defer func() { <-b.sem }()
	b.reply(msg, fmt.Sprintf(b.tr("⏳ Видео получено (%s), скачиваю…", "⏳ Got the video (%s), downloading…"), humanSize(size)))

	start := time.Now()
	fctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	f, err := b.Client.GetFile(fctx, fileID)
	if err != nil {
		log.Error("telegram getFile failed", "err", err)
		if strings.Contains(strings.ToLower(err.Error()), "too big") {
			b.reply(msg, b.tr("Telegram не даёт боту скачать такой большой файл. Загрузите видео через сайт.",
				"Telegram won't let the bot download a file this large. Please upload it on the website."))
		} else {
			b.reply(msg, b.tr("Не удалось скачать видео из Telegram: ", "Couldn't download the video from Telegram: ")+code(err.Error()))
		}
		return
	}
	rc, localPath, err := b.Client.OpenFile(fctx, f)
	if err != nil {
		log.Error("telegram file open failed", "err", err)
		b.reply(msg, b.tr("Не удалось скачать видео: ", "Couldn't download the video: ")+code(err.Error()))
		return
	}
	defer rc.Close()
	log.Info("telegram file ready, storing", "local_bot_api", localPath != "", "took", time.Since(start).Round(time.Second).String())

	title, body := titleBody(msg.Caption, b.tr("Практика "+time.Now().Format("02.01.2006"), "Practice "+time.Now().Format("2006-01-02")))
	postID, err := b.Store.CreatePost(fctx, title, body, user.ID)
	if err != nil {
		log.Error("telegram: create post", "err", err)
		b.reply(msg, b.tr("Не удалось создать пост: ", "Couldn't create the post: ")+code(err.Error()))
		return
	}
	if _, err := b.Pipeline.IngestVideo(fctx, postID, &user.ID, name, rc); err != nil {
		log.Error("telegram: store video", "post_id", postID, "err", err)
		_ = b.Store.DeletePost(context.Background(), postID)
		b.reply(msg, b.tr("Не удалось сохранить видео: ", "Couldn't save the video: ")+code(err.Error()))
		return
	}
	rc.Close()
	if localPath != "" { // the Bot API server's copy is no longer needed
		if err := os.Remove(localPath); err != nil {
			log.Warn("could not delete local Bot API file", "path", localPath, "err", err)
		}
	}
	log.Info("telegram video stored", "post_id", postID, "took", time.Since(start).Round(time.Second).String())
	b.reply(msg, fmt.Sprintf(b.tr(
		"✅ Видео загружено, создан черновик «%s».\nОбработка началась — напишу, когда видео будет готово и проанализировано.\n%s",
		"✅ Video uploaded, created draft “%s”.\nProcessing has started; I'll message you when the video is ready and analysed.\n%s"),
		html.EscapeString(title), b.postLink(postID)))
}

// titleBody splits a caption into a title (first line) and description.
func titleBody(caption, fallback string) (string, string) {
	caption = strings.TrimSpace(caption)
	title, body, _ := strings.Cut(caption, "\n")
	title = strings.TrimSpace(title)
	if title == "" {
		return fallback, ""
	}
	if utf8.RuneCountInString(title) > 200 {
		title = string([]rune(title)[:200])
	}
	return title, strings.TrimSpace(body)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n > 0:
		return fmt.Sprintf("%d KB", max(n>>10, 1))
	}
	return "?"
}

func code(s string) string {
	if utf8.RuneCountInString(s) > 500 {
		s = string([]rune(s)[:500]) + "…"
	}
	return "<code>" + html.EscapeString(s) + "</code>"
}

func (b *Bot) postLink(postID int64) string {
	u := fmt.Sprintf("%s/admin/posts/%d", b.PublicURL, postID)
	if !linkable(b.PublicURL) { // Telegram would silently drop the link: show the address to copy instead
		return b.tr("Пост: ", "Post: ") + "<code>" + html.EscapeString(u) + "</code>"
	}
	return fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(u), b.tr("Открыть пост", "Open post"))
}

// linkable reports whether Telegram will turn the URL into a clickable link:
// it needs an http(s) URL with a public domain name (not localhost or a bare
// hostname such as "app").
func linkable(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified()
	}
	return strings.Contains(host, ".")
}

func clock(sec int) string {
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// broadcast sends text to every linked admin.
func (b *Bot) broadcast(ctx context.Context, text string) {
	ids, err := b.Store.AdminTelegramIDs(ctx)
	if err != nil {
		b.Log.Error("telegram: list admin accounts", "err", err)
		return
	}
	if len(ids) == 0 {
		b.Log.Debug("telegram: no linked admins to notify")
		return
	}
	for _, id := range ids {
		if err := b.Client.SendMessage(ctx, id, text, 0); err != nil {
			b.Log.Warn("telegram notification failed", "telegram_id", id, "err", err)
		}
	}
}

func (b *Bot) post(ctx context.Context, id int64) (store.PostDetail, bool) {
	p, err := b.Store.GetPost(ctx, id, 0)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.Log.Warn("telegram: load post for notification", "post_id", id, "err", err)
		}
		return p, false
	}
	return p, true
}

// VideoProcessed implements pipeline.Notifier.
func (b *Bot) VideoProcessed(ctx context.Context, n pipeline.VideoNotice) {
	for _, id := range n.PostIDs {
		p, ok := b.post(ctx, id)
		if !ok {
			continue
		}
		title := html.EscapeString(p.Title)
		var text string
		if n.OK {
			next := b.tr("Анализ запущен — напишу, когда будет готов.", "Analysis has started; I'll message you when it's ready.")
			if !n.AnalysisQueued {
				next = b.tr("Анализ видео на сервере не настроен.", "Video analysis is not configured on the server.")
			}
			text = fmt.Sprintf(b.tr("🎬 <b>Видео обработано</b>: «%s»\n%s · %d×%d\n%s\n%s", "🎬 <b>Video processed</b>: “%s”\n%s · %d×%d\n%s\n%s"),
				title, clock(n.DurationSeconds), n.Width, n.Height, next, b.postLink(id))
		} else {
			text = fmt.Sprintf(b.tr("❌ <b>Не удалось обработать видео</b>: «%s»\n%s\n%s", "❌ <b>Video processing failed</b>: “%s”\n%s\n%s"),
				title, code(n.Error), b.postLink(id))
		}
		b.broadcast(ctx, text)
	}
}

// AnalysisFinished implements pipeline.Notifier.
func (b *Bot) AnalysisFinished(ctx context.Context, n pipeline.AnalysisNotice) {
	p, ok := b.post(ctx, n.PostID)
	if !ok {
		return
	}
	title := html.EscapeString(p.Title)
	if !n.OK {
		b.broadcast(ctx, fmt.Sprintf(b.tr("⚠️ <b>Анализ не удался</b>: «%s»\n%s\n%s", "⚠️ <b>Analysis failed</b>: “%s”\n%s\n%s"),
			title, code(n.Error), b.postLink(n.PostID)))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, b.tr("✨ <b>Анализ готов</b>: «%s»\n", "✨ <b>Analysis ready</b>: “%s”\n"), title)
	if n.Result != nil {
		if s := strings.TrimSpace(n.Result.Summary); s != "" {
			if utf8.RuneCountInString(s) > 600 {
				s = string([]rune(s)[:600]) + "…"
			}
			sb.WriteString("\n<i>" + html.EscapeString(s) + "</i>\n")
		}
		groups := map[string][]string{}
		for _, t := range n.Result.Tags(b.Lang) {
			groups[t.Kind] = append(groups[t.Kind], html.EscapeString(t.Name))
		}
		sb.WriteString("\n")
		for _, g := range []struct{ kind, ru, en string }{
			{store.TagFocus, "Направленность", "Focus"},
			{store.TagBodyArea, "Зоны тела", "Body areas"},
			{store.TagLevel, "Уровень", "Level"},
			{store.TagPose, "Позы", "Poses"},
		} {
			names := groups[g.kind]
			if len(names) == 0 {
				continue
			}
			more := ""
			if len(names) > 12 {
				more = fmt.Sprintf(" +%d", len(names)-12)
				names = names[:12]
			}
			fmt.Fprintf(&sb, "<b>%s:</b> %s%s\n", b.tr(g.ru, g.en), strings.Join(names, ", "), more)
		}
	}
	if p.Status == store.PostDraft {
		sb.WriteString(b.tr("\nПост в черновиках — проверьте теги и опубликуйте.\n", "\nThe post is a draft: check the tags and publish it.\n"))
	}
	sb.WriteString(b.postLink(n.PostID))
	b.broadcast(ctx, sb.String()) // capped above to stay under Telegram's 4096-char limit
}
