# Practice library

A members-only library of recorded yoga and movement practices. An admin uploads a video post. The server makes the
video web-playable and runs a pluggable analysis pipeline on it: speech transcription (Whisper) → pose dictionary
matching → Claude. The pipeline produces the poses, targeted body areas and focus, which become searchable tags.
Members search and filter the library, watch practices and mark them as done.

See **[PLAN.md](PLAN.md)** for the architecture, design decisions and roadmap.

```
cmd/lilya/            entrypoint: serve | migrate | create-user | set-password | set-role
internal/
  config/             env configuration
  db/                 Postgres pool + embedded SQL migrations
  store/              data access (users, sessions, posts, media, tags, analyses, completions, jobs)
  auth/               argon2id passwords, session tokens
  storage/            Storage interface + local-disk implementation (S3 later)
  media/              ffprobe / ffmpeg wrapper
  analysis/           pipeline steps: transcribe (Whisper API), keywords (pose dictionary), claude; vocabularies
  pipeline/           background jobs: video.process → video.analyze
  worker/             Postgres-backed job runner
  httpapi/            JSON API + SPA hosting
  telegram/           admin bot: pipeline notifications, video uploads
web/                  React + TypeScript SPA (built into web/dist and embedded into the binary)
deploy/Caddyfile      reverse proxy with automatic HTTPS
```

## Local development

Requirements: Go 1.26+, Node 20.19+, PostgreSQL 14+, ffmpeg.

```bash
createdb lilya
export DATABASE_URL="postgres://$USER@localhost:5432/lilya?sslmode=disable"
export COOKIE_SECURE=false ALLOW_SIGNUP=true DATA_DIR=./data
export ANTHROPIC_API_KEY=...            # optional: enables the Claude step
# optional: speech transcription via a local Whisper server
docker run -d -p 8010:8000 -v hf-hub-cache:/home/ubuntu/.cache/huggingface/hub ghcr.io/speaches-ai/speaches:latest-cpu
curl -X POST localhost:8010/v1/models/Systran/faster-whisper-small     # one-time model download
export TRANSCRIBE_BASE_URL=http://localhost:8010/v1 TRANSCRIBE_MODEL=Systran/faster-whisper-small

go run ./cmd/lilya create-user -email you@example.com -admin   # prompts for a password
go run ./cmd/lilya serve                                        # API on :8080

cd web && npm install && npm run dev                            # UI on :5173 (proxies /api to :8080)
```

To serve everything from the Go binary instead, run `cd web && npm run build` and then `go run ./cmd/lilya serve`.
Open http://localhost:8080.

Tests:

```bash
go test ./...
createdb lilya_test && TEST_DATABASE_URL="postgres://$USER@localhost:5432/lilya_test?sslmode=disable" go test ./internal/pipeline ./internal/telegram   # use an empty database
cd web && npm run typecheck
```

The pipeline test runs the full flow (process → transcribe in chunks → keywords → refining step → tags → search →
transcript cache) against a real database, using fake transcriber and LLM steps, so it needs no API keys. The
keyword tests use real faster-whisper output, including its recognition errors.

## Deploy (single VPS, Docker Compose)

1. Point a DNS record at the server and open ports 80 and 443.
2. Set up the config:
   ```bash
   cp .env.example .env   # set DOMAIN, POSTGRES_PASSWORD, transcription + ANTHROPIC_API_KEY
   docker compose --profile whisper up -d --build   # drop "--profile whisper" if using OpenAI/Groq
   docker compose exec app lilya create-user -email you@example.com -admin
   ```
3. Caddy gets a TLS certificate automatically. Media lives in the `media` volume and the database in `pgdata`.
   Back up both.

Migrations run automatically when the server starts. Registration is closed by default (`ALLOW_SIGNUP=false`):
create members with `lilya create-user -email … [-name …]`, or open sign-up.

## Configuration

All settings are environment variables. See `.env.example`. The main ones:

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | — | required |
| `DATA_DIR` | `./data` | media + scratch space |
| `PUBLIC_URL` | `http://localhost:8080` | |
| `COOKIE_SECURE` | `true` | set `false` only for plain-HTTP local development |
| `TRUST_PROXY` | `false` | honour `X-Forwarded-For` (enable only behind your own proxy) |
| `ALLOW_SIGNUP` | `false` | open self-registration |
| `ANALYSIS_STEPS` | all available | steps that run automatically after upload |
| `ANALYSIS_LANGUAGE` | `ru` | language of summaries, pose names and the transcription hint |
| `TRANSCRIBE_BASE_URL` / `TRANSCRIBE_API_KEY` / `TRANSCRIBE_MODEL` | — / — / `whisper-1` | enables the `transcribe` step (OpenAI-compatible API) |
| `KEYWORDS_ENABLED` | `true` | the `keywords` step |
| `ANTHROPIC_API_KEY` | — | enables the `claude` step |
| `CLAUDE_MODEL` / `CLAUDE_EFFORT` | `claude-opus-5-5` / `high` | |
| `CLAUDE_INPUTS` | `transcript,frames` | what Claude looks at |
| `CLAUDE_MAX_FRAMES` | `90` | frames sampled per video (at most 1 frame per 8 s) |
| `MAX_VIDEO_MB` / `MAX_IMAGE_MB` | `4096` / `20` | |
| `TELEGRAM_BOT_TOKEN` | — | enables the admin Telegram bot |
| `TELEGRAM_API_URL` | `https://api.telegram.org` | Bot API endpoint; a local server lifts the 20 MB download limit |

## Telegram bot

