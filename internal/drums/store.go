package drums

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BeFeast/RehearseKit/internal/storage"
)

// ErrNotFound is returned by Load when no edit revision has been saved.
var ErrNotFound = errors.New("drums: no edit revision")

// ConflictError is returned by Save when base_rev is not the current
// revision (someone else saved first, or the client is stale).
type ConflictError struct {
	EditRev int
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("drums: edit_rev %d is current", e.EditRev)
}

// Store reads and writes edits/<stem>.json. Saves are serialised per job
// in this process (rk serve is a single process; the worker never writes
// the file), so a compare-and-swap on edit_rev needs no database lock.
type Store struct {
	layout storage.Layout

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewStore builds a store over the data layout.
func NewStore(layout storage.Layout) *Store {
	return &Store{layout: layout, locks: map[string]*sync.Mutex{}}
}

func (s *Store) lock(jobID string) func() {
	s.mu.Lock()
	l, ok := s.locks[jobID]
	if !ok {
		l = &sync.Mutex{}
		s.locks[jobID] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Load returns the saved document, or ErrNotFound.
func (s *Store) Load(jobID string) (Doc, error) {
	path, err := s.layout.EditsPath(jobID, "drums")
	if err != nil {
		return Doc{}, err
	}
	return read(path)
}

func read(path string) (Doc, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Doc{}, ErrNotFound
	}
	if err != nil {
		return Doc{}, err
	}
	var d Doc
	if err := json.Unmarshal(b, &d); err != nil {
		return Doc{}, fmt.Errorf("drums: %s: %w", filepath.Base(path), err)
	}
	if d.Version != Version {
		return Doc{}, fmt.Errorf("drums: %s: version %d, want %d", filepath.Base(path), d.Version, Version)
	}
	if d.Events == nil {
		d.Events = []Event{}
	}
	return d, nil
}

// Save replaces the events when baseRev matches the current revision
// (the seed's when nothing was saved yet) and returns the new document.
// seed supplies the revision-0 document for the first save. The file is
// written via a temp file and rename.
func (s *Store) Save(jobID string, seed func() (Doc, error), baseRev int, events []Event, now time.Time) (Doc, error) {
	path, err := s.layout.EditsPath(jobID, "drums")
	if err != nil {
		return Doc{}, err
	}
	defer s.lock(jobID)()
	cur, err := read(path)
	if errors.Is(err, ErrNotFound) {
		cur, err = seed()
	}
	if err != nil {
		return Doc{}, err
	}
	if cur.EditRev != baseRev {
		return Doc{}, &ConflictError{EditRev: cur.EditRev}
	}
	cur.EditRev++
	cur.UpdatedAt = now
	cur.Events = events
	if cur.Events == nil {
		cur.Events = []Event{}
	}
	if err := write(path, cur); err != nil {
		return Doc{}, err
	}
	return cur, nil
}

// MarkExported records that an export was built from rev. A revision that
// is no longer current is not marked (the export was refused anyway).
func (s *Store) MarkExported(jobID string, rev int) error {
	path, err := s.layout.EditsPath(jobID, "drums")
	if err != nil {
		return err
	}
	defer s.lock(jobID)()
	cur, err := read(path)
	if errors.Is(err, ErrNotFound) {
		// Nothing saved: rev 0 was exported; nothing to record on disk.
		return nil
	}
	if err != nil {
		return err
	}
	if cur.EditRev != rev || cur.ExportedRev == rev {
		return nil
	}
	cur.ExportedRev = rev
	return write(path, cur)
}

func write(path string, d Doc) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
