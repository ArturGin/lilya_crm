-- Initial schema.
--
-- Design notes
--  * posts are the unit of content. Anything that bundles posts later
--    (programs, courses, weekly plans, subscriptions) should reference posts
--    through its own join table instead of adding columns here.
--  * media_assets are storage-agnostic: they hold a storage *key*, never a path
--    or URL, so the same rows work with local disk today and S3/CDN later.
--  * completions are an append-only event log, so weekly goals / streaks /
--    checklists can be computed from it later without schema changes.

CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    email         CITEXT      NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    display_name  TEXT        NOT NULL DEFAULT '',
    role          TEXT        NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'admin')),
    disabled_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Opaque session tokens (only the SHA-256 is stored). Used as an HttpOnly
-- cookie by the web client and as a Bearer token by mobile / bots.
CREATE TABLE sessions (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   BYTEA       NOT NULL UNIQUE,
    client       TEXT        NOT NULL DEFAULT 'web',
    user_agent   TEXT        NOT NULL DEFAULT '',
    ip           TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE media_assets (
    id                BIGSERIAL PRIMARY KEY,
    kind              TEXT        NOT NULL CHECK (kind IN ('video', 'image')),
    storage_key       TEXT        NOT NULL UNIQUE, -- original upload
    playback_key      TEXT,                        -- web-playable rendition (videos)
    original_filename TEXT        NOT NULL DEFAULT '',
    content_type      TEXT        NOT NULL DEFAULT 'application/octet-stream',
    size_bytes        BIGINT      NOT NULL DEFAULT 0,
    status            TEXT        NOT NULL DEFAULT 'ready'
                      CHECK (status IN ('uploaded', 'processing', 'ready', 'failed')),
    error             TEXT,
    duration_seconds  INT,
    width             INT,
    height            INT,
    poster_asset_id   BIGINT REFERENCES media_assets (id) ON DELETE SET NULL,
    uploaded_by       BIGINT REFERENCES users (id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE posts (
    id               BIGSERIAL PRIMARY KEY,
    title            TEXT        NOT NULL,
    body             TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
    video_asset_id   BIGINT REFERENCES media_assets (id) ON DELETE SET NULL,
    duration_seconds INT, -- denormalised from the video for fast filtering
    author_id        BIGINT REFERENCES users (id) ON DELETE SET NULL,
    published_at     TIMESTAMPTZ,
    search_tsv       TSVECTOR    NOT NULL DEFAULT ''::tsvector,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX posts_search_idx ON posts USING GIN (search_tsv);
CREATE INDEX posts_status_published_idx ON posts (status, published_at DESC);
CREATE INDEX posts_duration_idx ON posts (duration_seconds);

-- Extra pictures attached to a post.
CREATE TABLE post_attachments (
    post_id  BIGINT NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    asset_id BIGINT NOT NULL REFERENCES media_assets (id) ON DELETE CASCADE,
    position INT    NOT NULL DEFAULT 0,
    caption  TEXT   NOT NULL DEFAULT '',
    PRIMARY KEY (post_id, asset_id)
);

-- Every analysis run is kept; the post shows the latest one.
CREATE TABLE video_analyses (
    id            BIGSERIAL PRIMARY KEY,
    post_id       BIGINT      NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    asset_id      BIGINT      NOT NULL REFERENCES media_assets (id) ON DELETE CASCADE,
    status        TEXT        NOT NULL CHECK (status IN ('pending', 'running', 'done', 'failed', 'skipped')),
    provider      TEXT        NOT NULL DEFAULT '',
    model         TEXT        NOT NULL DEFAULT '',
    result        JSONB,
    search_text   TEXT        NOT NULL DEFAULT '',
    error         TEXT,
    input_tokens  INT,
    output_tokens INT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ
);
CREATE INDEX video_analyses_post_idx ON video_analyses (post_id, id DESC);

-- Searchable labels. kind groups them into filter facets.
CREATE TABLE tags (
    id   BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('pose', 'body_area', 'focus', 'level', 'other')),
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    UNIQUE (kind, slug)
);

CREATE TABLE post_tags (
    post_id  BIGINT NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    tag_id   BIGINT NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    source   TEXT   NOT NULL CHECK (source IN ('ai', 'manual')),
    is_primary BOOLEAN NOT NULL DEFAULT false, -- main emphasis of the practice
    position INT    NOT NULL DEFAULT 0,
    PRIMARY KEY (post_id, tag_id)
);
CREATE INDEX post_tags_tag_idx ON post_tags (tag_id);

CREATE TABLE post_completions (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    post_id      BIGINT      NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX post_completions_user_post_idx ON post_completions (user_id, post_id, completed_at DESC);
CREATE INDEX post_completions_user_time_idx ON post_completions (user_id, completed_at DESC);

-- Minimal durable job queue (SELECT ... FOR UPDATE SKIP LOCKED).
CREATE TABLE jobs (
    id           BIGSERIAL PRIMARY KEY,
    kind         TEXT        NOT NULL,
    payload      JSONB       NOT NULL DEFAULT '{}',
    status       TEXT        NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    attempts     INT         NOT NULL DEFAULT 0,
    max_attempts INT         NOT NULL DEFAULT 3,
    run_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_until TIMESTAMPTZ,
    last_error   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX jobs_pending_idx ON jobs (run_at) WHERE status IN ('queued', 'running');

-- Rebuilds the full-text document of a post from its title, tags, latest
-- analysis and body (in that order of importance).
CREATE FUNCTION refresh_post_search(p_id BIGINT) RETURNS void
    LANGUAGE sql AS
$$
UPDATE posts p
SET search_tsv =
        setweight(to_tsvector('russian', coalesce(p.title, '')), 'A') ||
        setweight(to_tsvector('russian', coalesce((
            SELECT string_agg(t.name || ' ' || replace(t.slug, '-', ' '), ' ')
            FROM post_tags pt JOIN tags t ON t.id = pt.tag_id
            WHERE pt.post_id = p.id), '')), 'B') ||
        setweight(to_tsvector('russian', coalesce((
            SELECT a.search_text
            FROM video_analyses a
            WHERE a.post_id = p.id AND a.status = 'done'
            ORDER BY a.id DESC LIMIT 1), '')), 'C') ||
        setweight(to_tsvector('russian', coalesce(p.body, '')), 'D')
WHERE p.id = p_id;
$$;
