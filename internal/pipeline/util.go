package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/lilya/practice/internal/media"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func mb(n int64) string { return fmt.Sprintf("%.1f", float64(n)/(1<<20)) }

// fmtDur renders seconds as "1h02m03s" / "34m27s".
func fmtDur(sec float64) string {
	return (time.Duration(math.Round(sec)) * time.Second).String()
}

// progressLogger returns an ffmpeg progress callback that logs at most every
// 15 s: how far along the output is, ffmpeg's speed and an ETA.
func progressLogger(log *slog.Logger, what string, totalSec float64) media.Progress {
	const every = 15 * time.Second
	var mu sync.Mutex
	start := time.Now()
	last := start
	return func(done float64, speed string) {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if now.Sub(last) < every {
			return
		}
		last = now
		attrs := []any{"done", fmtDur(done), "total", fmtDur(totalSec), "speed", speed,
			"elapsed", now.Sub(start).Round(time.Second).String()}
		if totalSec > 0 && done > 0 {
			frac := min(done/totalSec, 1)
			eta := time.Duration(float64(now.Sub(start)) * (1 - frac) / frac)
			attrs = append(attrs, "percent", fmt.Sprintf("%.1f", frac*100), "eta", eta.Round(time.Second).String())
		}
		log.Info(what+" progress", attrs...)
	}
}
