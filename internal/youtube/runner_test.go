package youtube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestYtdlpMessage(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{"private", "ERROR: [youtube] dQw4w9WgXcQ: Private video. Sign in if you've been granted access to this video\n", "Private video. Sign in if you've been granted access to this video"},
		{"removed", "WARNING: something\nERROR: [youtube] abc: Video unavailable\n", "Video unavailable"},
		{"geo", "ERROR: [youtube] abc: The uploader has not made this video available in your country\n", "The uploader has not made this video available in your country"},
		{"no extractor prefix", "ERROR: Unsupported URL: https://x\n", "Unsupported URL: https://x"},
		{"no ERROR line", "Traceback (most recent call last):\n  File ...\n", "Traceback (most recent call last):"},
		{"empty", "", "yt-dlp failed"},
		{"long line capped", "ERROR: " + strings.Repeat("x", 500), strings.Repeat("x", 300)},
		{"colon inside message kept", "ERROR: [generic] abc: Something: with colons\n", "Something: with colons"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ytdlpMessage(tc.stderr); got != tc.want {
				t.Fatalf("ytdlpMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecRunnerNotInstalled(t *testing.T) {
	var r *ExecRunner
	if r.Available() {
		t.Fatal("nil runner reported available")
	}
	r = &ExecRunner{}
	if r.Available() {
		t.Fatal("empty runner reported available")
	}
	if _, err := r.Run(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err=%v, want ErrNotInstalled", err)
	}
}

// TestExecRunnerFakeBinary drives ExecRunner through a shell script standing
// in for yt-dlp, covering the argv shape, a non-zero exit and a timeout.
func TestExecRunnerFakeBinary(t *testing.T) {
	script := writeScript(t, `#!/bin/sh
case "$*" in
  *sleepy*) sleep 5 ;;
  *broken*) echo "ERROR: [youtube] broken12345: Video unavailable" >&2; exit 1 ;;
  *) echo "$@" >&2; printf '{"id":"dQw4w9WgXcQ","title":"ok"}' ;;
esac
`)
	// Short WaitDelay: the script's `sleep` child keeps the stdout pipe open
	// after sh is killed, which is exactly the hang WaitDelay guards against.
	r := &ExecRunner{Path: script, WaitDelay: 100 * time.Millisecond}
	if !r.Available() {
		t.Fatal("not available")
	}
	out, err := r.Run(context.Background(), CanonicalURL("dQw4w9WgXcQ"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"title":"ok"`) {
		t.Fatalf("stdout=%q", out)
	}

	_, err = r.Run(context.Background(), CanonicalURL("broken12345"))
	var re *RunError
	if !errors.As(err, &re) || re.ExitCode != 1 || re.Message != "Video unavailable" {
		t.Fatalf("err=%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = r.Run(ctx, CanonicalURL("sleepy123456"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want DeadlineExceeded", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Run blocked %v after the deadline (child kept the pipe open)", el)
	}
}
