package pipeline

import (
	"context"
	"time"

	"github.com/lilya/practice/internal/analysis"
)

// Notifier is told about pipeline outcomes, e.g. to message admins on
// Telegram. Calls run in their own goroutine and must not block for long.
type Notifier interface {
	VideoProcessed(ctx context.Context, n VideoNotice)
	AnalysisFinished(ctx context.Context, n AnalysisNotice)
}

// VideoNotice reports that processing of an uploaded video finished
// (successfully or, after the last retry, with an error).
type VideoNotice struct {
	AssetID         int64
	PostIDs         []int64
	OK              bool
	Error           string
	DurationSeconds int
	Width, Height   int
	AnalysisQueued  bool
}

// AnalysisNotice reports that an analysis run finished (successfully or,
// after the last retry, with an error).
type AnalysisNotice struct {
	AnalysisID int64
	PostID     int64
	OK         bool
	Error      string
	Result     *analysis.Result
	Took       time.Duration
}

func (p *Pipeline) notify(fn func(ctx context.Context, n Notifier)) {
	if p.Notify == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		fn(ctx, p.Notify)
	}()
}
