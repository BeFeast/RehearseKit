package drums

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/pipeline/analysis"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// testNotes is a bar of ADTOF-style output: kick, snare, closed hats, a
// tom, a crash, plus one pitch (50) the GM profile does not map.
const testNotes = `{"stem":"drums","adapter":"adtof","model":"adtof_frame_rnn","notes":[
{"onset":0.5,"offset":0.6,"pitch":36,"velocity":0.83},
{"onset":0.5,"offset":0.6,"pitch":42,"velocity":0.7},
{"onset":0.75,"offset":0.85,"pitch":42,"velocity":0.66},
{"onset":1.0,"offset":1.1,"pitch":38,"velocity":0.91},
{"onset":1.0,"offset":1.1,"pitch":42,"velocity":0.71},
{"onset":1.25,"offset":1.35,"pitch":36,"velocity":0.64},
{"onset":1.5,"offset":1.6,"pitch":36,"velocity":0.8},
{"onset":1.5,"offset":1.6,"pitch":42,"velocity":0.69},
{"onset":1.75,"offset":1.85,"pitch":48,"velocity":0.77},
{"onset":2.0,"offset":2.1,"pitch":38,"velocity":0.9},
{"onset":2.0,"offset":2.1,"pitch":49,"velocity":0.95},
{"onset":2.0,"offset":2.1,"pitch":50,"velocity":0.5}
]}`

func parseTestNotes(t *testing.T) (analysis.Notes, []byte) {
	t.Helper()
	raw := []byte(testNotes)
	n, err := analysis.ParseNotes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return n, raw
}

func gm(t *testing.T) Profile {
	t.Helper()
	p, ok := ProfileByID("gm")
	if !ok {
		t.Fatal("gm profile missing")
	}
	return p
}

func TestProfileGM(t *testing.T) {
	p := gm(t)
	if ids := ProfileIDs(); len(ids) != 1 || ids[0] != "gm" {
		t.Fatalf("profiles %v", ids)
	}
	for art, key := range map[string]int{"kick": 36, "snare": 38, "stick": 37, "hhc": 42, "hho": 46, "hhp": 44, "tomh": 48, "tomm": 47, "tomf": 43, "ride": 51, "bell": 53, "crash": 49} {
		if k, ok := p.Key(art); !ok || k != key {
			t.Errorf("%s → %d (%v), want %d", art, k, ok, key)
		}
		if a, ok := p.Articulation(key); !ok || a != art {
			t.Errorf("%d → %s (%v), want %s", key, a, ok, art)
		}
	}
	if _, ok := p.Articulation(50); ok {
		t.Error("50 (GM high tom, SD3 choke) must not map")
	}
	if len(Articulations) != 12 || !ValidArticulation("hhp") || ValidArticulation("hh") {
		t.Errorf("taxonomy %v", Articulations)
	}
}

