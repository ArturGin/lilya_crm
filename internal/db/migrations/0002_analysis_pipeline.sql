-- Pluggable analysis pipeline.
--
-- An analysis run executes a list of steps (transcribe, keywords, claude, ...).
-- Each step's status, provider/model, usage and output fragment is kept in
-- video_analyses.steps; the merged result stays in video_analyses.result.

ALTER TABLE video_analyses
    ADD COLUMN steps           JSONB  NOT NULL DEFAULT '[]',
    ADD COLUMN requested_steps TEXT[];          -- NULL = server default

-- Transcripts are expensive, so they are cached per asset + provider + model
-- and reused by later analysis runs.
CREATE TABLE media_transcripts (
    id               BIGSERIAL PRIMARY KEY,
    asset_id         BIGINT      NOT NULL REFERENCES media_assets (id) ON DELETE CASCADE,
    provider         TEXT        NOT NULL,
    model            TEXT        NOT NULL,
    language         TEXT        NOT NULL DEFAULT '',
    text             TEXT        NOT NULL,
    segments         JSONB       NOT NULL DEFAULT '[]',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (asset_id, provider, model)
);

-- Which analysis step(s) produced an AI tag, e.g. "keywords,claude".
ALTER TABLE post_tags ADD COLUMN origin TEXT NOT NULL DEFAULT '';
