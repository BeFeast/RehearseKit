// Package grid turns detected beats and downbeats into the one mapping the
// pipeline uses between seconds and DAW beats: clip warps, note times, the
// tempo automation and the .mid tempo track are all derived from a Map, so
// audio and MIDI can not drift apart.
//
// Detected beats are cleaned (doubled, double-time and missed beats) and
// then fitted with the fewest stretches of constant tempo that keep every
// beat within fitTol of the grid: a steady song gets one tempo, a song
// with tempo changes a few steps, never a per-beat zig-zag. The map is
// built on the fitted beat times. Beat positions are uniform in beat units
// (beat k sits at Offset+k); the tempo between two beats is
// 60/(t[k+1]-t[k]). Before the first beat the first interval is
// extrapolated, after the last beat the last one.
//
// Bar lines come from a bar tracker over the detected downbeats (bars of
// 2..8 beats, a time-signature change costs more than a missed downbeat).
// The first bar line is placed on a bar boundary (P = numerator·ceil(n/
// numerator) where n is the number of beats between second 0 and it), so
// second 0 lands at a non-negative fractional beat and every clip starts
// at Beat(0).
//
// A grid the tracker is not confident about is refused (ErrUncertain)
// rather than written: too little of the song on a stable tempo, or a
// metrical level that disagrees with an independent tempo estimate.
package grid

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	// MinBPM and MaxBPM are Bitwig's transport range; automation values
	// are clamped to it.
	MinBPM = 20.0
	MaxBPM = 666.0
	// MinBeats is the smallest usable grid.
	MinBeats = 8
	// Denominator is fixed: one beat is a quarter note.
	Denominator = 4

	// minStable: below this share of beats on a stable tempo the grid is
	// refused; minStableOffLevel applies when the tempo also disagrees with
	// the reference tempo by a non-octave ratio (3:2 and the like).
	minStable         = 0.6
	minStableOffLevel = 0.85
	// levelTol is the relative tolerance for "same metrical level" (ratio
	// 1, 2 or 1/2 to the reference tempo).
	levelTol = 0.08
	// dropRatio: an interval shorter than dropRatio*median is a doubled beat.
	dropRatio = 0.5
	// halfRatio: two consecutive intervals both shorter than halfRatio*median
	// that add up to about one median interval are a double-time beat
	// between two real ones (busy passages make trackers halve the beat).
	halfRatio = 0.7
	// fillRatio: an interval longer than fillRatio*median has missed beats.
	fillRatio = 1.6
)

// ErrTooFewBeats is returned when the grid is unusable.
var ErrTooFewBeats = errors.New("grid: too few beats")

// ErrUncertain is returned (wrapped, with the reason) when the beat
// tracker is not confident enough for a tempo map.
var ErrUncertain = errors.New("grid: beat tracking not confident")

// Options are the inputs of Build besides the tracker output.
type Options struct {
	// RefBPM is an independent tempo estimate of the same audio (librosa,
	// jobs.detected_bpm); nil when there is none. It is only used to check
	// the metrical level.
	RefBPM *float64
	// Force builds the map even when the tracker is not confident
	// (diagnostics only; exports never set it).
	Force bool
}

// Bar is one bar of the map.
type Bar struct {
	// Beat is the position of the downbeat, in beats.
	Beat float64
	// Numerator is the number of beats in the bar.
	Numerator int
}

// TimeSignature is a time-signature change at a beat position.
type TimeSignature struct {
	Beat        float64 `json:"beat"`
	Numerator   int     `json:"numerator"`
	Denominator int     `json:"denominator"`
}

// TempoPoint is one automation point (Bitwig stepped form: two points
// share a time at every tempo change).
type TempoPoint struct {
	Beat float64
	BPM  float64
}

// Warp maps a beat position onto a second of audio.
type Warp struct {
	Beat    float64
	Seconds float64
}

