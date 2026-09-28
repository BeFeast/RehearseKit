package drums_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/db/dbtest"
	"github.com/BeFeast/RehearseKit/internal/drums"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/wavtest"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

const notesJSON = `{"stem":"drums","adapter":"adtof","model":"adtof_frame_rnn","notes":[
{"onset":0.5,"offset":0.6,"pitch":36,"velocity":0.83},
{"onset":1.0,"offset":1.1,"pitch":38,"velocity":0.91},
{"onset":1.0,"offset":1.1,"pitch":42,"velocity":0.71},
{"onset":1.75,"offset":1.85,"pitch":48,"velocity":0.77},
{"onset":2.0,"offset":2.1,"pitch":49,"velocity":0.95}]}`

// analysisJSON has a usable 120 BPM grid (8 bars of 4/4 from 0.5 s).
func analysisJSON() string {
	var beats, downs []string
	for i := 0; i < 32; i++ {
		t := 0.5 + float64(i)*0.5
		beats = append(beats, fmt.Sprint(t))
		if i%4 == 0 {
			downs = append(downs, fmt.Sprint(t))
		}
	}
	return `{"version":1,"grid":{"beats":[` + strings.Join(beats, ",") + `],"downbeats":[` + strings.Join(downs, ",") + `],"source":"beat_this"},` +
		`"sections":null,"instruments":{"drums":{"status":"ok","adapter":"adtof","model":"adtof_frame_rnn","notes":5},"bass":{"status":"failed","reason":"adapter timed out","notes":0}}}`
}

