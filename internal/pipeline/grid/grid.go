// Package grid turns detected beats and downbeats into the one mapping the
// pipeline uses between seconds and DAW beats: clip warps, note times, the
// tempo automation and the .mid tempo track are all derived from a Map, so
// audio and MIDI can not drift apart.
//
// Beat positions are uniform in beat units (beat k sits at Offset+k); the
// tempo between two beats is 60/(t[k+1]-t[k]). Before the first beat the
// first interval is extrapolated, after the last beat the last one. The
// first downbeat is placed on a bar boundary (P = numerator·ceil(n/numerator)
// where n is the number of beats between second 0 and that downbeat), so
// second 0 lands at a non-negative fractional beat and every clip starts
// at Beat(0).
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
	// MaxNumerator caps beats per bar.
	MaxNumerator = 32

	// constantSpread is the (p95-p5)/median beat-interval spread below
	// which a single tempo is written instead of per-beat automation.
	constantSpread = 0.02
	// dropRatio: an interval shorter than dropRatio*median is a doubled beat.
	dropRatio = 0.5
	// fillRatio: an interval longer than fillRatio*median has missed beats.
	fillRatio = 1.6
)

// ErrTooFewBeats is returned when the grid is unusable.
var ErrTooFewBeats = errors.New("grid: too few beats")

// Bar is one bar of the map.
type Bar struct {
	// Beat is the position of the downbeat, in beats.
	Beat float64
	// Count is the number of detected beats in the bar.
	Count int
	// Numerator is the effective time-signature numerator (Count unless
	// the bar was merged as a missed-downbeat artefact).
	Numerator int
}

// TimeSignature is a time-signature change at a beat position.
type TimeSignature struct {
	Beat        float64
	Numerator   int
	Denominator int
}

// TempoPoint is one automation point (Bitwig stepped form: two points
// share a time at every beat).
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
	beats  []float64 // cleaned, ascending
	offset float64   // beat position of beats[0]
	median float64   // median interval, seconds

	// Constant is true when the tempo is written as a single value.
	Constant bool
	// Bars from the first downbeat on; nil when no downbeat was given.
	Bars []Bar
	// Dropped and Filled count cleaning edits.
	Dropped, Filled int
	// Clamped counts tempo values that hit MinBPM/MaxBPM.
	Clamped int

	numerator0 int
	sigs       []TimeSignature
}

// Build cleans beats (seconds, ascending), matches downbeats (seconds) to
// them and derives bars and the lead-in. duration is the audio length.
func Build(beats, downbeats []float64, duration float64) (*Map, error) {
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
	m.beats, m.Dropped, m.Filled = clean(beats)
	if len(m.beats) < MinBeats {
		return nil, fmt.Errorf("%w after cleaning: %d", ErrTooFewBeats, len(m.beats))
	}
	m.median = median(intervals(m.beats))
	if m.median < 60/MaxBPM || m.median > 60/MinBPM {
		return nil, fmt.Errorf("grid: median interval %.3fs outside the %.0f–%.0f BPM range", m.median, MinBPM, MaxBPM)
	}
	m.Constant = spread(intervals(m.beats), m.median) < constantSpread

	// Downbeats → indices into the cleaned beats (nearest, within half an interval).
	var dbIdx []int
	for _, d := range downbeats {
		i := nearest(m.beats, d)
		if math.Abs(m.beats[i]-d) <= m.median/2 && (len(dbIdx) == 0 || i > dbIdx[len(dbIdx)-1]) {
			dbIdx = append(dbIdx, i)
		}
	}
	m.buildBars(dbIdx)
	_ = duration
	return m, nil
}

// buildBars derives numerators, the lead-in offset and the signature changes.
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
	counts := make([]int, len(db)-1)
	for i := range counts {
		counts[i] = clampInt(db[i+1]-db[i], 1, MaxNumerator)
	}
	nums := effectiveNumerators(counts)
	m.numerator0 = nums[0]
	m.offset = m.leadIn(db[0], nums[0])
	prev := 0
	for i, c := range counts {
		bar := Bar{Beat: m.offset + float64(db[i]), Count: c, Numerator: nums[i]}
		m.Bars = append(m.Bars, bar)
		if nums[i] != prev {
			m.sigs = append(m.sigs, TimeSignature{Beat: bar.Beat, Numerator: nums[i], Denominator: Denominator})
			prev = nums[i]
		}
	}
}