// Map is a built grid.
type Map struct {
	beats  []float64 // fitted, ascending
	offset float64   // beat position of beats[0]
	median float64   // median interval, seconds
	segs   []segment // constant-tempo stretches of beats

	// Constant is true when the tempo is written as a single value.
	Constant bool
	// Stable is the share of beats on a tempo held for at least stableLen
	// beats.
	Stable float64
	// Bars from the first downbeat on; nil when no downbeat was given.
	Bars []Bar
	// Dropped and Filled count cleaning edits; Bridged counts beats laid
	// evenly over tracker noise between two stretches of the same tempo
	// (the detections there are counted in Dropped).
	Dropped, Filled, Bridged int
	// Clamped counts tempo values that hit MinBPM/MaxBPM.
	Clamped int

	numerator0 int
	sigs       []TimeSignature
}

// Build is BuildWith without options.
func Build(beats, downbeats []float64, duration float64) (*Map, error) {
	return BuildWith(beats, downbeats, duration, Options{})
}

// BuildWith cleans beats (seconds, ascending), fits the tempo, matches
// downbeats (seconds) to the beats and derives bars and the lead-in.
// duration is the audio length.
func BuildWith(beats, downbeats []float64, duration float64, opt Options) (*Map, error) {
	if len(beats) < MinBeats {
		return nil, fmt.Errorf("%w: %d", ErrTooFewBeats, len(beats))
	}
	for i := 1; i < len(beats); i++ {
		if beats[i] <= beats[i-1] || math.IsNaN(beats[i]) {
			return nil, fmt.Errorf("grid: beats[%d]=%v not ascending", i, beats[i])
		}
	}
	if beats[0] < 0 {
		return nil, fmt.Errorf("grid: beats[0]=%v negative", beats[0])
	}
	m := &Map{}
	cleaned, dropped, filled := clean(beats)
	m.Dropped, m.Filled = dropped, filled
	if len(cleaned) < MinBeats {
		return nil, fmt.Errorf("%w after cleaning: %d", ErrTooFewBeats, len(cleaned))
	}
	if med := median(intervals(cleaned)); med < 60/MaxBPM || med > 60/MinBPM {
		return nil, fmt.Errorf("grid: median interval %.3fs outside the %.0f–%.0f BPM range", med, MinBPM, MaxBPM)
	}
	m.segs = fitSegments(cleaned)
	if out, n := trimEdges(cleaned, m.segs); n > 0 {
		cleaned, m.Dropped = out, m.Dropped+n
		m.segs = fitSegments(cleaned)
	}
	for range len(cleaned) { // each pass removes one stretch of noise
		out, removed, inserted, ok := bridge(cleaned, m.segs)
		if !ok {
			break
		}
		cleaned, m.Bridged = out, m.Bridged+inserted
		m.Dropped += removed
		m.segs = fitSegments(cleaned)
	}
	m.Stable = stableShare(m.segs, len(cleaned))
	m.beats = fitGrid(cleaned, m.segs)
	m.median = median(intervals(m.beats))
	if err := m.confident(opt.RefBPM); err != nil && !opt.Force {
		return nil, err
	}
	if len(m.segs) == 1 {
		m.Constant = true
		m.median = m.segs[0].period
	}

	// Downbeats → indices into the cleaned beats (nearest, within half an interval).
	var dbIdx []int
	for _, d := range downbeats {
		i := nearest(cleaned, d)
		if math.Abs(cleaned[i]-d) <= m.median/2 && (len(dbIdx) == 0 || i > dbIdx[len(dbIdx)-1]) {
			dbIdx = append(dbIdx, i)
		}
	}
	m.buildBars(dbIdx)
	_ = duration
	return m, nil
}

// confident refuses a grid with too little stable tempo, or with a tempo
// on a different metrical level than the reference (136 against 92 BPM is
// 3:2: one of the two counts the wrong pulse, and a wrong grid is worse
// than none).
func (m *Map) confident(ref *float64) error {
	bpm := 60 / m.median
	if m.Stable < minStable {
		return fmt.Errorf("%w: only %.0f%% of the beats on a stable tempo", ErrUncertain, m.Stable*100)
	}
	if ref == nil || *ref <= 0 {
		return nil
	}
	ratio := bpm / *ref
	for _, r := range []float64{1, 2, 0.5} {
		if math.Abs(ratio/r-1) <= levelTol {
			return nil
		}
	}
	if m.Stable < minStableOffLevel {
		return fmt.Errorf("%w: metrical level unclear, beat tracker %.1f BPM vs %.1f BPM (ratio %.2f), %.0f%% of the beats on a stable tempo",
			ErrUncertain, bpm, *ref, ratio, m.Stable*100)
	}
	return nil
}

