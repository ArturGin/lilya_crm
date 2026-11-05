package analysis

import (
	"context"
	"fmt"
	"sync"

	"github.com/lilya/practice/internal/media"
)

// sampleFrames grabs n evenly spaced frames across the whole video
// (at most one every 8 s, at least 8).
func sampleFrames(ctx context.Context, ff media.FFmpeg, src string, duration, maxFrames, width int) ([]Frame, error) {
	if duration <= 0 {
		return nil, fmt.Errorf("%w: unknown video duration", ErrPermanent)
	}
	n := max(8, min(duration/8, maxFrames))
	interval := float64(duration) / float64(n)
	frames := make([]Frame, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			at := (float64(i) + 0.5) * interval
			jpg, err := ff.Frame(ctx, src, at, width)
			frames[i] = Frame{AtSeconds: at, JPEG: jpg}
			errs[i] = err
		}()
	}
	wg.Wait()

	out := frames[:0]
	var firstErr error
	for i, f := range frames {
		if errs[i] != nil || len(f.JPEG) == 0 {
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		out = append(out, f)
	}
	if len(out) < n/2 {
		return nil, fmt.Errorf("frame extraction failed (%d/%d frames): %v", len(out), n, firstErr)
	}
	return out, nil
}
