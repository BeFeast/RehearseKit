package tempo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	r, err := Parse([]byte(`{"bpm": 128.004, "confidence": 0.91, "beats": [0.5, 0.97, 1.44], "raw_bpm": 128.004, "extra": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.BPM == nil || *r.BPM != 128.0 || r.Confidence != 0.91 || len(r.Beats) != 3 {
		t.Fatalf("%+v", r)
	}
	r, err = Parse([]byte(`{"bpm": null, "confidence": 0.2, "raw_bpm": 87.1}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.BPM != nil || r.RawBPM == nil {
		t.Fatalf("%+v", r)
	}
	for _, bad := range []string{
		`{"bpm": 5, "confidence": 1}`,
		`{"bpm": 900, "confidence": 1}`,
		`{"bpm": 120, "confidence": 1.5}`,
		`{"bpm": 120, "confidence": -0.1}`,
		`{"bpm": 120, "confidence": 0.5, "beats": [1, 0.5]}`,
		`{"bpm": 120, "confidence": 0.5, "beats": [-1]}`,
		`not json`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%s) accepted", bad)
		}
	}
}

func TestRunStub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntest -n \"$1\" || exit 3\necho '{\"bpm\": 100, \"confidence\": 0.8}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), []string{script}, "/x.wav")
	if err != nil {
		t.Fatal(err)
	}
	if r.BPM == nil || *r.BPM != 100 {
		t.Fatalf("%+v", r)
	}
	// A failing analyser surfaces its stderr.
	bad := filepath.Join(dir, "bad.sh")
	_ = os.WriteFile(bad, []byte("#!/bin/sh\necho 'ModuleNotFoundError: librosa' >&2\nexit 1\n"), 0o755)
	_, err = Run(context.Background(), []string{bad}, "/x.wav")
	if err == nil || !strings.Contains(err.Error(), "librosa") {
		t.Fatalf("err %v", err)
	}
}
