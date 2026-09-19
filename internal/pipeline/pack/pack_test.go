package pack

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "vocals.wav")
	if err := os.WriteFile(wav, []byte("RIFFxxxxWAVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	bpm := 136.0
	readme := Readme(ReadmeParams{ProjectName: "Song", BPM: &bpm, Duration: 185, Stems: []string{"vocals"}, Model: "htdemucs"})
	out := filepath.Join(dir, "package.zip")
	err := WriteFile(out, []Entry{
		{Name: "stems/vocals.wav", Path: wav},
		{Name: "tempo.json", Data: []byte(`{"bpm":136}`), Compress: true},
		{Name: "README.txt", Data: readme, Compress: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string]string{}
	methods := map[string]uint16{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
		methods[f.Name] = f.Method
	}
	if got["stems/vocals.wav"] != "RIFFxxxxWAVE" || methods["stems/vocals.wav"] != zip.Store {
		t.Fatalf("stem entry %q method %d", got["stems/vocals.wav"], methods["stems/vocals.wav"])
	}
	if methods["README.txt"] != zip.Deflate || !strings.Contains(got["README.txt"], "136.00 BPM") || !strings.Contains(got["README.txt"], "3:05") {
		t.Fatalf("readme %q", got["README.txt"])
	}
	if got["tempo.json"] != `{"bpm":136}` {
		t.Fatalf("tempo %q", got["tempo.json"])
	}
	// A missing source file fails and leaves no partial archive.
	err = WriteFile(filepath.Join(dir, "bad.zip"), []Entry{{Name: "x", Path: filepath.Join(dir, "missing")}})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.zip.part")); err == nil {
		t.Fatal("partial archive left behind")
	}
	if !strings.Contains(string(Readme(ReadmeParams{})), "not detected") {
		t.Fatal("readme without bpm")
	}
}
