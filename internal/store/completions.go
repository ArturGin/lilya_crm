package store

import (
	"context"
	"time"
)

func (s *Store) AddCompletion(ctx context.Context, userID, postID int64) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `
		INSERT INTO post_completions (user_id, post_id) VALUES ($1, $2) RETURNING completed_at`,
		userID, postID).Scan(&at)
	return at, mapErr(err)
}

// RemoveLatestCompletion undoes the most recent "done" mark of a user on a post.
func (s *Store) RemoveLatestCompletion(ctx context.Context, userID, postID int64) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM post_completions WHERE id = (
			SELECT id FROM post_completions WHERE user_id = $1 AND post_id = $2
			ORDER BY completed_at DESC LIMIT 1)`, userID, postID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) completionInfo(ctx context.Context, userID int64, postIDs []int64) (map[int64]CompletionInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT post_id, count(*), max(completed_at)
		FROM post_completions
		WHERE user_id = $1 AND post_id = ANY($2)
		GROUP BY post_id`, userID, postIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]CompletionInfo{}
	for rows.Next() {
		var id int64
		var c CompletionInfo
		if err := rows.Scan(&id, &c.Count, &c.LastCompletedAt); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}
