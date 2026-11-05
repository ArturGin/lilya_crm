package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

const (
	AssetVideo = "video"
	AssetImage = "image"

	AssetUploaded   = "uploaded"
	AssetProcessing = "processing"
	AssetReady      = "ready"
	AssetFailed     = "failed"
)

type Asset struct {
	ID               int64     `json:"id"`
	Kind             string    `json:"kind"`
	StorageKey       string    `json:"-"`
	PlaybackKey      *string   `json:"-"`
	OriginalFilename string    `json:"original_filename"`
	ContentType      string    `json:"content_type"`
	SizeBytes        int64     `json:"size_bytes"`
	Status           string    `json:"status"`
	Error            *string   `json:"error,omitempty"`
	DurationSeconds  *int      `json:"duration_seconds,omitempty"`
	Width            *int      `json:"width,omitempty"`
	Height           *int      `json:"height,omitempty"`
	PosterAssetID    *int64    `json:"poster_asset_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

const assetCols = `id, kind, storage_key, playback_key, original_filename, content_type, size_bytes,
	status, error, duration_seconds, width, height, poster_asset_id, created_at`

func scanAsset(row interface{ Scan(...any) error }) (Asset, error) {
	var a Asset
	err := row.Scan(&a.ID, &a.Kind, &a.StorageKey, &a.PlaybackKey, &a.OriginalFilename, &a.ContentType,
		&a.SizeBytes, &a.Status, &a.Error, &a.DurationSeconds, &a.Width, &a.Height, &a.PosterAssetID, &a.CreatedAt)
	return a, mapErr(err)
}

type NewAsset struct {
	Kind             string
	StorageKey       string
	OriginalFilename string
	ContentType      string
	SizeBytes        int64
	Status           string
	Width, Height    *int
	UploadedBy       *int64
}

func createAsset(ctx context.Context, q querier, n NewAsset) (Asset, error) {
	return scanAsset(q.QueryRow(ctx, `
		INSERT INTO media_assets (kind, storage_key, original_filename, content_type, size_bytes, status, width, height, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+assetCols,
		n.Kind, n.StorageKey, n.OriginalFilename, n.ContentType, n.SizeBytes, n.Status, n.Width, n.Height, n.UploadedBy))
}

func (s *Store) CreateAsset(ctx context.Context, n NewAsset) (Asset, error) {
	return createAsset(ctx, s.pool, n)
}

func (s *Store) AssetByID(ctx context.Context, id int64) (Asset, error) {
	return scanAsset(s.pool.QueryRow(ctx, `SELECT `+assetCols+` FROM media_assets WHERE id = $1`, id))
}

func (s *Store) SetAssetStatus(ctx context.Context, id int64, status string, errMsg *string) error {
	_, err := s.pool.Exec(ctx, `UPDATE media_assets SET status = $2, error = $3, updated_at = now() WHERE id = $1`, id, status, errMsg)
	return err
}

type ProcessedVideo struct {
	PlaybackKey     string
	SizeBytes       int64
	DurationSeconds int
	Width, Height   int
	PosterAssetID   *int64
}

// MarkVideoReady stores the processing results and propagates the duration to
// every post that uses this video.
func (s *Store) MarkVideoReady(ctx context.Context, id int64, p ProcessedVideo) error {
	_, err := s.pool.Exec(ctx, `
		WITH a AS (
			UPDATE media_assets
			SET status = 'ready', error = NULL, playback_key = $2, duration_seconds = $3,
			    width = $4, height = $5, poster_asset_id = $6, updated_at = now()
			WHERE id = $1
			RETURNING id, duration_seconds)
		UPDATE posts SET duration_seconds = a.duration_seconds, updated_at = now()
		FROM a WHERE posts.video_asset_id = a.id`,
		id, p.PlaybackKey, p.DurationSeconds, p.Width, p.Height, p.PosterAssetID)
	return err
}

// DeleteAssets removes asset rows and returns them (so files can be removed).
// Posters of deleted videos are included.
func (s *Store) DeleteAssets(ctx context.Context, ids []int64) ([]Asset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		DELETE FROM media_assets
		WHERE id = ANY($1)
		   OR id IN (SELECT poster_asset_id FROM media_assets WHERE id = ANY($1) AND poster_asset_id IS NOT NULL)
		RETURNING `+assetCols, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MemberCanViewAsset reports whether the asset belongs to a published post
// (as its video, the video's poster, or an attached image).
func (s *Store) MemberCanViewAsset(ctx context.Context, assetID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM posts p
			LEFT JOIN media_assets v ON v.id = p.video_asset_id
			WHERE p.status = 'published'
			  AND (p.video_asset_id = $1
			       OR v.poster_asset_id = $1
			       OR EXISTS (SELECT 1 FROM post_attachments pa WHERE pa.post_id = p.id AND pa.asset_id = $1)))`,
		assetID).Scan(&ok)
	return ok, err
}

func (s *Store) PostIDsByVideo(ctx context.Context, assetID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM posts WHERE video_asset_id = $1`, assetID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}
