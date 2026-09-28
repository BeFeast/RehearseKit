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
	locks map[string]*jobLock
}

// jobLock is one job's mutex with the number of holders and waiters, so
// the entry can be dropped once nobody uses it (the map does not grow
// with the number of jobs ever edited).
type jobLock struct {
	sync.Mutex
	refs int
}

// NewStore builds a store over the data layout.
func NewStore(layout storage.Layout) *Store {
	return &Store{layout: layout, locks: map[string]*jobLock{}}
}

func (s *Store) lock(key string) func() {
	s.mu.Lock()
	l, ok := s.locks[key]
	if !ok {
		l = &jobLock{}
		s.locks[key] = l
	}
	l.refs++
	s.mu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		s.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(s.locks, key)
		}
		s.mu.Unlock()
	}
}

// Locks reports the live lock entries (tests).
func (s *Store) Locks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.locks)
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
// is no longer current is not marked (the export was refused anyway). When
// nothing was saved yet the seed (rev 0) is written with the mark, so the
// document on disk says what was exported.
func (s *Store) MarkExported(jobID string, seed func() (Doc, error), rev int) error {
	path, err := s.layout.EditsPath(jobID, "drums")
	if err != nil {
		return err
	}
	defer s.lock(jobID)()
	cur, err := read(path)
	if errors.Is(err, ErrNotFound) {
		cur, err = seed()
	}
	if err != nil {
		return err
	}
	if cur.EditRev != rev || (cur.ExportedRev != nil && *cur.ExportedRev == rev) {
		return nil
	}
	cur.ExportedRev = &rev
	return write(path, cur)
}

// ErrJobGone is returned when the job directory no longer exists (DELETE or
// retention raced the save): the edits must not resurrect it.
var ErrJobGone = errors.New("drums: job directory is gone")

func write(path string, d Doc) error {
	// Mkdir (not MkdirAll) creates only edits/ itself: with the job
	// directory gone it fails with ENOENT instead of resurrecting the job,
	// and the rename below fails the same way — no check-then-act window.
	if err := os.Mkdir(filepath.Dir(path), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		if errors.Is(err, os.ErrNotExist) {
			return ErrJobGone
		}
		return err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrJobGone
		}
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		if errors.Is(err, os.ErrNotExist) {
			return ErrJobGone
		}
		return err
	}
	return nil
}
