package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const (
	TagPose     = "pose"
	TagBodyArea = "body_area"
	TagFocus    = "focus"
	TagLevel    = "level"
	TagOther    = "other"

	TagSourceAI     = "ai"
	TagSourceManual = "manual"
)

func ValidTagKind(k string) bool {
	switch k {
	case TagPose, TagBodyArea, TagFocus, TagLevel, TagOther:
		return true
	}
	return false
}

type PostTag struct {
	Kind    string `json:"kind"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Primary bool   `json:"primary"`
	Source  string `json:"source"`
	// Origin lists the analysis steps that produced an AI tag ("keywords,claude").
	Origin string `json:"origin,omitempty"`
}

type TagInput struct {
	Kind    string
	Slug    string
	Name    string
	Primary bool
	Origin  string
}

type TagFacet struct {
	Kind  string `json:"kind"`
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (s *Store) tagsForPosts(ctx context.Context, ids []int64) (map[int64][]PostTag, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT pt.post_id, t.kind, t.slug, t.name, pt.is_primary, pt.source, pt.origin
		FROM post_tags pt JOIN tags t ON t.id = pt.tag_id
		WHERE pt.post_id = ANY($1)
		ORDER BY pt.post_id, t.kind, pt.is_primary DESC, pt.position, t.name`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]PostTag{}
	for rows.Next() {
		var id int64
		var t PostTag
		if err := rows.Scan(&id, &t.Kind, &t.Slug, &t.Name, &t.Primary, &t.Source, &t.Origin); err != nil {
			return nil, err
		}
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}

func upsertTag(ctx context.Context, tx pgx.Tx, t TagInput) (int64, error) {
	var id int64
	// Keep the existing display name of a tag: names of shared tags are
	// curated by admins and should not flip with every AI run.
	err := tx.QueryRow(ctx, `
		INSERT INTO tags (kind, slug, name) VALUES ($1, $2, $3)
		ON CONFLICT (kind, slug) DO UPDATE SET kind = EXCLUDED.kind
		RETURNING id`, t.Kind, t.Slug, t.Name).Scan(&id)
	return id, err
}

// ReplaceAITags swaps the AI-sourced tags of a post, leaving manual tags intact.
func (s *Store) ReplaceAITags(ctx context.Context, postID int64, tags []TagInput) error {
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM post_tags WHERE post_id = $1 AND source = 'ai'`, postID); err != nil {
			return err
		}
		for i, t := range tags {
			id, err := upsertTag(ctx, tx, t)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO post_tags (post_id, tag_id, source, is_primary, position, origin)
				VALUES ($1, $2, 'ai', $3, $4, $5)
				ON CONFLICT (post_id, tag_id) DO NOTHING`, postID, id, t.Primary, i, t.Origin); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.RefreshSearch(ctx, postID)
}

// SetPostTags replaces the full tag list of a post (admin edit). Tags that
// were already present keep their source; new ones are marked manual.
func (s *Store) SetPostTags(ctx context.Context, postID int64, tags []TagInput) error {
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts WHERE id = $1)`, postID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		type prevTag struct{ source, origin string }
		prev := map[int64]prevTag{}
		rows, err := tx.Query(ctx, `SELECT tag_id, source, origin FROM post_tags WHERE post_id = $1`, postID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var pt prevTag
			if err := rows.Scan(&id, &pt.source, &pt.origin); err != nil {
				rows.Close()
				return err
			}
			prev[id] = pt
		}
		rows.Close()
		if _, err := tx.Exec(ctx, `DELETE FROM post_tags WHERE post_id = $1`, postID); err != nil {
			return err
		}
		for i, t := range tags {
			id, err := upsertTag(ctx, tx, t)
			if err != nil {
				return err
			}
			src, origin := TagSourceManual, ""
			if p, ok := prev[id]; ok {
				src, origin = p.source, p.origin
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO post_tags (post_id, tag_id, source, is_primary, position, origin)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (post_id, tag_id) DO NOTHING`, postID, id, src, t.Primary, i, origin); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.RefreshSearch(ctx, postID)
}

// TagFacets lists tags with the number of posts using them. When
// publishedOnly is set, only published posts are counted and unused tags are
// omitted.
func (s *Store) TagFacets(ctx context.Context, publishedOnly bool) ([]TagFacet, error) {
	q := `
		SELECT t.kind, t.slug, t.name, count(p.id)
		FROM tags t
		LEFT JOIN post_tags pt ON pt.tag_id = t.id
		LEFT JOIN posts p ON p.id = pt.post_id AND ($1 = false OR p.status = 'published')
		GROUP BY t.id
		HAVING ($1 = false OR count(p.id) > 0)
		ORDER BY t.kind, count(p.id) DESC, t.name`
	rows, err := s.pool.Query(ctx, q, publishedOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TagFacet{}
	for rows.Next() {
		var f TagFacet
		if err := rows.Scan(&f.Kind, &f.Slug, &f.Name, &f.Count); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
