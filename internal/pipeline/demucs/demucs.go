// Package demucs runs `python -m demucs` and turns its tqdm output into a
// 0..1 progress figure. It is shared by the GPU agent (cuda) and the
// worker's local development mode (cpu).
package demucs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BeFeast/RehearseKit/internal/pipeline/media"
)

// Options for one separation run.
type Options struct {
	Python string // interpreter; default python3
	Model  string // htdemucs | htdemucs_ft | htdemucs_6s
	Device string // cuda | cpu; empty lets demucs pick
	Input  string // source WAV
	OutDir string // demucs -o; stems land in OutDir/<Model>/<stem>.flac
	// Extra args appended verbatim (e.g. --segment 7 on small GPUs).
	Extra []string
}

// Bars is the number of tqdm bars demucs shows per track for a model
// (bag-of-models run one per sub-model). Unknown models are assumed 1 and
// corrected from the "bag of N models" banner at runtime.
var Bars = map[string]int{
	"htdemucs":    1,
	"htdemucs_ft": 4,
	"htdemucs_6s": 1,
}

var (
	pctRe = regexp.MustCompile(`(\d{1,3})%\|`)
	bagRe = regexp.MustCompile(`bag of (\d+) models`)
)

// Tracker folds successive per-model progress bars into one 0..1 value.
type Tracker struct {
	mu       sync.Mutex
	bars     int
	done     int
	last     float64 // last percentage seen on the current bar
	progress float64
}

// NewTracker returns a tracker expecting bars progress bars.
func NewTracker(bars int) *Tracker {
	if bars < 1 {
		bars = 1
	}
	return &Tracker{bars: bars}
}

// Feed consumes one line (or \r-separated tqdm fragment) of demucs output.
func (t *Tracker) Feed(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m := bagRe.FindStringSubmatch(line); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			t.bars = n
		}
	}
	m := pctRe.FindStringSubmatch(line)
	if m == nil {
		return
	}
	pct, _ := strconv.ParseFloat(m[1], 64)
	if pct < t.last-1 { // a new bar started (e.g. 100 → 3)
		t.done++
	}
	t.last = pct
	if t.done >= t.bars {
		t.done = t.bars - 1
	}
	p := (float64(t.done) + pct/100) / float64(t.bars)
	if p > t.progress {
		t.progress = p
	}
}

// Progress returns the current 0..1 estimate.
func (t *Tracker) Progress() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.progress
}

// scanCRLF splits on \n or \r so tqdm's in-place updates arrive as lines.
func scanCRLF(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// Run separates o.Input and returns the directory holding <stem>.flac
// files. progress (may be nil) receives 0..1 updates as demucs prints
// them. The child is killed when ctx is cancelled.
func Run(ctx context.Context, o Options, progress func(float64)) (string, error) {
	if o.Python == "" {
		o.Python = "python3"
	}
	if o.Model == "" {
		o.Model = "htdemucs"
	}
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return "", err
	}
	args := []string{"-m", "demucs", "-n", o.Model, "--flac", "--int24", "--filename", "{stem}.{ext}", "-o", o.OutDir}
	if o.Device != "" {
		args = append(args, "-d", o.Device)
	}
	args = append(args, o.Extra...)
	args = append(args, o.Input)
	cmd := exec.CommandContext(ctx, o.Python, args...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	cmd.Stdout = nil
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start demucs: %w", err)
	}
	tr := NewTracker(Bars[o.Model])
	var tail []string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(scanCRLF)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		before := tr.Progress()
		tr.Feed(line)
		if p := tr.Progress(); p != before && progress != nil {
			progress(p)
		}
		if !pctRe.MatchString(line) {
			tail = append(tail, line)
			if len(tail) > 20 {
				tail = tail[1:]
			}
		}
	}
	err = cmd.Wait()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("demucs failed: %w\n%s", err, strings.Join(tail, "\n"))
	}
	dir := filepath.Join(o.OutDir, o.Model)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("demucs produced no output directory %s", dir)
	}
	return dir, nil
}

// ConvertStems transcodes <dir>/<stem>.flac for each stem into
// <dst>/<stem>.wav (24-bit/48 kHz stereo). Missing stems are an error.
func ConvertStems(ctx context.Context, dir, dst string, stems []string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, name := range stems {
		in := filepath.Join(dir, name+".flac")
		if _, err := os.Stat(in); err != nil {
			if wav := filepath.Join(dir, name+".wav"); fileExists(wav) {
				in = wav
			} else {
				return fmt.Errorf("demucs output missing stem %s", name)
			}
		}
		if err := media.ConvertToStemWAV(ctx, in, filepath.Join(dst, name+".wav")); err != nil {
			return fmt.Errorf("convert %s: %w", name, err)
		}
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ErrNotInstalled is returned by Check when demucs cannot be imported.
var ErrNotInstalled = errors.New("demucs: python module not importable")

// Check verifies that python can import demucs.
func Check(ctx context.Context, python string) error {
	if python == "" {
		python = "python3"
	}
	if _, err := media.Run(ctx, python, "-c", "import demucs, torch"); err != nil {
		return fmt.Errorf("%w: %v", ErrNotInstalled, err)
	}
	return nil
}
