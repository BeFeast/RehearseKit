package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutPaths(t *testing.T) {
	l, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "11111111-2222-4333-8444-555555555555"
	p, err := l.StemPath(id, "vocals")
	if err != nil || p != filepath.Join(l.Root, "jobs", id, "stems", "vocals.wav") {
		t.Errorf("StemPath = %q, %v", p, err)
	}
	p, err = l.PeaksPath(id, "drums")
	if err != nil || p != filepath.Join(l.Root, "jobs", id, "peaks", "drums.pk") {
		t.Errorf("PeaksPath = %q, %v", p, err)
	}
	for _, bad := range []string{"../x", "11111111-2222-4333-8444-55555555555G", "11111111-2222-4333-8444-555555555555/..", "X1111111-2222-4333-8444-555555555555"} {
		if _, err := l.JobDir(bad); err == nil {
			t.Errorf("JobDir(%q) accepted", bad)
		}
	}
	for _, bad := range []string{"", "Vocals", "../vocals", "vocals.wav", strings.Repeat("a", 65)} {
		if _, err := l.StemPath(id, bad); err == nil {
			t.Errorf("StemPath(%q) accepted", bad)
		}
	}
}

func TestSaveSourceAndRemove(t *testing.T) {
	l, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const id = "11111111-2222-4333-8444-555555555555"
	n, err := l.SaveSource(id, "mp3", strings.NewReader("hello"))
	if err != nil || n != 5 {
		t.Fatalf("SaveSource: n=%d err=%v", n, err)
	}
	b, err := os.ReadFile(filepath.Join(l.Root, "jobs", id, "source.mp3"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("read back: %q %v", b, err)
	}
	if _, err := l.SaveSource(id, "../etc", strings.NewReader("x")); err == nil {
		t.Error("bad extension accepted")
	}
	if err := l.RemoveJob(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.Root, "jobs", id)); !os.IsNotExist(err) {
		t.Error("job dir still exists")
	}
	if err := l.RemoveJob(id); err != nil {
		t.Errorf("second remove: %v", err)
	}
	if ext, ok := SourceExt("Song Name.FLAC"); !ok || ext != "flac" {
		t.Errorf("SourceExt = %q %v", ext, ok)
	}
	if _, ok := SourceExt("noext"); ok {
		t.Error("SourceExt accepted a name without extension")
	}
}