// effectiveNumerators applies the missed-downbeat rule: an isolated bar
// whose count is a multiple of its neighbours' numerator is m bars of that
// numerator, not a signature change. Every other change is kept, so bar
// lines always follow the detected downbeats.
func effectiveNumerators(counts []int) []int {
	out := make([]int, len(counts))
	copy(out, counts)
	if len(counts) < 2 {
		return out
	}
	for i := range counts {
		var n int
		switch {
		case i == 0:
			n = counts[1] // first bar: the following bar is the reference
		case i == len(counts)-1:
			n = out[i-1] // last bar: the preceding one
		default:
			n = out[i-1]
			if counts[i+1] != n {
				continue
			}
		}
		if counts[i] != n && counts[i]%n == 0 {
			out[i] = n
		}
	}
	return out
}

// leadIn returns the beat position of beats[0] such that the downbeat at
// index first sits on a bar boundary at or after second 0.
func (m *Map) leadIn(first, numerator int) float64 {
	d0 := m.interval(0)
	pre := float64(first) + m.beats[0]/d0 // beats from second 0 to the first downbeat
	p := float64(numerator) * math.Ceil(pre/float64(numerator))
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

// TempoPoints returns the stepped tempo lane: (0, T0), then at every beat
// k ≥ 1 the pair (k, T_{k-1}), (k, T_k). Nil when the tempo is constant.
func (m *Map) TempoPoints() []TempoPoint {
	if m.Constant {
		return nil
	}
	n := len(m.beats)
	tempo := func(k int) float64 {
		v := 60 / m.interval(k)
		c := clampBPM(v)
		if c != v {
			m.Clamped++
		}
		return c
	}
	pts := make([]TempoPoint, 0, 2*n)
	pts = append(pts, TempoPoint{Beat: 0, BPM: tempo(0)})
	for k := 1; k < n-1; k++ {
		pos := m.offset + float64(k)
		pts = append(pts, TempoPoint{Beat: pos, BPM: tempo(k - 1)}, TempoPoint{Beat: pos, BPM: tempo(k)})
	}
	return pts
}

// Warps returns the warp markers for an audio file of duration seconds
// that starts at second 0: one per beat inside the file plus both ends.
// Beat positions are absolute; subtract Beat(0) for clip-local times.
func (m *Map) Warps(duration float64) []Warp {
	out := []Warp{{Beat: m.Beat(0), Seconds: 0}}
	if !m.Constant {
		for k, t := range m.beats {
			if t <= 0 || t >= duration {
				continue
			}
			out = append(out, Warp{Beat: m.offset + float64(k), Seconds: t})
		}
	}
	return append(out, Warp{Beat: m.Beat(duration), Seconds: duration})
}

// Beats returns the cleaned beat times.
func (m *Map) Beats() []float64 { return append([]float64(nil), m.beats...) }

// Range returns the lowest and highest tempo between beats.
func (m *Map) Range() (lo, hi float64) {
	if m.Constant {
		v := m.BPM()
		return v, v
	}
	lo, hi = math.Inf(1), math.Inf(-1)
	for k := 0; k < len(m.beats)-1; k++ {
		v := clampBPM(60 / m.interval(k))
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return lo, hi
}

// clean drops doubled beats and fills gaps by linear interpolation.
func clean(in []float64) (out []float64, dropped, filled int) {
	med := median(intervals(in))
	out = append(out, in[0])
	for i := 1; i < len(in); i++ {
		d := in[i] - out[len(out)-1]
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

// spread is (p95 - p5) / median of the intervals.
func spread(v []float64, med float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	p := func(q float64) float64 { return s[int(math.Min(float64(len(s)-1), math.Floor(q*float64(len(s)))))] }
	return (p(0.95) - p(0.05)) / med
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

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