With `TELEGRAM_BOT_TOKEN` set (create a bot with [@BotFather](https://t.me/BotFather)), the server runs an admin bot:

- **Notifications.** Logged-in admins get a message when a video is processed (or fails), and when its analysis is
  ready (summary, focus, body areas, poses, plus a link to the post) or fails.
- **Uploads.** An admin sends a video to the bot, and it becomes a draft post. The first line of the caption is the
  title and the rest is the description. The video then goes through the usual processing and analysis. Send videos
  *as a file* (📎 → File) so Telegram doesn't re-compress them.

**Logging in.** Open the bot and send `/login`. It asks for the email and password of your website account and
deletes the password message from the chat right away. Admins get notifications and can upload videos. Members can
log in too, but get nothing until they're made admins. `/logout` stops notifications. Changing your password, on the
site or with `lilya set-password`, also logs you out of the bot. Failed logins are rate-limited (5 per 15 minutes
per Telegram account and per email).

**Large videos.** The public Bot API only lets bots download files up to **20 MB**. To accept real practice
videos (up to 2 GB, or 4 GB with Telegram Premium), run the local Bot API server bundled as a Compose profile:

1. Get `api_id` and `api_hash` at <https://my.telegram.org> (API development tools).
2. Move the bot off the public API once: `curl https://api.telegram.org/bot<TOKEN>/logOut`.
3. In `.env`, set `TELEGRAM_API_ID`, `TELEGRAM_API_HASH` and `TELEGRAM_API_URL=http://telegram-bot-api:8081`.
4. `docker compose --profile telegram up -d` (add `--profile whisper` if you use it).

The server stores downloads on the app's `/data` volume. The app moves each video into media storage and deletes
the server's copy.

## Video analysis

Analysis is a pipeline of independent **steps**. Each step reads what earlier steps produced and adds its own
findings. Every run stores each step's status, provider/model, duration, token usage, notes and output, so tools
and models can be compared side by side in the admin UI.

| Step | Uses | Produces | Cost |
|---|---|---|---|
| `transcribe` | audio track | timestamped transcript. The transcript is cached per video, made full-text searchable, and readable by admins. | self-hosted: CPU time; OpenAI: ~$0.36 per hour of audio |
| `keywords` | transcript | poses (with timestamps), focus and body areas from the built-in RU/EN/Sanskrit pose dictionary | free |
| `claude` | transcript, frames, earlier findings | summary, level, intensity, props, cautions, and a verified pose/focus/body-area list | Claude API |

Merging: `keywords` adds its findings to what earlier steps found. `claude` refines: it sees earlier findings as
hints and returns its own verdict. For example, the keyword step reports "headstand" for "стойку на голове сегодня
не делаем" ("no headstand today"); Claude can drop it. Every pose, focus and body-area tag records which steps
found it (`origin`). Manual tag edits survive re-analysis.

Admins can re-run analysis with any combination of steps, and can force a fresh transcription.

**Adding a tool, model or approach** (pose estimation, another LLM, another speech-to-text vendor):
implement `analysis.Step` (or `analysis.Transcriber` for speech-to-text) and register it in `buildAnalysis` in
`cmd/lilya/main.go`. The DB schema, API and UI don't need changes. Pose slugs come from the shared dictionary in
`internal/analysis/poses.go`, so the same pose becomes the same tag whichever tool finds it.

## API (v1)

Responses are JSON. Errors look like `{"error":{"code":"…","message":"…"}}`.

**Auth.** Web clients get an HttpOnly cookie. Other clients pass `"client":"mobile"` (or `"telegram"`) on
login/register, get a `token`, and send `Authorization: Bearer <token>`. Non-GET requests that don't use a bearer
token must include the `X-Requested-With` header (CSRF protection).

| Method | Path | |
|---|---|---|
| POST | `/api/v1/auth/register` | `{email, password, display_name?, client?}` (if sign-up is enabled) |
| POST | `/api/v1/auth/login` | `{email, password, client?}` (rate-limited) |
| POST | `/api/v1/auth/logout` | |
| GET | `/api/v1/me` | |
| POST | `/api/v1/me/password` | `{current_password, new_password}`; signs out other sessions |
| GET | `/api/v1/config` | public client config |
| GET | `/api/v1/posts` | `q`, `min_duration`, `max_duration` (seconds), `tag=kind:slug` (repeatable, AND), `done=true\|false`, `limit`, `offset` |
| GET | `/api/v1/posts/{id}` | post with video, images, tags, analysis, own completion |
| POST | `/api/v1/posts/{id}/completions` | mark done |
| DELETE | `/api/v1/posts/{id}/completions/latest` | undo |
| GET | `/api/v1/tags` | facets with counts (published posts) |
| GET | `/api/v1/vocabulary` | focus / body-area / level terms (ru + en) |
| GET | `/api/v1/media/{id}` | video/image bytes (range requests supported) |
| | `/api/v1/admin/…` | posts CRUD, `POST posts/{id}/video` and `posts/{id}/images` (multipart, field `file`), `PUT posts/{id}/tags`, `POST posts/{id}/analysis` (`{steps?, retranscribe?}`), `GET posts/{id}/transcript`, `GET analysis/steps`, `GET tags`, `GET users` |

## Security notes

- Passwords use argon2id. Session tokens are 256-bit random values, and only their SHA-256 is stored.
- Login is rate-limited per IP and per email (failed attempts only). Unknown emails take the same time to reject as
  wrong passwords.
- Members can only reach media that belongs to published posts. Drafts and their files are admin-only.
- The CSP, `nosniff` and HSTS (via Caddy) headers are set.
- Not built yet: password reset by email (admins use `lilya set-password`), and 2FA.
