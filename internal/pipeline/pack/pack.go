// Package pack writes the download package (package.zip): stems, the
// DAWproject archive, tempo.json and a README. Audio and the (already
// compressed) DAWproject are stored, text is deflated; everything streams
// from disk.
package pack

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Entry is one file to include.
type Entry struct {
	// Name inside the archive, e.g. "stems/vocals.wav".
	Name string
	// Path on disk; used when Data is nil.
	Path string
	// Data is inline content (README).
	Data []byte
	// Compress selects Deflate over Store.
	Compress bool
}

// Write streams a zip of entries to w.
func Write(w io.Writer, entries []Entry) error {
	zw := zip.NewWriter(w)
	now := time.Now()
	for _, e := range entries {
		method := zip.Store
		if e.Compress {
			method = zip.Deflate
		}
		f, err := zw.CreateHeader(&zip.FileHeader{Name: e.Name, Method: method, Modified: now})
		if err != nil {
			return err
		}
		if e.Data != nil {
			if _, err := f.Write(e.Data); err != nil {
				return err
			}
			continue
		}
		src, err := os.Open(e.Path)
		if err != nil {
			return fmt.Errorf("pack %s: %w", e.Name, err)
		}
		_, err = io.Copy(f, src)
		src.Close()
		if err != nil {
			return fmt.Errorf("pack %s: %w", e.Name, err)
		}
	}
	return zw.Close()
}

// WriteFile writes the archive to path via a temp file and rename.
func WriteFile(path string, entries []Entry) error {
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := Write(f, entries); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ReadmeParams fills the README template.
type ReadmeParams struct {
	ProjectName string
	BPM         *float64
	Duration    float64
	Stems       []string
	Model       string
}

// Readme renders README.txt (adapted from the legacy import guide).
func Readme(p ReadmeParams) []byte {
	bpm := "not detected (project tempo left at 120 BPM placeholder)"
	if p.BPM != nil {
		bpm = fmt.Sprintf("%.2f BPM (detected)", *p.BPM)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "REHEARSEKIT PACKAGE: %s\n", p.ProjectName)
	b.WriteString(strings.Repeat("=", 60) + "\n\n")
	fmt.Fprintf(&b, "Tempo:        %s\nDuration:     %d:%02d\nStems:        %s\nSeparation:   Demucs %s\nFormat:       24-bit / 48 kHz stereo WAV\n\n",
		bpm, int(p.Duration)/60, int(p.Duration)%60, strings.Join(p.Stems, ", "), p.Model)
	b.WriteString(`CONTENTS
  stems/<name>.wav       individual stems for any DAW
  project.dawproject     DAWproject 1.0 archive (Cubase 14, Bitwig, Studio One 7, Reaper)
  tempo.json             detected tempo, confidence and beat positions
  README.txt             this file

CUBASE 14 PRO
  1. Extract this zip.
  2. File > Import > DAWproject.
  3. Select the folder that holds project.dawproject, then the file itself.
  4. Every stem arrives on its own track at the detected tempo, 48 kHz.

STUDIO ONE 7, BITWIG, REAPER
  1. Extract this zip.
  2. File > Open (or Import) > project.dawproject.

ANY OTHER DAW
  Create a 48 kHz project, set the tempo shown above, and drag the files
  from stems/ onto separate tracks starting at bar 1.

The stems are time-aligned: they all start at 0:00 and have the same length.
`)
	return []byte(b.String())
}
