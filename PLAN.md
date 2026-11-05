# Plan & architecture

## What v1 does

- **Admin** creates a *post* (title + text), uploads a practice video and optional images, and publishes it.
- The server **processes** the video in the background: probe → web-playable MP4 (H.264/AAC, faststart; remux if
  already compatible, otherwise transcode) → poster frame.
- The server then **analyses** the video through a pipeline of pluggable steps:
  1. `transcribe`: the audio goes to any OpenAI-compatible Whisper API (self-hosted faster-whisper, OpenAI or
     Groq) and comes back as a timestamped transcript, cached per video and full-text searchable.
  2. `keywords`: the transcript is matched against the RU/EN/Sanskrit pose dictionary, producing poses (with
     timestamps), focus and body areas. Free and deterministic.
  3. `claude`: Claude reads the transcript and looks at sampled frames, verifies the earlier findings, and writes
     the summary, practice type, level, intensity, **focus** (e.g. hip opening, chest opening, backbends),
     **body areas** (e.g. hips / ТБС, thoracic spine), **poses**, props and cautions.

  The merged result becomes searchable **tags**, each recording which steps found it. Admins can edit the tags and
  re-run any combination of steps.
- **Members** sign in with email/password, browse the library, filter by **duration**, **focus**, **body area**,
  **level**, **pose**, and done / not done (filters combine with AND), full-text search (Russian stemming, prefix
  matching, also matches English and Sanskrit pose names), watch, jump to a pose from its timestamp, and **mark a
  practice as done** (or undo it).

## Stack

| Concern | Choice | Why |
|---|---|---|
| Backend | Go (single binary: API + worker + embedded web UI) | Simple to deploy, good at streaming large files, low memory. |
| DB | PostgreSQL | Relational data + full-text search + JSONB + a job queue (`SKIP LOCKED`), so no Redis or Elastic needed yet. |
| Video | ffmpeg / ffprobe | Standard tooling. |
| Analysis | Step pipeline: Whisper (OpenAI-compatible API; self-hosted [speaches](https://speaches.ai) in compose) + pose dictionary + Claude (`claude-opus-5-5`, official Go SDK, structured outputs) | Speech gives pose names and intent, frames show what is actually practised, and the dictionary keeps tags consistent. |
| Web | React + TypeScript + Vite SPA | Runs as-is in a Telegram Mini App webview later. |
| Deploy | Docker Compose: app + Postgres + Caddy (automatic TLS) | One VPS is enough for now. |

## Decisions that leave room for later

- **API first, clients second.** All behaviour sits behind `/api/v1`. The web app is just one client.
  - Web: HttpOnly `SameSite=Lax` session cookie plus an `X-Requested-With` header check against CSRF.
  - Mobile / Telegram: same login endpoint with `"client":"mobile"|"telegram"` → bearer token.
  - Telegram login (initData verification) can be added as another way of creating a session, plus a
    `user_identities` table that links a Telegram id to a user.
- **Storage is an interface** (`internal/storage`). The DB only stores *keys*, never paths or URLs. To add S3/R2,
  implement `Storage` and `URLSigner`; `/media/{id}` then redirects to signed URLs. To use a video host (Mux,
  Cloudflare Stream, Bunny), store its asset id as the playback key and swap the player source.
- **Analysis is a pipeline of `Step`s** (`internal/analysis/step.go`). A step declares what it uses and produces,
  and returns a result fragment that is either merged in (union) or refines everything found so far (refine).
  Steps that fail permanently are recorded and the run continues. Transient failures retry the job, and cached
  transcripts make retries cheap. Planned steps such as a pose-estimation model (MediaPipe/YOLO-pose on frames), a
  different LLM or a human-review import only need the interface implemented plus one line in `buildAnalysis`.
- **The pose dictionary is shared** (`internal/analysis/poses.go`): canonical slugs, RU/EN/Sanskrit names, match
  patterns, and the areas and focus each pose targets. The keyword step matches against it, Claude is asked to use
  its slugs, aliases are mapped onto it, and the transcription prompt uses it to spell asana names.
- **Controlled vocabularies** for focus, body areas and levels keep filters consistent. Poses are open-ended, but
  the model is asked for canonical English slugs so the same pose matches across videos. New vocabulary terms are
  appended in `vocabulary.go`.
- **Every analysis run is kept** (`video_analyses`), so prompts or models can be compared and re-run later.
- **Completions are an event log** (one row per "done"). Weekly goals, streaks and checklists can be computed
  from it without schema changes.
- **Posts don't know about bundles.** Programs, courses and subscriptions should be new tables that reference
  posts (e.g. `collections`, `collection_items`, `entitlements`), so the post model stays the same.

## Roadmap (suggested order)

1. ~~Audio transcript in the analysis~~ (done: `transcribe` + `keywords` steps; Claude reads the transcript).
   Next: grow the pose dictionary from real transcripts, and show members the transcript or chapters ("pigeon at
   12:30").
2. **Password reset by email** (needs an SMTP or transactional email provider). Until then, admins reset passwords
   with `lilya set-password`.
3. **Collections / programs**: grouping practices into series and courses, with ordering.
4. **Payments and access** (entitlements per user, per collection): Stripe / ЮKassa / Telegram Payments.
5. **Telegram bot + Mini App**: Telegram auth endpoint; the bot sends new-practice notifications and deep links.
6. **Goals and checklists**: "5 practices this week", built on `post_completions`.
7. **Video pipeline upgrades**: HLS / adaptive bitrate, S3 + CDN, or a managed video host; countdown overlay and
   branded intro (Lilya's template idea) as an optional ffmpeg step.
8. **Ops**: Postgres backups (`pg_dump` cron or managed DB), error reporting, metrics.

## Known limitations

- The keyword step is literal. It can't tell "do pigeon" from "no pigeon today", and it misses poses that are shown
  but never named. Claude refines its output when the Claude step is enabled.
- Self-hosted Whisper on CPU is slow: faster-whisper-small took 43–74 s for 50 s of audio in local tests, so a
  one-hour class may take an hour or more on a small VPS. Use `faster-whisper-base`, a GPU, or a hosted API (OpenAI
  or Groq) if that's too slow.

- The login rate limiter is in-memory, which is fine for a single instance. Move it to Postgres or Redis before
  running more than one instance.
- If the process crashes during a job's *final* retry, that job is marked failed by the reaper, but the matching
  video or analysis row can stay in `processing` / `running` status. Re-upload the video or re-run the analysis to
  fix it.
- Full-text search uses the `russian` text-search config. English words are matched by prefix, without stemming.
- Video is served from the app server's disk via HTTP range requests, which is fine for a small audience. Move to
  S3 + CDN (or a video host) as traffic grows.
