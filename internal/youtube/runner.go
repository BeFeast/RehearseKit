package youtube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes yt-dlp (or a test double) and returns its single-JSON
// dump for url.
type Runner interface {
	// Run returns yt-dlp's stdout. Failures of the tool itself are reported
	// as *RunError; ErrNotInstalled means the binary could not be executed.
	Run(ctx context.Context, url string) ([]byte, error)
}

// ErrNotInstalled is returned when the yt-dlp binary is missing.
var ErrNotInstalled = errors.New("yt-dlp is not installed")

// RunError carries a yt-dlp failure (non-zero exit) with the message the
// tool printed, already trimmed for the client.
type RunError struct {
	ExitCode int
	Message  string
}

func (e *RunError) Error() string { return fmt.Sprintf("yt-dlp exit %d: %s", e.ExitCode, e.Message) }

// ExecRunner runs the real yt-dlp binary.
type ExecRunner struct {
	// Path is the resolved binary; empty means not installed.
	Path string
	// WaitDelay bounds how long Run waits for stdout/stderr to close after
	// the context kills yt-dlp; a grandchild holding the pipe would
	// otherwise block Wait past the timeout. Zero means 2s.
	WaitDelay time.Duration
}

// LookPath resolves yt-dlp on PATH. The returned runner reports
// ErrNotInstalled from Run when the binary was not found, so the handler can
// answer 501 without a second lookup.
func LookPath() *ExecRunner {
	p, err := exec.LookPath("yt-dlp")
	if err != nil {
		return &ExecRunner{}
	}
	return &ExecRunner{Path: p}
}

// Available reports whether the binary was found.
func (r *ExecRunner) Available() bool { return r != nil && r.Path != "" }

// Run invokes yt-dlp with metadata-only flags.
func (r *ExecRunner) Run(ctx context.Context, url string) ([]byte, error) {
	if !r.Available() {
		return nil, ErrNotInstalled
	}
	cmd := exec.CommandContext(ctx, r.Path,
		"--dump-single-json", "--no-playlist", "--skip-download",
		"--no-warnings", "--no-progress", "--", url)
	cmd.WaitDelay = r.WaitDelay
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = 2 * time.Second
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil, &RunError{ExitCode: ee.ExitCode(), Message: ytdlpMessage(stderr.String())}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, ErrNotInstalled
	}
	return nil, err
}

// ytdlpMessage picks the first "ERROR:" line from yt-dlp's stderr and strips
// the extractor prefix ("ERROR: [youtube] abc: Private video" -> "Private
// video"). The result is capped so a stack trace never reaches the client.
func ytdlpMessage(stderr string) string {
	var line string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "ERROR:") {
			line = strings.TrimSpace(strings.TrimPrefix(l, "ERROR:"))
			break
		}
	}
	if line == "" {
		line, _, _ = strings.Cut(strings.TrimSpace(stderr), "\n")
	}
	if strings.HasPrefix(line, "[") {
		if _, rest, ok := strings.Cut(line, "] "); ok {
			line = rest
			// "abc123: message" -> "message" (only a short id-like prefix)
			if j := strings.Index(line, ": "); j >= 0 && j <= 12 {
				line = line[j+2:]
			}
		}
	}
	if line == "" {
		line = "yt-dlp failed"
	}
	const maxLen = 300
	if len(line) > maxLen {
		line = line[:maxLen]
	}
	return line
}
