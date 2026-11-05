package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

const (
	PostDraft     = "draft"
	PostPublished = "published"
	PostArchived  = "archived"
)

func ValidPostStatus(s string) bool {
	return s == PostDraft || s == PostPublished || s == PostArchived
}

type CompletionInfo struct {
	Count           int        `json:"count"`
	LastCompletedAt *time.Time `json:"last_completed_at"`
}

type Attachment struct {
	Asset    Asset  `json:"asset"`
	Caption  string `json:"caption"`
	Position int    `json:"position"`
}

type PostDetail struct {
	ID              int64          `json:"id"`
	Title           string         `json:"title"`
	Body            string         `json:"body"`
	Status          string         `json:"status"`
	DurationSeconds *int           `json:"duration_seconds"`
	PublishedAt     *time.Time     `json:"published_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Video           *Asset         `json:"video"`
	Images          []Attachment   `json:"images"`
	Tags            []PostTag      `json:"tags"`
	Analysis        *Analysis      `json:"analysis"`
	Completion      CompletionInfo `json:"completion"`
}

type PostCard struct {
	ID              int64          `json:"id"`
	Title           string         `json:"title"`
	Status          string         `json:"status"`
	DurationSeconds *int           `json:"duration_seconds"`
	PublishedAt     *time.Time     `json:"published_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	PosterAssetID   *int64         `json:"poster_asset_id"`
	VideoStatus     *string        `json:"video_status"`
	AnalysisStatus  *string        `json:"analysis_status"`
	Tags            []PostTag      `json:"tags"`
	Completion      CompletionInfo `json:"completion"`
}

func (s *Store) CreatePost(ctx context.Context, title, body string, authorID int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO posts (title, body, author_id) VALUES ($1, $2, $3) RETURNING id`,
		title, body, authorID).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = s.pool.Exec(ctx, `SELECT refresh_post_search($1)`, id)
	return id, err
}

type PostUpdate struct {
	Title  *string
	Body   *string
	Status *string
}

func (s *Store) UpdatePost(ctx context.Context, id int64, u PostUpdate) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE posts SET
			title  = coalesce($2, title),
			body   = coalesce($3, body),
			status = coalesce($4, status),
			published_at = CASE WHEN coalesce($4, status) = 'published' AND published_at IS NULL THEN now() ELSE published_at END,
			updated_at = now()
		WHERE id = $1`, id, u.Title, u.Body, u.Status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return s.RefreshSearch(ctx, id)
}

func (s *Store) RefreshSearch(ctx context.Context, postID int64) error {
	_, err := s.pool.Exec(ctx, `SELECT refresh_post_search($1)`, postID)
	return err
}

// PostAssetIDs returns every asset owned by a post (video + attachments).
func (s *Store) PostAssetIDs(ctx context.Context, postID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT video_asset_id FROM posts WHERE id = $1 AND video_asset_id IS NOT NULL
		UNION
		SELECT asset_id FROM post_attachments WHERE post_id = $1`, postID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

func (s *Store) DeletePost(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM posts WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AttachVideo creates a video asset and makes it the post's video, returning
// the new asset and the id of the replaced video (if any).
func (s *Store) AttachVideo(ctx context.Context, postID int64, n NewAsset) (Asset, *int64, error) {
	var asset Asset
	var oldID *int64
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT video_asset_id FROM posts WHERE id = $1 FOR UPDATE`, postID).Scan(&oldID); err != nil {
			return mapErr(err)
		}
		var err error
		asset, err = createAsset(ctx, tx, n)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE posts SET video_asset_id = $2, duration_seconds = NULL, updated_at = now() WHERE id = $1`, postID, asset.ID)
		return err
	})
	return asset, oldID, err
}

func (s *Store) AddAttachment(ctx context.Context, postID int64, n NewAsset, caption string) (Asset, error) {
	var asset Asset
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts WHERE id = $1)`, postID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		var err error
		asset, err = createAsset(ctx, tx, n)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO post_attachments (post_id, asset_id, caption, position)
			VALUES ($1, $2, $3, (SELECT coalesce(max(position), -1) + 1 FROM post_attachments WHERE post_id = $1))`,
			postID, asset.ID, caption)
		return err
	})
	return asset, err
}

func (s *Store) IsAttachment(ctx context.Context, postID, assetID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM post_attachments WHERE post_id = $1 AND asset_id = $2)`,
		postID, assetID).Scan(&ok)
	return ok, err
}

