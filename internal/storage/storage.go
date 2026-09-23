// Package storage knows the on-disk layout under RK_DATA_DIR:
//
//	jobs/<job_id>/source.<ext>       original upload
//	jobs/<job_id>/source.wav         24-bit/48k after convert
//	jobs/<job_id>/stems/<name>.wav
//	jobs/<job_id>/peaks/<name>.pk
//	jobs/<job_id>/analysis.json      transcription: beat grid, sections, per-instrument status (runner)
//	jobs/<job_id>/notes/<name>.json   transcription: note events of one stem, seconds (runner)
//	jobs/<job_id>/midi/<name>.mid     transcription: SMF written by the worker from notes + grid
//	jobs/<job_id>/project.dawproject
//	jobs/<job_id>/tempo.json
//	jobs/<job_id>/package.zip
package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	jobIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// NamePattern constrains stem names used in paths.
	NamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	extPattern  = regexp.MustCompile(`^[a-z0-9]{1,8}$`)
)

// ErrInvalidID is returned for job ids that are not lowercase UUIDs.
var ErrInvalidID = errors.New("storage: invalid job id")

// Layout resolves paths under a data root.
type Layout struct {
	Root string
}

// New returns a Layout for root and creates the jobs directory.
func New(root string) (Layout, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Layout{}, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "jobs"), 0o755); err != nil {
		return Layout{}, err
	}
	return Layout{Root: abs}, nil
}

// ValidJobID reports whether id is a lowercase UUID usable in paths.
func ValidJobID(id string) bool { return jobIDPattern.MatchString(id) }

// JobDir returns jobs/<id>. It errors on ids that could escape the root.
func (l Layout) JobDir(id string) (string, error) {
	if !ValidJobID(id) {
		return "", ErrInvalidID
	}
	return filepath.Join(l.Root, "jobs", id), nil
}

// StemPath returns jobs/<id>/stems/<name>.wav.
func (l Layout) StemPath(id, name string) (string, error) {
	dir, err := l.JobDir(id)
	if err != nil {
		return "", err
	}
	if !NamePattern.MatchString(name) {
		return "", fmt.Errorf("storage: invalid stem name %q", name)
	}
	return filepath.Join(dir, "stems", name+".wav"), nil
}

// AnalysisPath returns jobs/<id>/analysis.json (transcription artefact).
func (l Layout) AnalysisPath(id string) (string, error) {
	dir, err := l.JobDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "analysis.json"), nil
}

// NotesPath returns jobs/<id>/notes/<name>.json (note events of one stem).
func (l Layout) NotesPath(id, name string) (string, error) {
	dir, err := l.JobDir(id)
	if err != nil {
		return "", err
	}
	if !NamePattern.MatchString(name) {
		return "", fmt.Errorf("storage: invalid stem name %q", name)
	}
	return filepath.Join(dir, "notes", name+".json"), nil
}

// MidiPath returns jobs/<id>/midi/<name>.mid.
func (l Layout) MidiPath(id, name string) (string, error) {
	dir, err := l.JobDir(id)
	if err != nil {
		return "", err
	}
	if !NamePattern.MatchString(name) {
		return "", fmt.Errorf("storage: invalid stem name %q", name)
	}
	return filepath.Join(dir, "midi", name+".mid"), nil
}

// PeaksPath returns jobs/<id>/peaks/<name>.pk.
func (l Layout) PeaksPath(id, name string) (string, error) {
	dir, err := l.JobDir(id)
	if err != nil {
		return "", err
	}
	if !NamePattern.MatchString(name) {
		return "", fmt.Errorf("storage: invalid stem name %q", name)
	}
	return filepath.Join(dir, "peaks", name+".pk"), nil
}

// SourceExt normalises an upload's extension for use in source.<ext>.
func SourceExt(filename string) (string, bool) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	if !extPattern.MatchString(ext) {
		return "", false
	}
	return ext, true
}

// SaveSource streams r into jobs/<id>/source.<ext>, creating the job
// directory. It never buffers the whole body. On error the partial file is
// removed. Returns the byte count written.
func (l Layout) SaveSource(id, ext string, r io.Reader) (int64, error) {
	dir, err := l.JobDir(id)
	if err != nil {
		return 0, err
	}
	if !extPattern.MatchString(ext) {
		return 0, fmt.Errorf("storage: invalid extension %q", ext)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	path := filepath.Join(dir, "source."+ext)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return n, err
	}
	return n, nil
}

// RemoveJob deletes jobs/<id> recursively. A missing directory is not an error.
func (l Layout) RemoveJob(id string) error {
	dir, err := l.JobDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