// buildBars derives the bar lines, the lead-in offset and the signature
// changes.
func (m *Map) buildBars(db []int) {
	if len(db) < 2 {
		// No usable downbeats: 4/4 from the first beat, which is beat 0
		// of bar P.
		m.numerator0 = 4
		first := 0
		if len(db) == 1 {
			first = db[0]
		}
		m.offset = m.leadIn(first, 4)
		return
	}
	first, nums := trackBars(len(m.beats), db)
	m.numerator0 = nums[0]
	m.offset = m.leadIn(first, nums[0])
	prev, pos := 0, first
	for _, n := range nums {
		bar := Bar{Beat: m.offset + float64(pos), Numerator: n}
		m.Bars = append(m.Bars, bar)
		if n != prev {
			m.sigs = append(m.sigs, TimeSignature{Beat: bar.Beat, Numerator: n, Denominator: Denominator})
			prev = n
		}
		pos += n
	}
}

// leadIn returns the beat position of beats[0] such that the downbeat at
// index first sits on a bar boundary at or after second 0.
func (m *Map) leadIn(first, numerator int) float64 {
	d0 := m.interval(0)
	pre := float64(first) + m.beats[0]/d0 // beats from second 0 to the first downbeat
	// The fitted first beat is a least-squares value: a beat at second 0
	// comes out as 1e-16, which must not push the first bar a bar out.
	p := float64(numerator) * math.Ceil(pre/float64(numerator)-1e-9)
	return p - float64(first)
}

// interval returns t[k+1]-t[k], clamped to the tempo range.
func (m *Map) interval(k int) float64 {
	if k < 0 {
		k = 0
	}
	if k >= len(m.beats)-1 {
		k = len(m.beats) - 2
	}
	return m.beats[k+1] - m.beats[k]
}

// Beat maps a second to a beat position.
func (m *Map) Beat(sec float64) float64 {
	if m.Constant {
		return m.offset + (sec-m.beats[0])/m.median
	}
	b := m.beats
	n := len(b)
	switch {
	case sec < b[0]:
		return m.offset - (b[0]-sec)/m.interval(0)
	case sec >= b[n-1]:
		return m.offset + float64(n-1) + (sec-b[n-1])/m.interval(n-2)
	}
	k := sort.SearchFloat64s(b, sec)
	if k == n || b[k] > sec {
		k--
	}
	return m.offset + float64(k) + (sec-b[k])/m.interval(k)
}

// Seconds is the inverse of Beat.
func (m *Map) Seconds(beat float64) float64 {
	if m.Constant {
		return m.beats[0] + (beat-m.offset)*m.median
	}
	b := m.beats
	n := len(b)
	k := beat - m.offset
	switch {
	case k < 0:
		return b[0] + k*m.interval(0)
	case k >= float64(n-1):
		return b[n-1] + (k-float64(n-1))*m.interval(n-2)
	}
	i := int(math.Floor(k))
	return b[i] + (k-float64(i))*m.interval(i)
}

// BPM is the transport tempo: the constant tempo, or the tempo of the
// median interval when the map is variable.
func (m *Map) BPM() float64 { return clampBPM(60 / m.median) }

// Numerator is the initial time-signature numerator.
func (m *Map) Numerator() int { return m.numerator0 }

// TimeSignatures lists every signature from the first bar on; the first
// entry is the initial signature. Nil bars → a single 4/4 at beat 0.
func (m *Map) TimeSignatures() []TimeSignature {
	if len(m.sigs) == 0 {
		return []TimeSignature{{Beat: 0, Numerator: m.numerator0, Denominator: Denominator}}
	}
	return m.sigs
}

// Segments is the number of constant-tempo stretches of the lane.
func (m *Map) Segments() int { return len(m.segs) }

