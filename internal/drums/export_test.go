package drums

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/analysis"
	"github.com/BeFeast/RehearseKit/internal/pipeline/dawproject"
	"github.com/BeFeast/RehearseKit/internal/pipeline/grid"
	"github.com/BeFeast/RehearseKit/internal/pipeline/midi"
	"github.com/BeFeast/RehearseKit/internal/pipeline/peaks"
	"github.com/BeFeast/RehearseKit/internal/pipeline/wavtest"
)

var update = flag.Bool("update", false, "rewrite testdata/export-project.xml")

// workerMIDI is midi/<stem>.mid as the worker's transcription stage writes
// it for the drums (internal/worker/stages.go): notes in beats through the
// grid, or at the single tempo when there is none.
func workerMIDI(t *testing.T, m *grid.Map, bpm float64, notes analysis.Notes) []byte {
	t.Helper()
	beat := func(sec float64) float64 {
		if m != nil {
			return m.Beat(sec)
		}
		return sec * bpm / 60
	}
	mt := midi.Track{Name: dawproject.TrackName("drums"), Channel: 9}
	for _, n := range notes.Notes {
		start := beat(n.Onset)
		mt.Notes = append(mt.Notes, midi.Note{Beat: start, Duration: midi.Duration("drums", start, beat(n.Offset)), Key: n.Pitch, Velocity: n.Velocity})
	}
	var buf bytes.Buffer
	if err := midi.Write(&buf, m, bpm, mt); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestExportSeedEqualsWorker is the regression that pins the export to the
// worker: the untouched seed of a real stand job (a 45 s clip transcribed
// on the CPU stand) exports byte for byte the midi/drums.mid the worker
// writes for it, with the job's grid (refused on this clip: it is on an
// unclear metrical level, so both fall back to the single tempo) and with
// the grid forced.
func TestExportSeedEqualsWorker(t *testing.T) {
	dir := filepath.Join("testdata", "clip45")
	ab, err := os.ReadFile(filepath.Join(dir, "analysis.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := analysis.Parse(ab)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "notes-drums.json"))
	if err != nil {
		t.Fatal(err)
	}
	notes, err := analysis.ParseNotes(raw)
	if err != nil {
		t.Fatal(err)
	}
	var tempo struct {
		BPM float64 `json:"bpm"`
	}
	tb, _ := os.ReadFile(filepath.Join(dir, "tempo.json"))
	if err := json.Unmarshal(tb, &tempo); err != nil {
		t.Fatal(err)
	}
	const duration = 45.0
	doc, warnings := Seed(notes, raw, gm(t), time.Now())
	if len(warnings) != 0 || len(doc.Events) != 83 {
		t.Fatalf("seed: %d events, warnings %v", len(doc.Events), warnings)
	}
	job, jobErr := grid.BuildWith(res.Grid.Beats, res.Grid.Downbeats, duration, grid.Options{RefBPM: &tempo.BPM})
	if !errors.Is(jobErr, grid.ErrUncertain) {
		t.Fatalf("clip45 grid: want ErrUncertain, got %v", jobErr)
	}
	forced, err := grid.BuildWith(res.Grid.Beats, res.Grid.Downbeats, duration, grid.Options{RefBPM: &tempo.BPM, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []*grid.Map{job, forced} {
		e := &Export{ProjectName: "ES clip45", Duration: duration, BPM: tempo.BPM, Grid: m, Profile: gm(t), Events: doc.Events}
		got, err := e.MIDI()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, workerMIDI(t, m, tempo.BPM, notes)) {
			t.Fatalf("grid %v: export of the seed differs from the worker's midi/drums.mid", m != nil)
		}
		// Moving one hit changes the file; the others keep their ticks.
		moved := append([]Event(nil), doc.Events...)
		moved[10].T += 0.011
		e.Events = moved
		if got2, _ := e.MIDI(); bytes.Equal(got, got2) {
			t.Fatal("a moved hit did not change the MIDI")
		}
	}
}

func steadyGrid(t *testing.T) *grid.Map {
	t.Helper()
	var beats, downs []float64
	for i := 0; i < 40; i++ {
		tm := 0.25 + float64(i)*0.5
		beats = append(beats, tm)
		if i%4 == 0 {
			downs = append(downs, tm)
		}
	}
	m, err := grid.Build(beats, downs, 21)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fixtureExport(t *testing.T) *Export {
	t.Helper()
	return &Export{
		ProjectName: "Fixture Song", Duration: 21, BPM: 119.5, Grid: steadyGrid(t), Profile: gm(t), Version: "test",
		Sections: []analysis.Section{{Start: 0.25, End: 8.25, Label: "Intro"}, {Start: 8.25, End: 21, Label: "Verse"}},
		Events: []Event{
			{ID: "m0", Art: "kick", T: 0.25, Vel: 0.9, Src: SourceModel},
			{ID: "m1", Art: "hhc", T: 0.5, Vel: 0.6, Src: SourceModel},
			{ID: "u1", Art: "snare", T: 1.2375, Vel: 0.2, Src: SourceManual},
			{ID: "m2", Art: "hho", T: 1.25, Vel: 0.7, Src: SourceModel},
			{ID: "m3", Art: "crash", T: 8.25, Vel: 1, Src: SourceModel},
		},
	}
}

func TestExportProjectGolden(t *testing.T) {
	e := fixtureExport(t)
	p, err := e.Project()
	if err != nil {
		t.Fatal(err)
	}
	got, err := dawproject.ProjectXML(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "export-project.xml")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("project.xml differs from golden\n%s", got)
	}
	s := string(got)
	for _, want := range []string{
		`<Track contentType="audio" loaded="true" id="track-mix" name="Mix (no drums)">`,
		`<Track contentType="audio" loaded="true" id="track-drums" name="Drums" color="#FFA500">`,
		`<Track contentType="notes" loaded="true" id="track-drums-midi" name="Drums MIDI" color="#FFA500">`,
		`<Marker time="4.000000" name="Intro">`,
		`<Clip time="3.500000"`, // second 0 sits half a beat before beat 4 (the lead-in bar)
		`channel="9" key="36" vel="0.900000"`,
		`channel="9" key="46" vel="0.700000"`,
		`channel="9" key="38" vel="0.200000"`,
		`<TimeSignature denominator="4" numerator="4" id="timesig">`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("project.xml lacks %s", want)
		}
	}
	if strings.Contains(s, "<TempoAutomation") {
		t.Error("steady grid must not write a tempo lane")
	}
	// Notes keep absolute time: the manual ghost lands 0.0125 s (0.025 beat)
	// before the open hat.
	if !strings.Contains(s, `<Note time="2.475000" duration="0.250000" channel="9" key="38"`) || !strings.Contains(s, `<Note time="2.500000" duration="0.250000" channel="9" key="46"`) {
		t.Errorf("note times: %s", s)
	}
	xsdCheck(t, got)
}

// xsdCheck validates project.xml against the DAWproject schema kept with
// the dawproject package.
func xsdCheck(t *testing.T, projectXML []byte) {
	t.Helper()
	xmllint, err := exec.LookPath("xmllint")
	if err != nil {
		t.Skip("xmllint not installed")
	}
	path := filepath.Join(t.TempDir(), "project.xml")
	if err := os.WriteFile(path, projectXML, 0o644); err != nil {
		t.Fatal(err)
	}
	xsd, _ := filepath.Abs(filepath.Join("..", "pipeline", "dawproject", "testdata", "Project.xsd"))
	out, err := exec.Command(xmllint, "--noout", "--schema", xsd, path).CombinedOutput()
	if err != nil {
		t.Fatalf("xmllint: %v\n%s", err, out)
	}
}

func TestExportUnknownArticulation(t *testing.T) {
	e := fixtureExport(t)
	e.Events = append(e.Events, Event{ID: "u9", Art: "cowbell", T: 3, Vel: 0.5, Src: SourceManual})
	if _, err := e.MIDI(); err == nil {
		t.Fatal("unknown articulation exported")
	}
}

func TestExportWithoutGrid(t *testing.T) {
	e := fixtureExport(t)
	e.Grid = nil
	mid, err := e.MIDI()
	if err != nil || len(mid) == 0 {
		t.Fatal(err)
	}
	p, err := e.Project()
	if err != nil {
		t.Fatal(err)
	}
	xml, _ := dawproject.ProjectXML(p)
	// Single tempo from BPM; the crash at 8.25 s → 8.25*119.5/60 beats.
	if !strings.Contains(string(xml), `value="119.500000" id="tempo"`) || !strings.Contains(string(xml), `<Note time="16.431250"`) {
		t.Errorf("no-grid project: %s", xml)
	}
}

// TestMixdownSum renders the no-drums mix of three synthetic stems and
// checks every sample against the sum of the decoded inputs.
func TestMixdownSum(t *testing.T) {
	store, layout, id := newStore(t)
	dir, _ := layout.JobDir(id)
	_ = os.MkdirAll(filepath.Join(dir, "stems"), 0o755)
	j := &jobs.Job{ID: id}
	specs := map[string]wavtest.Options{
		"vocals": {Seconds: 0.5, Frequency: 440, Amplitude: 0.5},
		"bass":   {Seconds: 0.5, Frequency: 55, Amplitude: 0.7},
		"other":  {Seconds: 0.4, Frequency: 1000, Amplitude: 0.3}, // shorter: padded with silence
		"drums":  {Seconds: 0.5, Frequency: 200, Amplitude: 0.9},  // excluded
	}
	for name, o := range specs {
		if _, err := wavtest.Write(filepath.Join(dir, "stems", name+".wav"), o); err != nil {
			t.Fatal(err)
		}
		j.Stems = append(j.Stems, jobs.Stem{Name: name})
	}
	path, err := store.RenderNoDrums(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(path)) != "mixes" || filepath.Base(path) != "nodrums.wav" {
		t.Fatalf("path %s", path)
	}
	read := func(p string) (*peaks.Decoder, []float32) {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		d, err := peaks.OpenWAV(f)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out := make([]float32, 0, d.Frames*uint64(d.Channels))
		buf := make([]float32, 4096*int(d.Channels))
		for {
			n, err := d.ReadFrames(buf)
			out = append(out, buf[:n*int(d.Channels)]...)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		return d, out
	}
	mixDec, mix := read(path)
	if !mixDec.Float || mixDec.BitDepth != 32 || mixDec.SampleRate != 48000 || mixDec.Channels != 2 || mixDec.Frames != 24000 {
		t.Fatalf("mix header %+v", mixDec)
	}
	want := make([]float64, len(mix))
	for _, name := range []string{"vocals", "bass", "other"} {
		_, s := read(filepath.Join(dir, "stems", name+".wav"))
		for i, v := range s {
			want[i] += float64(v)
		}
	}
	var maxErr float64
	for i := range mix {
		maxErr = math.Max(maxErr, math.Abs(float64(mix[i])-want[i]))
	}
	if maxErr > 1e-6 {
		t.Fatalf("mix differs from the sum by up to %g", maxErr)
	}
	if mix[2*23999] != 0 || mix[2*23999+1] != 0 {
		// after 0.4 s only vocals+bass play; check the tail is not garbage
		var tail float64
		for i := 2 * 19200; i < len(mix); i++ {
			tail += math.Abs(float64(mix[i]))
		}
		if tail == 0 {
			t.Fatal("tail silent although two stems continue")
		}
	}
	// The render is cached: a second call returns the same file untouched.
	st1, _ := os.Stat(path)
	_ = os.Remove(filepath.Join(dir, "stems", "vocals.wav"))
	path2, err := store.RenderNoDrums(context.Background(), j)
	st2, _ := os.Stat(path2)
	if err != nil || path2 != path || !st1.ModTime().Equal(st2.ModTime()) || st1.Size() != st2.Size() {
		t.Fatalf("cache: %v %s %v", err, path2, st2)
	}
	if _, err := os.Stat(path + ".part"); err == nil {
		t.Error(".part left behind")
	}
}

func TestMixdownNoStems(t *testing.T) {
	store, _, id := newStore(t)
	if _, err := store.RenderNoDrums(context.Background(), &jobs.Job{ID: id, Stems: []jobs.Stem{{Name: "drums"}}}); err == nil {
		t.Fatal("rendered a mix without inputs")
	}
}

func TestWriteZip(t *testing.T) {
	e := fixtureExport(t)
	dir := t.TempDir()
	for _, name := range []string{"drums", "mix"} {
		if _, err := wavtest.Write(filepath.Join(dir, name+".wav"), wavtest.Options{Seconds: 0.2}); err != nil {
			t.Fatal(err)
		}
	}
	e.DrumsPath, e.MixPath = filepath.Join(dir, "drums.wav"), filepath.Join(dir, "mix.wav")
	var buf bytes.Buffer
	if err := e.WriteZip(&buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 2 || zr.File[0].Name != "drums.mid" || zr.File[1].Name != "drums.dawproject" {
		t.Fatalf("entries %v", zr.File)
	}
	rc, _ := zr.File[1].Open()
	inner, _ := io.ReadAll(rc)
	rc.Close()
	ir, err := zip.NewReader(bytes.NewReader(inner), int64(len(inner)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]*zip.File{}
	for _, f := range ir.File {
		names[f.Name] = f
		if f.Flags&0x8 != 0 {
			t.Errorf("%s uses a data descriptor", f.Name)
		}
	}
	for _, n := range []string{"project.xml", "metadata.xml", "audio/mix.wav", "audio/drums.wav"} {
		if names[n] == nil {
			t.Errorf("drums.dawproject lacks %s", n)
		}
	}
	rc, _ = names["project.xml"].Open()
	proj, _ := io.ReadAll(rc)
	rc.Close()
	if !strings.Contains(string(proj), `<File path="audio/mix.wav">`) || !strings.Contains(string(proj), `name="Mix (no drums)"`) {
		t.Errorf("project.xml: %s", proj)
	}
	mid, _ := e.MIDI()
	rc, _ = zr.File[0].Open()
	gotMid, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(mid, gotMid) {
		t.Error("drums.mid in the zip differs from MIDI()")
	}
	if ZipName("My Song", 3) != "My Song-drums-r3.zip" {
		t.Error(ZipName("My Song", 3))
	}
}