func newEnv(t *testing.T) (*httptest.Server, storage.Layout, *jobs.Store) {
	t.Helper()
	pool := dbtest.Pool(t)
	layout, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := jobs.NewStore(pool)
	jh := jobs.NewHandlers(store, layout, jobs.NewBroker(pool), jobs.Options{AnonRetention: time.Hour, UserRetention: time.Hour})
	mux := http.NewServeMux()
	drums.NewHandlers(jh, layout).Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, layout, store
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// createJob inserts an anonymous transcribe job (the claim token stands in
// for the owner's session: the handlers use jobs.AuthorizeWrite) and
// returns its id and token.
func createJob(t *testing.T, store *jobs.Store, transcribe bool) (string, string) {
	t.Helper()
	id := newID()
	token, hash, _ := jobs.NewClaimToken()
	if _, err := store.Create(context.Background(), id, jobs.CreateParams{ClaimTokenHash: hash, ProjectName: "Song", InputType: jobs.InputUpload,
		Quality: jobs.QualityHigh6, Transcribe: transcribe, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return id, token
}

func complete(t *testing.T, store *jobs.Store, id string, duration float64) {
	t.Helper()
	ctx := context.Background()
	for _, st := range []string{jobs.StatusConverting, jobs.StatusCompleted} {
		if err := jobs.Transition(ctx, store.Pool(), id, st, 0, st); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetAudioInfo(ctx, id, jobs.AudioInfo{DurationSeconds: duration, SampleRate: 48000, Channels: 2}); err != nil {
		t.Fatal(err)
	}
}

func writeArtefacts(t *testing.T, layout storage.Layout, id string) {
	t.Helper()
	dir, _ := layout.JobDir(id)
	_ = os.MkdirAll(filepath.Join(dir, "notes"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "analysis.json"), []byte(analysisJSON()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes", "drums.json"), []byte(notesJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

func call(t *testing.T, ts *httptest.Server, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rdr)
	if token != "" {
		req.Header.Set(jobs.ClaimTokenHeader, token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func code(body []byte) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Code
}

func TestArtefactRoutes(t *testing.T) {
	ts, layout, store := newEnv(t)
	id, token := createJob(t, store, true)
	base := "/api/v1/jobs/" + id

	// Nothing written yet → 404 with specific codes; unknown stem → 400.
	if st, body := call(t, ts, "GET", base+"/analysis", token, nil); st != 404 || code(body) != "analysis_not_found" {
		t.Fatalf("analysis missing: %d %s", st, body)
	}
	if st, body := call(t, ts, "GET", base+"/notes/drums", token, nil); st != 404 || code(body) != "notes_not_found" {
		t.Fatalf("notes missing: %d %s", st, body)
	}
	if st, body := call(t, ts, "GET", base+"/notes/vocals", token, nil); st != 400 || code(body) != "invalid_stem" {
		t.Fatalf("vocals notes: %d %s", st, body)
	}
	if st, body := call(t, ts, "GET", base+"/notes/../analysis", token, nil); st == 200 {
		t.Fatalf("traversal served: %s", body)
	}
	writeArtefacts(t, layout, id)
	st, body := call(t, ts, "GET", base+"/analysis", token, nil)
	if st != 200 || string(body) != analysisJSON() {
		t.Fatalf("analysis: %d %s", st, body)
	}
	st, body = call(t, ts, "GET", base+"/notes/drums", token, nil)
	if st != 200 || string(body) != notesJSON {
		t.Fatalf("notes: %d %s", st, body)
	}
	// Read access, not owner access: a stranger gets 403 on an anonymous
	// job only for edits; artefacts follow the job's read rule (anyone
	// with the link while it lives).
	if st, _ := call(t, ts, "GET", base+"/analysis", "", nil); st != 200 {
		t.Fatalf("analysis without token: %d", st)
	}
	if st, body := call(t, ts, "GET", base+"/drums/edits", "", nil); st != 403 || code(body) != "claim_token_required" {
		t.Fatalf("edits without token: %d %s", st, body)
	}
}

func TestEditsLifecycle(t *testing.T) {
	ts, layout, store := newEnv(t)
	id, token := createJob(t, store, true)
	base := "/api/v1/jobs/" + id + "/drums/edits"

	if st, body := call(t, ts, "GET", base, token, nil); st != 409 || code(body) != "not_ready" {
		t.Fatalf("pending: %d %s", st, body)
	}
	complete(t, store, id, 16.5)
	if st, body := call(t, ts, "GET", base, token, nil); st != 404 || code(body) != "drum_notes_not_found" {
		t.Fatalf("no artefacts: %d %s", st, body)
	}
	writeArtefacts(t, layout, id)

	// Seed: rev 0, nothing on disk.
	st, body := call(t, ts, "GET", base, token, nil)
	if st != 200 {
		t.Fatalf("get seed: %d %s", st, body)
	}
	var res drums.EditsResponse
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatal(err)
	}
	if res.Doc.EditRev != 0 || len(res.Doc.Events) != 5 || res.Doc.Events[0].Art != "kick" || res.Doc.Events[4].Art != "crash" || res.Profile.ID != "gm" || res.Profile.Notes["hho"] != 46 {
		t.Fatalf("seed %+v", res)
	}
	if res.Grid == nil || !res.Grid.Constant || res.Grid.BPM < 119.9 || res.Grid.BPM > 120.1 || res.Grid.Numerator != 4 || len(res.Grid.Beats) != 32 {
		t.Fatalf("grid %+v (%s)", res.Grid, res.GridError)
	}
	if res.Duration != 16.5 || res.StaleModel || res.Model.Count != 5 || res.Model.NotesSHA256 != res.Doc.ModelRev.NotesSHA256 || len(res.Warnings) != 0 {
		t.Fatalf("meta %+v", res)
	}
	editsPath, _ := layout.EditsPath(id, "drums")
	if _, err := os.Stat(editsPath); err == nil {
		t.Fatal("GET wrote the edits file")
	}

	// Save rev 1: delete the tom, add a ghost snare.
	evs := res.Doc.Events
	evs = append(evs[:3], evs[4:]...)
	evs = append(evs, drums.Event{ID: "u1", Art: "snare", T: 1.4375, Vel: 0.2, Src: "manual"})
	st, body = call(t, ts, "PUT", base, token, map[string]any{"base_rev": 0, "events": evs})
	if st != 200 {
		t.Fatalf("put: %d %s", st, body)
	}
	var saved drums.SaveResponse
	_ = json.Unmarshal(body, &saved)
	if saved.EditRev != 1 || saved.UpdatedAt.IsZero() {
		t.Fatalf("saved %+v", saved)
	}
	// Stale base → 409 with the current revision; a refresh sees rev 1.
	st, body = call(t, ts, "PUT", base, token, map[string]any{"base_rev": 0, "events": evs})
	if st != 409 || code(body) != "edit_conflict" || !strings.Contains(string(body), `"edit_rev":1`) {
		t.Fatalf("stale put: %d %s", st, body)
	}
	st, body = call(t, ts, "GET", base, token, nil)
	if err := json.Unmarshal(body, &res); err != nil || st != 200 {
		t.Fatal(st, err)
	}
	if res.Doc.EditRev != 1 || len(res.Doc.Events) != 5 || res.Doc.Events[3].ID != "u1" || res.Doc.Events[3].T != 1.4375 {
		t.Fatalf("after save %+v", res.Doc)
	}
	// Model events stay bit for bit; the notes file did not change.
	if res.Doc.Events[0].T != 0.5 || res.Doc.Events[0].Vel != 0.83 || res.StaleModel {
		t.Fatalf("model events %+v", res.Doc.Events)
	}
	// Invalid bodies.
	for name, body := range map[string]any{
		"art":     map[string]any{"base_rev": 1, "events": []map[string]any{{"id": "u2", "art": "hh", "t": 1, "vel": 0.5, "src": "manual"}}},
		"time":    map[string]any{"base_rev": 1, "events": []map[string]any{{"id": "u2", "art": "kick", "t": 99, "vel": 0.5, "src": "manual"}}},
		"unknown": map[string]any{"base_rev": 1, "events": []any{}, "extra": 1},
		"neg":     map[string]any{"base_rev": -1, "events": []any{}},
		"null":    map[string]any{"base_rev": 1, "events": nil},
		"missing": map[string]any{"base_rev": 1},
	} {
		if st, _ := call(t, ts, "PUT", base, token, body); st != 400 {
			t.Errorf("%s: %d", name, st)
		}
	}
	// Over the body limit → 413, and the revision is untouched.
	big := make([]map[string]any, 0, 60000)
	for i := 0; i < 60000; i++ {
		big = append(big, map[string]any{"id": fmt.Sprintf("u%027d", i+10), "art": "kick", "t": 1.123456789, "vel": 0.512345678, "src": "manual"})
	}
	if b, _ := json.Marshal(map[string]any{"base_rev": 1, "events": big}); len(b) <= drums.MaxBodyBytes {
		t.Fatalf("test payload is only %d bytes", len(b))
	}
	if st, body := call(t, ts, "PUT", base, token, map[string]any{"base_rev": 1, "events": big}); st != 413 || code(body) != "body_too_large" {
		t.Fatalf("oversized put: %d %s", st, body[:min(len(body), 200)])
	}
	// An empty event list is a valid revision (everything deleted).
	if st, body := call(t, ts, "PUT", base, token, map[string]any{"base_rev": 1, "events": []any{}}); st != 200 {
		t.Fatalf("empty put: %d %s", st, body)
	}
	// A changed model output flags the revision as stale.
	np, _ := layout.NotesPath(id, "drums")
	_ = os.WriteFile(np, []byte(strings.Replace(notesJSON, "0.83", "0.84", 1)), 0o644)
	st, body = call(t, ts, "GET", base, token, nil)
	_ = json.Unmarshal(body, &res)
	if st != 200 || !res.StaleModel || res.Doc.EditRev != 2 {
		t.Fatalf("stale model: %d %+v", st, res)
	}
	// Stems are untouched by the whole exchange (no stems dir was ever created).
	dir, _ := layout.JobDir(id)
	if _, err := os.Stat(filepath.Join(dir, "stems")); err == nil {
		t.Error("stems dir appeared")
	}
}

func TestEditsNotTranscribed(t *testing.T) {
	ts, layout, store := newEnv(t)
	id, token := createJob(t, store, false)
	complete(t, store, id, 10)
	writeArtefacts(t, layout, id)
	if st, body := call(t, ts, "GET", "/api/v1/jobs/"+id+"/drums/edits", token, nil); st != 404 || code(body) != "drum_notes_not_found" {
		t.Fatalf("%d %s", st, body)
	}
	// Transcribed, but the drums adapter failed.
	id, token = createJob(t, store, true)
	complete(t, store, id, 10)
	writeArtefacts(t, layout, id)
	ap, _ := layout.AnalysisPath(id)
	_ = os.WriteFile(ap, []byte(`{"version":1,"grid":null,"grid_error":"too few beats","sections":null,"instruments":{"drums":{"status":"failed","reason":"adapter crashed","notes":0}}}`), 0o644)
	st, body := call(t, ts, "GET", "/api/v1/jobs/"+id+"/drums/edits", token, nil)
	if st != 404 || code(body) != "drum_notes_not_found" || !strings.Contains(string(body), "adapter crashed") {
		t.Fatalf("%d %s", st, body)
	}
	// No grid: the editor still opens, in seconds.
	_ = os.WriteFile(ap, []byte(`{"version":1,"grid":null,"grid_error":"too few beats","sections":null,"instruments":{"drums":{"status":"ok","adapter":"adtof","notes":5}}}`), 0o644)
	st, body = call(t, ts, "GET", "/api/v1/jobs/"+id+"/drums/edits", token, nil)
	var res drums.EditsResponse
	_ = json.Unmarshal(body, &res)
	if st != 200 || res.Grid != nil || res.GridError != "too few beats" || len(res.Doc.Events) != 5 {
		t.Fatalf("no grid: %d %+v", st, res)
	}
}

func TestExportRoute(t *testing.T) {
	ts, layout, store := newEnv(t)
	id, token := createJob(t, store, true)
	complete(t, store, id, 2.5)
	writeArtefacts(t, layout, id)
	dir, _ := layout.JobDir(id)
	_ = os.MkdirAll(filepath.Join(dir, "stems"), 0o755)
	ctx := context.Background()
	for _, name := range []string{"vocals", "drums", "bass", "other", "guitar", "piano"} {
		p := filepath.Join(dir, "stems", name+".wav")
		frames, err := wavtest.Write(p, wavtest.Options{Seconds: 0.5, Frequency: 100 + float64(len(name))*50})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertStem(ctx, id, jobs.Stem{Name: name, Frames: frames, SampleRate: 48000, BitDepth: 24, Channels: 2}, "stems/"+name+".wav", nil); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/v1/jobs/" + id + "/drums/export"

	// Stale revision → 409 with the current one (the seed is rev 0).
	if st, body := call(t, ts, "POST", base, token, map[string]any{"edit_rev": 3}); st != 409 || code(body) != "edit_conflict" || !strings.Contains(string(body), `"edit_rev":0`) {
		t.Fatalf("stale: %d %s", st, body)
	}
	// Without the token → 403 like the edits.
	if st, _ := call(t, ts, "POST", base, "", map[string]any{"edit_rev": 0}); st != 403 {
		t.Fatalf("no token: %d", st)
	}
	// Bad bodies.
	if st, _ := call(t, ts, "POST", base, token, map[string]any{"edit_rev": -1, "x": 1}); st != 400 {
		t.Fatalf("unknown field: %d", st)
	}
	req, _ := http.NewRequest("POST", ts.URL+base, strings.NewReader("edit_rev=abc"))
	req.Header.Set(jobs.ClaimTokenHeader, token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("form abc: %d", resp.StatusCode)
	}

	// Export the seed as a form post (the SPA's native download).
	req, _ = http.NewRequest("POST", ts.URL+base, strings.NewReader("edit_rev=0"))
	req.Header.Set(jobs.ClaimTokenHeader, token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" || resp.Header.Get("Content-Disposition") != `attachment; filename="Song-drums-r0.zip"` {
		t.Fatalf("export: %d %s %s", resp.StatusCode, resp.Header, body[:min(len(body), 200)])
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 2 || zr.File[0].Name != "drums.mid" || zr.File[1].Name != "drums.dawproject" {
		t.Fatalf("entries %v", zr.File)
	}
	mixPath, _ := layout.MixPath(id, "nodrums")
	st1, err := os.Stat(mixPath)
	if err != nil {
		t.Fatalf("mix not cached: %v", err)
	}
	// Seed export does not create the edits file, so exported_rev is implicit.
	editsPath, _ := layout.EditsPath(id, "drums")
	if _, err := os.Stat(editsPath); err == nil {
		t.Fatal("export of the seed wrote edits/drums.json")
	}

	// Save a revision, export it as JSON; exported_rev follows, the mix is reused.
	st, body2 := call(t, ts, "GET", "/api/v1/jobs/"+id+"/drums/edits", token, nil)
	var res drums.EditsResponse
	_ = json.Unmarshal(body2, &res)
	evs := append(res.Doc.Events, drums.Event{ID: "u1", Art: "hho", T: 0.3, Vel: 0.5, Src: "manual"})
	if st, body := call(t, ts, "PUT", "/api/v1/jobs/"+id+"/drums/edits", token, map[string]any{"base_rev": 0, "events": evs}); st != 200 {
		t.Fatalf("put: %d %s", st, body)
	}
	st, body = call(t, ts, "POST", base, token, map[string]any{"edit_rev": 1})
	if st != 200 {
		t.Fatalf("export rev 1: %d %s", st, body)
	}
	zr, _ = zip.NewReader(bytes.NewReader(body), int64(len(body)))
	rc, _ := zr.File[1].Open()
	inner, _ := io.ReadAll(rc)
	rc.Close()
	ir, err := zip.NewReader(bytes.NewReader(inner), int64(len(inner)))
	if err != nil {
		t.Fatal(err)
	}
	var proj string
	for _, f := range ir.File {
		if f.Name == "project.xml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			proj = string(b)
		}
	}
	if !strings.Contains(proj, `key="46"`) || !strings.Contains(proj, `name="Mix (no drums)"`) {
		t.Errorf("project.xml of rev 1: %s", proj)
	}
	st2, _ := os.Stat(mixPath)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("mix re-rendered on the second export")
	}
	st, body2 = call(t, ts, "GET", "/api/v1/jobs/"+id+"/drums/edits", token, nil)
	_ = json.Unmarshal(body2, &res)
	if st != 200 || res.Doc.ExportedRev != 1 || res.Doc.EditRev != 1 {
		t.Fatalf("exported_rev: %+v", res.Doc)
	}
	// Removing the job takes the cache with it.
	_ = layout.RemoveJob(id)
	if _, err := os.Stat(mixPath); err == nil {
		t.Error("mix survived RemoveJob")
	}
}
