// Package media wraps ffprobe/ffmpeg for probing, transcoding and frame extraction.
package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

type FFmpeg struct {
	FFmpegPath  string
	FFprobePath string
}

type ProbeResult struct {
	DurationSeconds float64
	Width, Height   int
	VideoCodec      string
	AudioCodec      string
	FormatName      string
	HasVideo        bool
}

func (f FFmpeg) Probe(ctx context.Context, path string) (ProbeResult, error) {
	out, err := run(ctx, f.FFprobePath, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return ProbeResult{}, err
	}
	var raw struct {
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			Duration     string `json:"duration"`
			SideDataList []struct {
				Rotation float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return ProbeResult{}, fmt.Errorf("parse ffprobe output: %w", err)
	}
	var p ProbeResult
	p.FormatName = raw.Format.FormatName
	p.DurationSeconds, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if p.HasVideo || s.CodecName == "mjpeg" || s.CodecName == "png" { // skip cover art
				continue
			}
			p.HasVideo = true
			p.VideoCodec = s.CodecName
			p.Width, p.Height = s.Width, s.Height
			for _, sd := range s.SideDataList {
				if r := math.Abs(sd.Rotation); r == 90 || r == 270 {
					p.Width, p.Height = p.Height, p.Width
				}
			}
			if p.DurationSeconds == 0 {
				p.DurationSeconds, _ = strconv.ParseFloat(s.Duration, 64)
			}
		case "audio":
			if p.AudioCodec == "" {
				p.AudioCodec = s.CodecName
			}
		}
	}
	return p, nil
}

// CanRemux reports whether the source is already browser-friendly
// (H.264 + AAC in an MP4/MOV container) and only needs a faststart remux.
func (p ProbeResult) CanRemux() bool {
	container := strings.Contains(p.FormatName, "mp4") || strings.Contains(p.FormatName, "mov")
	audioOK := p.AudioCodec == "" || p.AudioCodec == "aac"
	return container && p.VideoCodec == "h264" && audioOK
}

// Progress is called periodically (~every 0.5 s) while ffmpeg works, with the
// number of seconds of output written so far and ffmpeg's speed (e.g. "1.8x").
type Progress func(doneSeconds float64, speed string)

// MakePlayable writes a web-playable MP4 (H.264/AAC, moov atom first) to dst.
// onProgress may be nil.
func (f FFmpeg) MakePlayable(ctx context.Context, src, dst string, p ProbeResult, onProgress Progress) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostats", "-progress", "pipe:1", "-y",
		"-i", src, "-map", "0:v:0", "-map", "0:a:0?"}
	if p.CanRemux() {
		args = append(args, "-c", "copy")
	} else {
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
			"-vf", "scale='min(1920,iw)':-2",
			"-c:a", "aac", "-b:a", "128k", "-ac", "2")
	}
	args = append(args, "-movflags", "+faststart", "-f", "mp4", dst)
	return runProgress(ctx, f.FFmpegPath, args, onProgress)
}

// ExtractAudio writes [start, start+dur) of the audio track as 16 kHz mono
// MP3 (~14 MB/hour): small enough for transcription API upload limits.
func (f FFmpeg) ExtractAudio(ctx context.Context, src, dst string, start, dur float64) error {
	_, err := run(ctx, f.FFmpegPath, "-hide_banner", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(start, 'f', 2, 64), "-t", strconv.FormatFloat(dur, 'f', 2, 64),
		"-i", src, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k", "-f", "mp3", dst)
	return err
}

// Frame grabs a single JPEG frame at the given second, scaled to width px.
func (f FFmpeg) Frame(ctx context.Context, src string, atSeconds float64, width int) ([]byte, error) {
	return run(ctx, f.FFmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-ss", strconv.FormatFloat(atSeconds, 'f', 2, 64),
		"-i", src, "-frames:v", "1",
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", width),
		"-q:v", "4", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1")
}

func run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		return nil, fmt.Errorf("%s: %w: %s", bin, err, msg)
	}
	return stdout.Bytes(), nil
}

// runProgress runs ffmpeg with "-progress pipe:1" and feeds the key=value
// progress blocks it prints on stdout to onProgress.
func runProgress(ctx context.Context, bin string, args []string, onProgress Progress) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", bin, err)
	}
	var done float64
	var speed string
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "out_time_us", "out_time_ms": // both are microseconds (old ffmpeg mislabels it)
			if us, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && us > 0 {
				done = float64(us) / 1e6
			}
		case "speed":
			speed = strings.TrimSpace(v)
		case "progress": // end of one progress block
			if onProgress != nil {
				onProgress(done, speed)
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		return fmt.Errorf("%s: %w: %s", bin, err, msg)
	}
	return nil
}
