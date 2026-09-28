package drums

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/BeFeast/RehearseKit/internal/pipeline/analysis"
	"github.com/BeFeast/RehearseKit/internal/pipeline/dawproject"
	"github.com/BeFeast/RehearseKit/internal/pipeline/grid"
	"github.com/BeFeast/RehearseKit/internal/pipeline/midi"
)

// Export describes one export of an edit revision.
type Export struct {
	ProjectName string
	Duration    float64
	// BPM is the single tempo used when Grid is nil (0 → dawproject.DefaultBPM).
	BPM float64
	// Grid is the cleaned beat map, nil when the job has none.
	Grid     *grid.Map
	Sections []analysis.Section
	Profile  Profile
	Events   []Event
	// DrumsPath and MixPath are the audio tracks of the .dawproject: the
	// drum stem and the cached no-drums mix.
	DrumsPath string
	MixPath   string
	// Version is written as the generator version.
	Version string
}

// ZipName is the attachment name of an export.
func ZipName(safeProjectName string, rev int) string {
	return fmt.Sprintf("%s-drums-r%d.zip", safeProjectName, rev)
}

// tempo is the single tempo used without a grid: the detected BPM, or the
// DAWproject default when none was detected (the worker does the same).
func (e *Export) tempo() float64 {
	if e.BPM > 0 {
		return e.BPM
	}
	return dawproject.DefaultBPM
}

func (e *Export) beat(sec float64) float64 {
	if e.Grid != nil {
		return e.Grid.Beat(sec)
	}
	return sec * e.tempo() / 60
}

// tracks converts the events into the MIDI and DAWproject note lists with
// the same rules the worker applies to the model output (midi.Duration),
// so an export of the untouched seed equals midi/drums.mid.
func (e *Export) tracks() (midi.Track, dawproject.NoteTrack, error) {
	mt := midi.Track{Name: "Drums", Channel: 9}
	nt := dawproject.NoteTrack{Stem: "drums", Channel: 9}
	for i, ev := range e.Events {
		key, ok := e.Profile.Key(ev.Art)
		if !ok {
			return mt, nt, fmt.Errorf("events[%d]: articulation %q has no key in profile %s", i, ev.Art, e.Profile.ID)
		}
		start := e.beat(ev.T)
		dur := midi.Duration("drums", start, start)
		mt.Notes = append(mt.Notes, midi.Note{Beat: start, Duration: dur, Key: key, Velocity: ev.Vel})
		nt.Notes = append(nt.Notes, dawproject.Note{Beat: start, Duration: dur, Key: key, Velocity: ev.Vel})
	}
	return mt, nt, nil
}

// MIDI renders drums.mid.
func (e *Export) MIDI() ([]byte, error) {
	mt, _, err := e.tracks()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := midi.Write(&buf, e.Grid, e.tempo(), mt); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Project builds the .dawproject description: the no-drums mix, the drum
// stem, the edited notes, markers from the sections, the tempo map.
func (e *Export) Project() (dawproject.Project, error) {
	_, nt, err := e.tracks()
	if err != nil {
		return dawproject.Project{}, err
	}
	nt.Name = "Drums MIDI"
	p := dawproject.Project{
		Name: e.ProjectName, DurationSeconds: e.Duration, Grid: e.Grid, GeneratorVersion: e.Version,
		Stems: []dawproject.Stem{
			{Name: "mix", Label: "Mix (no drums)", Path: e.MixPath},
			{Name: "drums", Path: e.DrumsPath},
		},
		NoteTracks: []dawproject.NoteTrack{nt},
	}
	if e.BPM > 0 {
		bpm := e.tempo()
		p.BPM = &bpm
	}
	for _, s := range e.Sections {
		p.Markers = append(p.Markers, dawproject.Marker{Beat: e.beat(s.Start), Name: s.Label})
	}
	return p, nil
}

// WriteZip streams the export archive: drums.mid (deflated) and
// drums.dawproject (stored; itself a zip without data descriptors).
func (e *Export) WriteZip(w io.Writer) error {
	mid, err := e.MIDI()
	if err != nil {
		return err
	}
	proj, err := e.Project()
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	now := time.Now()
	f, err := zw.CreateHeader(&zip.FileHeader{Name: "drums.mid", Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}
	if _, err := f.Write(mid); err != nil {
		return err
	}
	f, err = zw.CreateHeader(&zip.FileHeader{Name: "drums.dawproject", Method: zip.Store, Modified: now})
	if err != nil {
		return err
	}
	if err := dawproject.Write(f, proj); err != nil {
		return err
	}
	return zw.Close()
}
