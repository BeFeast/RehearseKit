// Package midi writes Standard MIDI Files (format 1, 960 PPQ) for one
// transcribed stem: track 0 carries the tempo map and time signatures from
// the same grid.Map the DAWproject uses, track 1 the notes. Note times are
// already in beats (project-absolute), so the file lines up with the
// .dawproject and with the stems in any DAW that reads its tempo track.
package midi

import (
	"fmt"
	"io"
	"math"
	"os"
	"sort"

	gomidi "gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/BeFeast/RehearseKit/internal/pipeline/grid"
)

// PPQ is the resolution of the files written.
const PPQ = 960

// Note is one event in beats (project-absolute).
type Note struct {
	Beat     float64
	Duration float64
	Key      int
	Velocity float64 // 0..1
}

// Track is one stem's notes.
type Track struct {
	Name    string
	Channel int // 0..15; 9 for drums
	Notes   []Note
}

// Write renders the SMF for one track over the tempo map m. With a nil map
// the tempo track holds a single tempo of bpm at 4/4.
func Write(w io.Writer, m *grid.Map, bpm float64, tr Track) error {
	s := smf.NewSMF1()
	s.TimeFormat = smf.MetricTicks(PPQ)

	var tempo smf.Track
	tempo.Add(0, smf.MetaTrackSequenceName("Tempo map"))
	if m == nil {
		tempo.Add(0, smf.MetaMeter(4, 4), smf.MetaTempo(bpm))
	} else {
		type ev struct {
			tick int64
			msg  smf.Message
		}
		var evs []ev
		for _, ts := range m.TimeSignatures() {
			evs = append(evs, ev{ticks(ts.Beat), smf.MetaMeter(uint8(ts.Numerator), uint8(ts.Denominator))})
		}
		if pts := m.TempoPoints(); len(pts) == 0 {
			evs = append(evs, ev{0, smf.MetaTempo(m.BPM())})
		} else {
			// The stepped lane repeats the previous tempo at each beat;
			// MIDI only needs the new value.
			last := -1.0
			for _, p := range pts {
				if p.BPM != last {
					evs = append(evs, ev{ticks(p.Beat), smf.MetaTempo(p.BPM)})
					last = p.BPM
				}
			}
		}
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].tick < evs[j].tick })
		var at int64
		for _, e := range evs {
			tempo.Add(uint32(e.tick-at), e.msg)
			at = e.tick
		}
	}
	tempo.Close(0)
	if err := s.Add(tempo); err != nil {
		return err
	}

	if tr.Channel < 0 || tr.Channel > 15 {
		return fmt.Errorf("midi: channel %d", tr.Channel)
	}
	type ev struct {
		tick int64
		off  bool
		msg  gomidi.Message
	}
	var evs []ev
	ch := uint8(tr.Channel)
	for _, n := range tr.Notes {
		if n.Key < 0 || n.Key > 127 {
			return fmt.Errorf("midi: key %d", n.Key)
		}
		on := ticks(n.Beat)
		off := ticks(n.Beat + n.Duration)
		if off <= on {
			off = on + 1
		}
		vel := uint8(math.Round(math.Max(0, math.Min(1, n.Velocity)) * 127))
		if vel == 0 {
			vel = 1
		}
		evs = append(evs, ev{on, false, gomidi.NoteOn(ch, uint8(n.Key), vel)}, ev{off, true, gomidi.NoteOff(ch, uint8(n.Key))})
	}
	// Note-offs before note-ons at the same tick so retriggers work.
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].tick != evs[j].tick {
			return evs[i].tick < evs[j].tick
		}
		return evs[i].off && !evs[j].off
	})
	var notes smf.Track
	notes.Add(0, smf.MetaTrackSequenceName(tr.Name))
	var at int64
	for _, e := range evs {
		notes.Add(uint32(e.tick-at), e.msg)
		at = e.tick
	}
	notes.Close(0)
	if err := s.Add(notes); err != nil {
		return err
	}
	_, err := s.WriteTo(w)
	return err
}

// WriteFile writes via a temp file and rename.
func WriteFile(path string, m *grid.Map, bpm float64, tr Track) error {
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := Write(f, m, bpm, tr); err != nil {
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

func ticks(beat float64) int64 {
	if beat < 0 {
		beat = 0
	}
	return int64(math.Round(beat * PPQ))
}
