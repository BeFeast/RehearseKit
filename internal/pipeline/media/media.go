// Package media wraps the external tools the CPU stages shell out to:
// ffprobe, ffmpeg and yt-dlp. Every call takes a context; cancelling it
// kills the child process.
package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tool names; overridable for tests.
var (
	FFmpeg  = "ffmpeg"
	FFprobe = "ffprobe"
	YtDlp   = "yt-dlp"
)

// ErrTooLong is returned by CheckDuration.
var ErrTooLong = errors.New("media: source is too long")

// stderrTail keeps the last n bytes of a stream for error messages.
type stderrTail struct {
	buf bytes.Buffer
	n   int
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if t.buf.Len() > t.n {
		b := t.buf.Bytes()
		t.buf = *bytes.NewBuffer(append([]byte(nil), b[len(b)-t.n:]...))
	}
	return len(p), nil
}

func (t *stderrTail) String() string { return strings.TrimSpace(t.buf.String()) }

// Run executes name with args, returning stdout and a trimmed error that
// includes the tail of stderr. Context cancellation kills the process.
func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 5 * time.Second
	tail := &stderrTail{n: 4096}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = tail
	cmd.Stdin = nil
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return out.Bytes(), ctx.Err()
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.Bytes(), fmt.Errorf("%s exited with %d: %s", filepath.Base(name), ee.ExitCode(), lastLines(tail.String(), 5))
		}
		return out.Bytes(), fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return out.Bytes(), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Available reports whether the tool binaries can be found (tests skip otherwise).
func Available(tools ...string) bool {
	for _, t := range tools {
		if _, err := exec.LookPath(t); err != nil {
			return false
		}
	}
	return true
}

// Duration returns the container duration of path in seconds via ffprobe.
func Duration(ctx context.Context, path string) (float64, error) {
	out, err := Run(ctx, FFprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" || s == "N/A" {
		return 0, errors.New("ffprobe: no duration in container")
	}
	d, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe: parse duration %q: %w", s, err)
	}
	return d, nil
}

// CheckDuration probes path and returns ErrTooLong when it exceeds max.
func CheckDuration(ctx context.Context, path string, max time.Duration) (float64, error) {
	d, err := Duration(ctx, path)
	if err != nil {
		return 0, err
	}
	if d > max.Seconds() {
		return d, fmt.Errorf("%w: %s exceeds the %s limit", ErrTooLong, fmtDuration(d), fmtDuration(max.Seconds()))
	}
	return d, nil
}

func fmtDuration(sec float64) string {
	d := time.Duration(sec * float64(time.Second)).Round(time.Second)
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%d:%02d", m, s)
}

// ConvertToStemWAV transcodes in to a 24-bit/48 kHz stereo PCM WAV at out,
// dropping any video stream. The output is written to a temp file and
// renamed into place so a killed ffmpeg never leaves a truncated target.
func ConvertToStemWAV(ctx context.Context, in, out string) error {
	tmp := out + ".part.wav"
	_, err := Run(ctx, FFmpeg, "-y", "-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", in, "-vn", "-map_metadata", "-1", "-ar", "48000", "-ac", "2", "-c:a", "pcm_s24le", "-f", "wav", tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, out)
}

// DownloadYouTube fetches the best audio of url into dir with yt-dlp and
// returns the path of the downloaded file (any audio container; the
// caller converts it).
func DownloadYouTube(ctx context.Context, url, dir string) (string, error) {
	tmpl := filepath.Join(dir, "download.%(ext)s")
	_, err := Run(ctx, YtDlp, "--no-playlist", "--no-progress", "-f", "bestaudio/best", "-x", "--audio-format", "wav",
		"--no-mtime", "-o", tmpl, url)
	if err != nil {
		return "", err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "download.*"))
	for _, m := range matches {
		if strings.HasSuffix(m, ".wav") {
			return m, nil
		}
	}
	if len(matches) > 0 {
		return matches[0], nil
	}
	return "", errors.New("yt-dlp: no file downloaded")
}

// CopyFile copies src to dst (used for audio copies in packages).
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
