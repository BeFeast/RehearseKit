package midi

import (
	"bytes"
	"math"
	"testing"

	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/BeFeast/RehearseKit/internal/pipeline/grid"
)

func TestRoundTrip(t *testing.T) {
	// 8 beats at 120 then 8 at 150 BPM, 4/4.
	var beats, downs []float64
	tm := 0.0
	for k := 0; k < 16; k++ {
		beats = append(beats, tm)
		if k%4 == 0 {
			downs = append(downs, tm)
		}
		if k < 8 {
			tm += 0.5
		} else {
			tm += 0.4
		}
	}
	m, err := grid.Build(beats, downs, tm)
	if err != nil {
		t.Fatal(err)
	}
	tr := Track{Name: "Bass", Channel: 0, Notes: []Note{
		{Beat: 1, Duration: 0.5, Key: 40, Velocity: 0.8},
		{Beat: 1.5, Duration: 0.5, Key: 40, Velocity: 0.8}, // retrigger at the previous off
		{Beat: 9, Duration: 2, Key: 43, Velocity: 1},
	}}
	var buf bytes.Buffer
	if err := Write(&buf, m, 0, tr); err != nil {
		t.Fatal(err)
	}
	f, err := smf.ReadFrom(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if f.NumTracks() != 2 || f.TimeFormat != smf.MetricTicks(PPQ) {
		t.Fatalf("header tracks=%d tf=%v", f.NumTracks(), f.TimeFormat)
	}
	var tempos []float64
	var tempoTicks []int64
	var meter uint8
	var abs int64
	for _, ev := range f.Tracks[0] {
		abs += int64(ev.Delta)
		var bpm float64
		if ev.Message.GetMetaTempo(&bpm) {
			tempos = append(tempos, bpm)
			tempoTicks = append(tempoTicks, abs)
		}
		var num, den uint8
		if ev.Message.GetMetaMeter(&num, &den) {
			meter = num
		}
	}
	if meter != 4 || len(tempos) != 2 || math.Abs(tempos[0]-120) > 0.01 || math.Abs(tempos[1]-150) > 0.01 || tempoTicks[1] != 8*PPQ {
		t.Fatalf("tempo track: meter=%d tempos=%v ticks=%v", meter, tempos, tempoTicks)
	}
	type noteEv struct {
		tick int64
		on   bool
		key  uint8
	}
	var notes []noteEv
	abs = 0
	for _, ev := range f.Tracks[1] {
		abs += int64(ev.Delta)
		var ch, key, vel uint8
		if ev.Message.GetNoteStart(&ch, &key, &vel) {
			notes = append(notes, noteEv{abs, true, key})
		} else if ev.Message.GetNoteEnd(&ch, &key) {
			notes = append(notes, noteEv{abs, false, key})
		}
	}
	want := []noteEv{{PPQ, true, 40}, {int64(1.5 * PPQ), false, 40}, {int64(1.5 * PPQ), true, 40}, {2 * PPQ, false, 40}, {9 * PPQ, true, 43}, {11 * PPQ, false, 43}}
	if len(notes) != len(want) {
		t.Fatalf("notes %+v", notes)
	}
	for i := range want {
		if notes[i] != want[i] {
			t.Fatalf("note %d: %+v want %+v", i, notes[i], want[i])
		}
	}
}

func TestNilMap(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, nil, 136, Track{Name: "Drums", Channel: 9, Notes: []Note{{Beat: 0, Duration: 0.25, Key: 36, Velocity: 1}}}); err != nil {
		t.Fatal(err)
	}
	f, err := smf.ReadFrom(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var bpm float64
	for _, ev := range f.Tracks[0] {
		ev.Message.GetMetaTempo(&bpm)
	}
	if math.Abs(bpm-136) > 0.01 {
		t.Fatalf("bpm %v", bpm)
	}
}