// GetPost loads a post with everything needed to render it. userID is used
// for completion info (0 = none).
func (s *Store) GetPost(ctx context.Context, id, userID int64) (PostDetail, error) {
	var p PostDetail
	var videoID *int64
	err := s.pool.QueryRow(ctx, `
		SELECT id, title, body, status, duration_seconds, published_at, created_at, updated_at, video_asset_id
		FROM posts WHERE id = $1`, id).
		Scan(&p.ID, &p.Title, &p.Body, &p.Status, &p.DurationSeconds, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt, &videoID)
	if err != nil {
		return p, mapErr(err)
	}
	if videoID != nil {
		v, err := s.AssetByID(ctx, *videoID)
		if err != nil && err != ErrNotFound {
			return p, err
		}
		if err == nil {
			p.Video = &v
		}
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+prefixCols("a.", assetCols)+`, pa.caption, pa.position
		FROM post_attachments pa JOIN media_assets a ON a.id = pa.asset_id
		WHERE pa.post_id = $1 ORDER BY pa.position, a.id`, id)
	if err != nil {
		return p, err
	}
	p.Images = []Attachment{}
	for rows.Next() {
		var at Attachment
		a := &at.Asset
		if err := rows.Scan(&a.ID, &a.Kind, &a.StorageKey, &a.PlaybackKey, &a.OriginalFilename, &a.ContentType,
			&a.SizeBytes, &a.Status, &a.Error, &a.DurationSeconds, &a.Width, &a.Height, &a.PosterAssetID, &a.CreatedAt,
			&at.Caption, &at.Position); err != nil {
			rows.Close()
			return p, err
		}
		p.Images = append(p.Images, at)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return p, err
	}

	tags, err := s.tagsForPosts(ctx, []int64{id})
	if err != nil {
		return p, err
	}
	p.Tags = tags[id]
	if p.Tags == nil {
		p.Tags = []PostTag{}
	}

	a, err := s.LatestAnalysis(ctx, id)
	if err != nil && err != ErrNotFound {
		return p, err
	}
	if err == nil {
		p.Analysis = &a
	}

	if userID != 0 {
		c, err := s.completionInfo(ctx, userID, []int64{id})
		if err != nil {
			return p, err
		}
		p.Completion = c[id]
	}
	return p, nil
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// TagRef identifies a tag by kind and slug, e.g. body_area:hips.
type TagRef struct {
	Kind string
	Slug string
}

type ListFilter struct {
	Query       string
	MinDuration *int // seconds, inclusive
	MaxDuration *int // seconds, inclusive
	Tags        []TagRef
	// Done filters by the requesting user's completion state.
	Done   *bool
	UserID int64
	// Statuses restricts post status; members always get only "published".
	Statuses []string
	Limit    int
	Offset   int
}

// ListPosts returns cards matching the filter plus the total count.
func (s *Store) ListPosts(ctx context.Context, f ListFilter) ([]PostCard, int, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if len(f.Statuses) > 0 {
		where = append(where, "p.status = ANY("+arg(f.Statuses)+")")
	}
	tsq := BuildTSQuery(f.Query)
	rank := "0::real"
	if tsq != "" {
		ph := arg(tsq)
		where = append(where, "p.search_tsv @@ to_tsquery('russian', "+ph+")")
		rank = "ts_rank(p.search_tsv, to_tsquery('russian', " + ph + "))"
	}
	if f.MinDuration != nil {
		where = append(where, "p.duration_seconds >= "+arg(*f.MinDuration))
	}
	if f.MaxDuration != nil {
		where = append(where, "p.duration_seconds <= "+arg(*f.MaxDuration))
	}
	for _, t := range f.Tags {
		where = append(where, fmt.Sprintf(`EXISTS (SELECT 1 FROM post_tags pt JOIN tags t ON t.id = pt.tag_id
			WHERE pt.post_id = p.id AND t.kind = %s AND t.slug = %s)`, arg(t.Kind), arg(t.Slug)))
	}
	if f.Done != nil && f.UserID != 0 {
		not := "NOT "
		if *f.Done {
			not = ""
		}
		where = append(where, not+"EXISTS (SELECT 1 FROM post_completions c WHERE c.post_id = p.id AND c.user_id = "+arg(f.UserID)+")")
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM posts p `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 24
	}
	q := `
		SELECT p.id, p.title, p.status, p.duration_seconds, p.published_at, p.updated_at,
		       v.poster_asset_id, v.status,
		       (SELECT a.status FROM video_analyses a WHERE a.post_id = p.id ORDER BY a.id DESC LIMIT 1)
		FROM posts p LEFT JOIN media_assets v ON v.id = p.video_asset_id
		` + whereSQL + `
		ORDER BY ` + rank + ` DESC, coalesce(p.published_at, p.created_at) DESC, p.id DESC
		LIMIT ` + arg(limit) + ` OFFSET ` + arg(max(f.Offset, 0))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	cards := []PostCard{}
	var ids []int64
	for rows.Next() {
		var c PostCard
		if err := rows.Scan(&c.ID, &c.Title, &c.Status, &c.DurationSeconds, &c.PublishedAt, &c.UpdatedAt,
			&c.PosterAssetID, &c.VideoStatus, &c.AnalysisStatus); err != nil {
			return nil, 0, err
		}
		cards = append(cards, c)
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(ids) == 0 {
		return cards, total, nil
	}

	tags, err := s.tagsForPosts(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	var comp map[int64]CompletionInfo
	if f.UserID != 0 {
		if comp, err = s.completionInfo(ctx, f.UserID, ids); err != nil {
			return nil, 0, err
		}
	}
	for i := range cards {
		cards[i].Tags = tags[cards[i].ID]
		if cards[i].Tags == nil {
			cards[i].Tags = []PostTag{}
		}
		cards[i].Completion = comp[cards[i].ID]
	}
	return cards, total, nil
}

// BuildTSQuery turns free user input into a safe prefix-matching tsquery
// string ("word1:* & word2:*"). Returns "" if nothing searchable remains.
func BuildTSQuery(input string) string {
	words := strings.FieldsFunc(strings.ToLower(input), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var terms []string
	for _, w := range words {
		if len(terms) >= 8 {
			break
		}
		if w == "" {
			continue
		}
		terms = append(terms, w+":*")
	}
	return strings.Join(terms, " & ")
}
