package store

import (
	"context"
	"encoding/json"
	"time"
)

type Job struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	Attempts    int
	MaxAttempts int
}

func (s *Store) EnqueueJob(ctx context.Context, kind string, payload any, maxAttempts int) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx, `INSERT INTO jobs (kind, payload, max_attempts) VALUES ($1, $2, $3) RETURNING id`,
		kind, b, maxAttempts).Scan(&id)
	return id, err
}

// ClaimJob leases the next runnable job. Jobs whose lease expired (e.g. the
// process crashed mid-run) are picked up again while attempts remain.
func (s *Store) ClaimJob(ctx context.Context, lease time.Duration) (*Job, error) {
	var j Job
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		UPDATE jobs SET status = 'running', attempts = attempts + 1,
		                locked_until = now() + $1::interval, updated_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE attempts < max_attempts
			  AND ((status = 'queued' AND run_at <= now())
			    OR (status = 'running' AND locked_until < now()))
			ORDER BY run_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1)
		RETURNING id, kind, payload, attempts, max_attempts`, lease.String()).
		Scan(&j.ID, &j.Kind, &payload, &j.Attempts, &j.MaxAttempts)
	if err != nil {
		if mapErr(err) == ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	j.Payload = payload
	return &j, nil
}

func (s *Store) ExtendJobLease(ctx context.Context, id int64, lease time.Duration) error {
	_, err := s.pool.Exec(ctx, `UPDATE jobs SET locked_until = now() + $2::interval WHERE id = $1 AND status = 'running'`,
		id, lease.String())
	return err
}

func (s *Store) CompleteJob(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE jobs SET status = 'done', locked_until = NULL, last_error = NULL, updated_at = now() WHERE id = $1`, id)
	return err
}

// FailJob records an error and either schedules a retry or fails permanently.
func (s *Store) FailJob(ctx context.Context, id int64, msg string, retryAt *time.Time) error {
	if retryAt != nil {
		_, err := s.pool.Exec(ctx, `
			UPDATE jobs SET status = 'queued', run_at = $3, locked_until = NULL, last_error = $2, updated_at = now()
			WHERE id = $1`, id, msg, *retryAt)
		return err
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'failed', locked_until = NULL, last_error = $2, updated_at = now()
		WHERE id = $1`, id, msg)
	return err
}

// ReapStuckJobs fails jobs whose lease expired after their last attempt.
func (s *Store) ReapStuckJobs(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'failed', last_error = coalesce(last_error, 'lease expired'), updated_at = now()
		WHERE status = 'running' AND locked_until < now() AND attempts >= max_attempts`)
	return tag.RowsAffected(), err
}
