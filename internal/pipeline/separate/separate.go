// Package separate runs an MSST recipe (internal/models) through
// tools/separate/separate.py and relays its "progress <0..1>" lines.
package separate

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Options for one run.
type Options struct {
	Python    string // interpreter; default python3
	Script    string // tools/separate/separate.py
	Manifest  string // internal/models/manifest.json as shipped in the image
	ModelsDir string
	Recipe    string
	Stack     string // the job's stack; the script refuses weights it may not load
	Device    string // cuda | cpu
	Input     string // source WAV
	OutDir    string // receives <stem>.wav
	// Env is appended to the child's environment (egress lock, PYTHONPATH).
	Env []string
}

// Run separates o.Input into o.OutDir. progress (may be nil) receives 0..1.
func Run(ctx context.Context, o Options, progress func(float64)) error {
	if o.Python == "" {
		o.Python = "python3"
	}
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return err
	}
	args := []string{o.Script, "--manifest", o.Manifest, "--models-dir", o.ModelsDir, "--recipe", o.Recipe,
		"--stack", o.Stack, "--input", o.Input, "--out", o.OutDir}
	if o.Device != "" {
		args = append(args, "--device", o.Device)
	}
	cmd := exec.CommandContext(ctx, o.Python, args...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(append(os.Environ(), "PYTHONUNBUFFERED=1"), o.Env...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start separate.py: %w", err)
	}
	var tail []string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "progress "); ok {
			if p, err := strconv.ParseFloat(v, 64); err == nil && progress != nil {
				progress(p)
			}
			continue
		}
		if line != "" {
			tail = append(tail, line)
			if len(tail) > 20 {
				tail = tail[1:]
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("separate.py %s failed: %w\n%s", o.Recipe, err, strings.Join(tail, "\n"))
	}
	return nil
}