// TempoPoints returns the stepped tempo lane: (0, T0), then at every tempo
// change at beat k the pair (k, T_before), (k, T_after). Nil when the
// tempo is constant.
func (m *Map) TempoPoints() []TempoPoint {
	if m.Constant {
		return nil
	}
	tempo := func(s segment) float64 {
		v := 60 / s.period
		c := clampBPM(v)
		if c != v {
			m.Clamped++
		}
		return c
	}
	pts := make([]TempoPoint, 0, 2*len(m.segs))
	pts = append(pts, TempoPoint{Beat: 0, BPM: tempo(m.segs[0])})
	for i := 1; i < len(m.segs); i++ {
		pos := m.offset + float64(m.segs[i].start)
		pts = append(pts, TempoPoint{Beat: pos, BPM: tempo(m.segs[i-1])}, TempoPoint{Beat: pos, BPM: tempo(m.segs[i])})
	}
	return pts
}

// Warps returns the warp markers for an audio file of duration seconds
// that starts at second 0: one per tempo change inside the file plus both
// ends (the map is linear between tempo changes). Beat positions are
// absolute; subtract Beat(0) for clip-local times.
func (m *Map) Warps(duration float64) []Warp {
	out := []Warp{{Beat: m.Beat(0), Seconds: 0}}
	if !m.Constant {
		for _, s := range m.segs[1:] {
			t := m.beats[s.start]
			if t <= 0 || t >= duration {
				continue
			}
			out = append(out, Warp{Beat: m.offset + float64(s.start), Seconds: t})
		}
	}
	return append(out, Warp{Beat: m.Beat(duration), Seconds: duration})
}

// Beats returns the fitted beat times.
func (m *Map) Beats() []float64 { return append([]float64(nil), m.beats...) }

// Export is the JSON form of a Map for clients that draw a ruler or snap
// to the grid: everything Beat and Seconds need (the cleaned beats, the
// beat position of beats[0], the median interval, the constant flag) plus
// the transport tempo and the time signatures. A client that ports Beat /
// Seconds over these fields lands on the same beats the MIDI export uses.
type Export struct {
	Constant       bool            `json:"constant"`
	Offset         float64         `json:"offset"`
	Median         float64         `json:"median"`
	Beats          []float64       `json:"beats"`
	BPM            float64         `json:"bpm"`
	Numerator      int             `json:"numerator"`
	TimeSignatures []TimeSignature `json:"time_signatures"`
}

// Export returns the JSON form of the map.
func (m *Map) Export() Export {
	return Export{
		Constant: m.Constant, Offset: m.offset, Median: m.median, Beats: m.Beats(),
		BPM: m.BPM(), Numerator: m.Numerator(), TimeSignatures: m.TimeSignatures(),
	}
}

// Range returns the lowest and highest tempo between beats.
func (m *Map) Range() (lo, hi float64) {
	if m.Constant {
		v := m.BPM()
		return v, v
	}
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, s := range m.segs {
		v := clampBPM(60 / s.period)
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi
}

// clean drops doubled and double-time beats and fills gaps by linear
// interpolation. The median is taken over the raw intervals, so a tracker
// that halves the beat for less than half of the song still yields the
// song's real beat period.
func clean(in []float64) (out []float64, dropped, filled int) {
	med := median(intervals(in))
	out = append(out, in[0])
	for i := 1; i < len(in); i++ {
		d := in[i] - out[len(out)-1]
		if i+1 < len(in) {
			// Double-time: this beat splits one real interval in two.
			d2 := in[i+1] - in[i]
			if d < halfRatio*med && d2 < halfRatio*med && d+d2 > 0.75*med && d+d2 < 1.25*med {
				dropped++
				continue
			}
		}
		switch {
		case d < dropRatio*med:
			dropped++
			continue
		case d > fillRatio*med:
			n := int(math.Round(d / med))
			prev := out[len(out)-1]
			for j := 1; j < n; j++ {
				out = append(out, prev+d*float64(j)/float64(n))
				filled++
			}
		}
		out = append(out, in[i])
	}
	return out, dropped, filled
}

func intervals(b []float64) []float64 {
	out := make([]float64, len(b)-1)
	for i := range out {
		out[i] = b[i+1] - b[i]
	}
	return out
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func nearest(b []float64, t float64) int {
	i := sort.SearchFloat64s(b, t)
	if i == len(b) {
		return i - 1
	}
	if i > 0 && t-b[i-1] < b[i]-t {
		return i - 1
	}
	return i
}

func clampBPM(v float64) float64 { return math.Max(MinBPM, math.Min(MaxBPM, v)) }