func TestSeed(t *testing.T) {
	notes, raw := parseTestNotes(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	d, warnings := Seed(notes, raw, gm(t), now)
	if len(warnings) != 1 || warnings[0] != "notes[11]: pitch 50 has no articulation in profile gm, dropped" {
		t.Fatalf("warnings %v", warnings)
	}
	if d.Version != 1 || d.Stem != "drums" || d.EditRev != 0 || d.ExportedRev != 0 || d.Profile != "gm" || !d.UpdatedAt.Equal(now) {
		t.Fatalf("doc %+v", d)
	}
	if d.ModelRev.Adapter != "adtof" || d.ModelRev.Model != "adtof_frame_rnn" || d.ModelRev.Count != 12 || len(d.ModelRev.NotesSHA256) != 64 {
		t.Fatalf("model rev %+v", d.ModelRev)
	}
	if len(d.Events) != 11 {
		t.Fatalf("%d events", len(d.Events))
	}
	want := []string{"kick", "hhc", "hhc", "snare", "hhc", "kick", "kick", "hhc", "tomh", "snare", "crash"}
	for i, e := range d.Events {
		n := notes.Notes[i]
		if e.Art != want[i] || e.T != n.Onset || e.Vel != n.Velocity || e.Src != SourceModel || e.Model == nil || *e.Model != i || e.ID != "m"+itoa(i) {
			t.Errorf("events[%d] = %+v, want %s from notes[%d]", i, e, want[i], i)
		}
	}
	// The JSON round trip keeps model times bit for bit.
	b, _ := json.Marshal(d)
	var back Doc
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for i, e := range back.Events {
		if math.Float64bits(e.T) != math.Float64bits(notes.Notes[i].Onset) || math.Float64bits(e.Vel) != math.Float64bits(notes.Notes[i].Velocity) {
			t.Errorf("events[%d] changed in JSON: %v vs %v", i, e.T, notes.Notes[i].Onset)
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestSeedDropsZeroVelocity(t *testing.T) {
	raw := []byte(`{"stem":"drums","notes":[{"onset":1,"offset":1.1,"pitch":36,"velocity":0},{"onset":2,"offset":2.1,"pitch":38,"velocity":0.5}]}`)
	notes, err := analysis.ParseNotes(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, warnings := Seed(notes, raw, gm(t), time.Now())
	if len(d.Events) != 1 || d.Events[0].Art != "snare" || len(warnings) != 1 {
		t.Fatalf("events %+v warnings %v", d.Events, warnings)
	}
	if err := Validate(d.Events, 10, 2); err != nil {
		t.Fatalf("seed must validate: %v", err)
	}
}

func TestValidate(t *testing.T) {
	idx := func(i int) *int { return &i }
	ok := []Event{{ID: "u2", Art: "snare", T: 2.5, Vel: 0.3, Src: SourceManual}, {ID: "m0", Art: "kick", T: 0.5, Vel: 0.8, Src: SourceModel, Model: idx(0)}}
	if err := Validate(ok, 10, 12); err != nil {
		t.Fatal(err)
	}
	if ok[0].ID != "m0" || ok[1].ID != "u2" {
		t.Fatalf("not sorted by time: %+v", ok)
	}
	if err := Validate([]Event{{ID: "u1", Art: "snare", T: 20, Vel: 0.5, Src: SourceManual}}, 0, 12); err != nil {
		t.Errorf("duration 0 must not bound time: %v", err)
	}
	bad := map[string][]Event{
		"id":         {{ID: "Bad Id", Art: "kick", T: 1, Vel: 0.5, Src: SourceManual}},
		"dup":        {{ID: "u1", Art: "kick", T: 1, Vel: 0.5, Src: SourceManual}, {ID: "u1", Art: "kick", T: 2, Vel: 0.5, Src: SourceManual}},
		"art":        {{ID: "u1", Art: "hh", T: 1, Vel: 0.5, Src: SourceManual}},
		"time":       {{ID: "u1", Art: "kick", T: 11, Vel: 0.5, Src: SourceManual}},
		"neg":        {{ID: "u1", Art: "kick", T: -1, Vel: 0.5, Src: SourceManual}},
		"nan":        {{ID: "u1", Art: "kick", T: math.NaN(), Vel: 0.5, Src: SourceManual}},
		"vel0":       {{ID: "u1", Art: "kick", T: 1, Vel: 0, Src: SourceManual}},
		"vel2":       {{ID: "u1", Art: "kick", T: 1, Vel: 1.5, Src: SourceManual}},
		"src":        {{ID: "u1", Art: "kick", T: 1, Vel: 0.5, Src: "fusion"}},
		"manual+idx": {{ID: "u1", Art: "kick", T: 1, Vel: 0.5, Src: SourceManual, Model: idx(1)}},
		"model-idx":  {{ID: "m1", Art: "kick", T: 1, Vel: 0.5, Src: SourceModel}},
		"model-oob":  {{ID: "m1", Art: "kick", T: 1, Vel: 0.5, Src: SourceModel, Model: idx(12)}},
	}
	for name, evs := range bad {
		if err := Validate(evs, 10, 12); err == nil {
			t.Errorf("%s: accepted %+v", name, evs)
		}
	}
	many := make([]Event, MaxEvents+1)
	if err := Validate(many, 10, 12); err == nil {
		t.Error("accepted more than MaxEvents")
	}
}

func newStore(t *testing.T) (*Store, storage.Layout, string) {
	t.Helper()
	layout, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "0f5b8c2e-1d3a-4b7c-9e0f-1a2b3c4d5e6f"
	dir, _ := layout.JobDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return NewStore(layout), layout, id
}

// TestScenarioD3b is the editing acceptance: seed from the model, add a
// ghost snare, delete a false kick, turn an open hat closed, move a flam
// off the grid, save, reload → the same events; untouched model events
// keep the onset and velocity of the notes file bit for bit.
func TestScenarioD3b(t *testing.T) {
	notes, raw := parseTestNotes(t)
	store, _, id := newStore(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	seed := func() (Doc, error) { d, _ := Seed(notes, raw, gm(t), now); return d, nil }
	if _, err := store.Load(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load before save: %v", err)
	}

	// Rev 1: the drummer decides the hat on beat 2 was open.
	base, _ := seed()
	evs := append([]Event(nil), base.Events...)
	find := func(art string, at float64) int {
		for i, e := range evs {
			if e.Art == art && e.T == at {
				return i
			}
		}
		t.Fatalf("no %s at %v", art, at)
		return -1
	}
	evs[find("hhc", 1.0)].Art = "hho"
	d1, err := store.Save(id, seed, 0, evs, now.Add(time.Minute))
	if err != nil || d1.EditRev != 1 {
		t.Fatalf("save 1: %+v %v", d1, err)
	}

	// Rev 2: the D3b edits.
	evs = append([]Event(nil), d1.Events...)
	evs = append(evs, Event{ID: "u1", Art: "snare", T: 1.4375, Vel: 0.2, Src: SourceManual}) // ghost snare, off grid
	k := find("kick", 1.25)                                                                  // false kick
	evs = append(evs[:k], evs[k+1:]...)
	evs[find("hho", 1.0)].Art = "hhc" // open → closed
	evs[find("snare", 2.0)].T = 2.011 // flam moved without snap
	if err := Validate(evs, 10, len(notes.Notes)); err != nil {
		t.Fatal(err)
	}
	d2, err := store.Save(id, seed, 1, evs, now.Add(2*time.Minute))
	if err != nil || d2.EditRev != 2 || len(d2.Events) != 11 {
		t.Fatalf("save 2: rev %d, %d events, %v", d2.EditRev, len(d2.Events), err)
	}

	// Reopen.
	got, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.EditRev != 2 || got.ExportedRev != 0 || !got.UpdatedAt.Equal(now.Add(2*time.Minute)) || got.ModelRev != d1.ModelRev {
		t.Fatalf("reloaded %+v", got)
	}
	if len(got.Events) != len(evs) {
		t.Fatalf("%d events, want %d", len(got.Events), len(evs))
	}
	for i := range evs {
		a, b := got.Events[i], evs[i]
		if a.ID != b.ID || a.Art != b.Art || math.Float64bits(a.T) != math.Float64bits(b.T) || math.Float64bits(a.Vel) != math.Float64bits(b.Vel) || a.Src != b.Src || (a.Model == nil) != (b.Model == nil) {
			t.Errorf("events[%d]: %+v vs %+v", i, a, b)
		}
	}
	var ghost, moved, closed int
	for _, e := range got.Events {
		switch {
		case e.Src == SourceManual:
			ghost++
			if e.Art != "snare" || e.T != 1.4375 || e.Vel != 0.2 || e.Model != nil {
				t.Errorf("ghost %+v", e)
			}
		case e.Art == "kick" && e.T == 1.25:
			t.Error("false kick survived")
		case e.Model != nil && *e.Model == 9:
			moved++
			if e.T != 2.011 {
				t.Errorf("flam at %v", e.T)
			}
		case e.Model != nil && *e.Model == 4:
			closed++
			if e.Art != "hhc" {
				t.Errorf("hat %+v", e)
			}
		default:
			// Untouched model events: bit for bit from the notes file.
			n := notes.Notes[*e.Model]
			if math.Float64bits(e.T) != math.Float64bits(n.Onset) || math.Float64bits(e.Vel) != math.Float64bits(n.Velocity) {
				t.Errorf("model event %d drifted: %v/%v vs %v/%v", *e.Model, e.T, e.Vel, n.Onset, n.Velocity)
			}
		}
	}
	if ghost != 1 || moved != 1 || closed != 1 {
		t.Errorf("ghost %d moved %d closed %d", ghost, moved, closed)
	}
}

func TestSaveConflictAndExported(t *testing.T) {
	notes, raw := parseTestNotes(t)
	store, layout, id := newStore(t)
	now := time.Now()
	seed := func() (Doc, error) { d, _ := Seed(notes, raw, gm(t), now); return d, nil }
	base, _ := seed()

	// A stale base_rev on a fresh job (nothing saved, seed is rev 0).
	_, err := store.Save(id, seed, 1, base.Events, now)
	var c *ConflictError
	if !errors.As(err, &c) || c.EditRev != 0 {
		t.Fatalf("stale on seed: %v", err)
	}
	if _, err := store.Load(id); !errors.Is(err, ErrNotFound) {
		t.Fatal("a refused save must not create the file")
	}
	// Two tabs: both loaded rev 0; the second save loses.
	if _, err := store.Save(id, seed, 0, base.Events, now); err != nil {
		t.Fatal(err)
	}
	_, err = store.Save(id, seed, 0, base.Events[:3], now)
	if !errors.As(err, &c) || c.EditRev != 1 {
		t.Fatalf("second tab: %v", err)
	}
	d, err := store.Load(id)
	if err != nil || d.EditRev != 1 || len(d.Events) != len(base.Events) {
		t.Fatalf("after conflict: %+v %v", d, err)
	}
	// Export marks only the current revision.
	if err := store.MarkExported(id, 5); err != nil {
		t.Fatal(err)
	}
	if d, _ = store.Load(id); d.ExportedRev != 0 {
		t.Fatalf("stale export marked: %d", d.ExportedRev)
	}
	if err := store.MarkExported(id, 1); err != nil {
		t.Fatal(err)
	}
	if d, _ = store.Load(id); d.ExportedRev != 1 {
		t.Fatalf("exported_rev %d", d.ExportedRev)
	}
	// No temp file is left behind and the file is JSON with a trailing newline.
	path, _ := layout.EditsPath(id, "drums")
	if _, err := os.Stat(path + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Error(".part left behind")
	}
	b, _ := os.ReadFile(path)
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Error("file not newline-terminated")
	}
	// RemoveJob takes the edits with it.
	if err := layout.RemoveJob(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Error("edits dir survived RemoveJob")
	}
}

func TestReadRejectsOtherVersions(t *testing.T) {
	store, layout, id := newStore(t)
	path, _ := layout.EditsPath(id, "drums")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(`{"version":2,"events":[]}`), 0o644)
	if _, err := store.Load(id); err == nil {
		t.Fatal("version 2 accepted")
	}
}

// TestSaveConcurrent: many tabs saving against the same base revision at
// once — exactly one wins, the rest see the winner's revision.
func TestSaveConcurrent(t *testing.T) {
	notes, raw := parseTestNotes(t)
	store, _, id := newStore(t)
	now := time.Now()
	seed := func() (Doc, error) { d, _ := Seed(notes, raw, gm(t), now); return d, nil }
	base, _ := seed()
	const n = 24
	var wg sync.WaitGroup
	wins := make([]bool, n)
	conflicts := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Save(id, seed, 0, base.Events[:i%len(base.Events)+1], now)
			var c *ConflictError
			switch {
			case err == nil:
				wins[i] = true
			case errors.As(err, &c):
				conflicts[i] = c.EditRev
			default:
				t.Errorf("save %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	won := 0
	for i := range wins {
		if wins[i] {
			won++
		} else if conflicts[i] != 1 {
			t.Errorf("save %d: conflict rev %d, want 1", i, conflicts[i])
		}
	}
	if won != 1 {
		t.Fatalf("%d winners", won)
	}
	d, err := store.Load(id)
	if err != nil || d.EditRev != 1 {
		t.Fatalf("after race: %+v %v", d, err)
	}
}

func TestSaveAfterJobRemoved(t *testing.T) {
	notes, raw := parseTestNotes(t)
	store, layout, id := newStore(t)
	now := time.Now()
	seed := func() (Doc, error) { d, _ := Seed(notes, raw, gm(t), now); return d, nil }
	base, _ := seed()
	if _, err := store.Save(id, seed, 0, base.Events, now); err != nil {
		t.Fatal(err)
	}
	if err := layout.RemoveJob(id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(id, seed, 0, base.Events, now); !errors.Is(err, ErrJobGone) {
		t.Fatalf("save after removal: %v", err)
	}
	dir, _ := layout.JobDir(id)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("job directory resurrected")
	}
}
