// Package worker runs background jobs (video processing, analysis) from the
// Postgres-backed job queue. It runs inside the server process; several
// instances can run concurrently thanks to SKIP LOCKED.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lilya/practice/internal/store"
)

// ErrPermanent marks failures that must not be retried.
var ErrPermanent = errors.New("permanent")

type Handler interface {
	Handle(ctx context.Context, payload json.RawMessage) error
	// OnFinalFailure is called once the job is given up on.
	OnFinalFailure(ctx context.Context, payload json.RawMessage, err error)
}

type Worker struct {
	store       *store.Store
	log         *slog.Logger
	handlers    map[string]Handler
	concurrency int
	lease       time.Duration
	poll        time.Duration
	wake        chan struct{}
}

func New(st *store.Store, log *slog.Logger, concurrency int) *Worker {
	return &Worker{
		store:       st,
		log:         log,
		handlers:    map[string]Handler{},
		concurrency: concurrency,
		lease:       5 * time.Minute,
		poll:        3 * time.Second,
		wake:        make(chan struct{}, 1),
	}
}

func (w *Worker) Register(kind string, h Handler) { w.handlers[kind] = h }

// Enqueue adds a job and nudges the local worker.
func (w *Worker) Enqueue(ctx context.Context, kind string, payload any) error {
	if _, err := w.store.EnqueueJob(ctx, kind, payload, 3); err != nil {
		return err
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return nil
}

// Run blocks until ctx is cancelled, then waits for in-flight jobs.
func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := w.store.ReapStuckJobs(ctx); err == nil && n > 0 {
					w.log.Warn("reaped stuck jobs", "count", n)
				}
				if n, err := w.store.DeleteExpiredSessions(ctx); err == nil && n > 0 {
					w.log.Info("deleted expired sessions", "count", n)
				}
			}
		}
	}()
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := w.store.ClaimJob(ctx, w.lease)
		if err != nil {
			if ctx.Err() == nil {
				w.log.Error("claim job", "err", err)
			}
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-w.wake:
			case <-time.After(w.poll):
			}
			continue
		}
		w.runJob(ctx, job)
	}
}

func (w *Worker) runJob(parent context.Context, job *store.Job) {
	log := w.log.With("job_id", job.ID, "kind", job.Kind, "attempt", job.Attempts)
	h, ok := w.handlers[job.Kind]
	if !ok {
		_ = w.store.FailJob(context.Background(), job.ID, "no handler for job kind", nil)
		log.Error("no handler for job kind")
		return
	}

	// Jobs keep running on shutdown only until the lease would be retried
	// anyway; cancel them with the parent and let another instance resume.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() { // heartbeat: keep the lease while the job is alive
		t := time.NewTicker(w.lease / 3)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = w.store.ExtendJobLease(context.Background(), job.ID, w.lease)
			}
		}
	}()

	log.Info("job started", "payload", string(job.Payload), "max_attempts", job.MaxAttempts)
	start := time.Now()
	err := safeHandle(ctx, h, job.Payload)
	bg := context.Background()
	if err == nil {
		_ = w.store.CompleteJob(bg, job.ID)
		log.Info("job done", "took", time.Since(start).Round(time.Millisecond).String())
		return
	}
	if parent.Err() != nil {
		// Shutting down: leave the job leased; it will be retried after the lease expires.
		log.Warn("job interrupted by shutdown", "err", err)
		return
	}
	final := errors.Is(err, ErrPermanent) || job.Attempts >= job.MaxAttempts
	if final {
		_ = w.store.FailJob(bg, job.ID, err.Error(), nil)
		h.OnFinalFailure(bg, job.Payload, err)
		log.Error("job failed", "err", err)
		return
	}
	retryAt := time.Now().Add(time.Duration(job.Attempts*job.Attempts) * 30 * time.Second)
	_ = w.store.FailJob(bg, job.ID, err.Error(), &retryAt)
	log.Warn("job failed, will retry", "err", err, "retry_at", retryAt)
}

func safeHandle(ctx context.Context, h Handler, payload json.RawMessage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: panic: %v", ErrPermanent, r)
		}
	}()
	return h.Handle(ctx, payload)
}
