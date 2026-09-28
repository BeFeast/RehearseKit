package grid

import (
	"errors"
	"math"
	"sort"
	"testing"
)

// steady returns n beats at bpm starting at t0.
func steady(t0, bpm float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = t0 + float64(i)*60/bpm
	}
	return out
}

func every(beats []float64, n int) []float64 {
	var out []float64
	for i := 0; i < len(beats); i += n {
		out = append(out, beats[i])
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestConstantNoIntro(t *testing.T) {
	beats := steady(0, 120, 32)
	m, err := Build(beats, every(beats, 4), 16)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Constant || !near(m.BPM(), 120) || m.Numerator() != 4 {
		t.Fatalf("constant=%v bpm=%v num=%d", m.Constant, m.BPM(), m.Numerator())
	}
	if !near(m.Beat(0), 0) || !near(m.Beat(1), 2) || !near(m.Beat(16), 32) {
		t.Fatalf("beat map %v %v %v", m.Beat(0), m.Beat(1), m.Beat(16))
	}
	if pts := m.TempoPoints(); pts != nil {
		t.Fatalf("constant map should have no automation, got %d points", len(pts))
	}
	if w := m.Warps(16); len(w) != 2 || !near(w[1].Beat, 32) || !near(w[1].Seconds, 16) {
		t.Fatalf("warps %+v", w)
	}
	if ts := m.TimeSignatures(); len(ts) != 1 || ts[0].Numerator != 4 {
		t.Fatalf("signatures %+v", ts)
	}
}

func TestLeadInSevenSeconds(t *testing.T) {
	// First beat at 7 s, 100 BPM (0.6 s): second 0 is 11.67 beats before
	// the first beat, which is a downbeat → bar boundary at beat 12.
	beats := steady(7, 100, 40)
	m, err := Build(beats, every(beats, 4), 40)
	if err != nil {
		t.Fatal(err)
	}
	if !near(m.Beat(7), 12) {
		t.Fatalf("first downbeat at beat %v, want 12", m.Beat(7))
	}
	if b0 := m.Beat(0); b0 < 0 || !near(b0, 12-7/0.6) {
		t.Fatalf("Beat(0)=%v", b0)
	}
	if !near(m.Seconds(m.Beat(3.3)), 3.3) || !near(m.Seconds(m.Beat(30)), 30) {
		t.Fatal("Seconds is not the inverse of Beat")
	}
}

func TestLeadInTinyOffset(t *testing.T) {
	// A beat at 0.03 s must not push the first downbeat a whole bar out.
	beats := steady(0.03, 120, 32)
	m, err := Build(beats, every(beats, 4), 16)
	if err != nil {
		t.Fatal(err)
	}
	if !near(m.Beat(0.03), 4) || m.Beat(0) < 0 {
		t.Fatalf("first downbeat at %v, Beat(0)=%v", m.Beat(0.03), m.Beat(0))
	}
}

func TestVariableTempoSteps(t *testing.T) {
	// 8 beats at 120 then 8 at 150: the lane steps at the change and
	// every beat maps exactly.
	beats := append(steady(0, 120, 8), steady(8*0.5, 150, 8)...)
	m, err := Build(beats, every(beats, 4), 8)
	if err != nil {
		t.Fatal(err)
	}
	if m.Constant {
		t.Fatal("tempo change should not be constant")
	}
	for k, sec := range beats {
		if !near(m.Beat(sec), float64(k)) {
			t.Fatalf("beat %d at %v maps to %v", k, sec, m.Beat(sec))
		}
	}
	// One step at beat 8: (0, 120), (8, 120), (8, 150).
	pts := m.TempoPoints()
	if len(pts) != 3 || !near(pts[0].BPM, 120) || !near(pts[1].Beat, 8) || !near(pts[1].BPM, 120) ||
		!near(pts[2].Beat, 8) || !near(pts[2].BPM, 150) {
		t.Fatalf("points %+v", pts)
	}
	if m.Segments() != 2 {
		t.Fatalf("segments %d", m.Segments())
	}
	lo, hi := m.Range()
	if !near(lo, 120) || !near(hi, 150) {
		t.Fatalf("range %v %v", lo, hi)
	}
	// Start, the tempo change, end.
	w := m.Warps(7.5)
	if len(w) != 3 || !near(w[1].Beat, 8) || !near(w[1].Seconds, 4) || !near(w[2].Seconds, 7.5) || !near(w[2].Beat, 16.75) {
		t.Fatalf("warps %+v", w)
	}
	// Tail extrapolates the last interval (0.4 s).
	if !near(m.Beat(6.8+0.4), 16) {
		t.Fatalf("tail %v", m.Beat(7.2))
	}
}

func TestClampAndClean(t *testing.T) {
	// 20 steady beats with beat 5 missing and a doubled beat after beat 10.
	var beats []float64
	for k := 0; k < 20; k++ {
		if k == 5 {
			continue
		}
		beats = append(beats, float64(k)*0.5)
		if k == 10 {
			beats = append(beats, float64(k)*0.5+0.05)
		}
	}
	m, err := Build(beats, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if m.Dropped != 1 || m.Filled != 1 {
		t.Fatalf("dropped=%d filled=%d", m.Dropped, m.Filled)
	}
	if !m.Constant {
		t.Fatal("after cleaning the grid is steady")
	}
	// Tempo values are clamped to Bitwig's range.
	if clampBPM(1000) != MaxBPM || clampBPM(5) != MinBPM {
		t.Fatal("clamp")
	}
	if _, err := Build(steady(0, 1000, 20), nil, 2); err == nil {
		t.Fatal("median interval outside the tempo range must fail")
	}
	if _, err := Build(steady(0, 120, 3), nil, 2); err == nil {
		t.Fatal("too few beats must fail")
	}
}

// TestCleanDoubleTime uses the beat intervals Beat This! produced on a
// 45 s Plini clip (92 BPM with double-time stretches): the cleaned grid
// must sit at the song's beat, not alternate between 92 and 184 BPM.
func TestCleanDoubleTime(t *testing.T) {
	iv := []float64{0.46, 0.64, 0.66, 0.58, 0.64, 0.64, 0.32, 0.28, 0.58, 0.66, 0.66, 0.66, 0.86, 0.66, 0.66, 0.64, 0.62, 0.22, 0.68, 0.66, 0.64, 0.3, 0.58, 0.64, 0.34, 0.32, 0.64, 0.32, 0.34, 0.22, 0.36, 0.28, 0.36, 0.26, 0.9, 0.64, 0.68, 0.66, 0.66, 0.84}
	beats := []float64{1.0}
	for _, d := range iv {
		beats = append(beats, beats[len(beats)-1]+d)
	}
	// 40 beats of a busy clip are too few for a confident tempo map;
	// Force still shows what cleaning did.
	m, err := BuildWith(beats, nil, beats[len(beats)-1]+1, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := m.Range()
	if lo < 55 || hi > 140 {
		t.Fatalf("tempo range %.1f–%.1f after cleaning (dropped %d, filled %d): %v", lo, hi, m.Dropped, m.Filled, intervals(m.beats))
	}
	if m.Dropped < 8 {
		t.Fatalf("expected the double-time beats dropped, got %d", m.Dropped)
	}
	for _, d := range intervals(m.beats) {
		if d < 0.4 { // a half beat at 92 BPM is 0.33 s
			t.Fatalf("interval %.2f survived cleaning", d)
		}
	}
}

// barBeats lays out bars of the given beat counts at 120 BPM and returns
// the beats plus a downbeat on every bar line (and a closing one).
func barBeats(counts []int) (beats, downs []float64) {
	tm := 0.0
	for _, c := range counts {
		downs = append(downs, tm)
		for i := 0; i < c; i++ {
			beats = append(beats, tm)
			tm += 0.5
		}
	}
	return append(beats, tm), append(downs, tm)
}

func numerators(m *Map) []int {
	var out []int
	for _, b := range m.Bars {
		out = append(out, b.Numerator)
	}
	return out
}

func sigNumerators(m *Map) []int {
	var out []int
	for _, s := range m.TimeSignatures() {
		out = append(out, s.Numerator)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTimeSignatures(t *testing.T) {
	// A 3/4 run is a signature change; a missed downbeat (8 beats) is two
	// bars of 4; an odd bar followed by a long stretch in phase with it
	// stays.
	counts := []int{4, 4, 3, 3, 3, 4, 4, 8, 4, 4, 5, 4, 4, 4, 4, 4, 4}
	beats, downs := barBeats(counts)
	m, err := Build(beats, downs, beats[len(beats)-1])
	if err != nil {
		t.Fatal(err)
	}
	want := []int{4, 4, 3, 3, 3, 4, 4, 4, 4, 4, 4, 5, 4, 4, 4, 4, 4, 4, 4}
	if got := numerators(m); !equalInts(got, want) {
		t.Fatalf("bars %v, want %v", got, want)
	}
	if got := sigNumerators(m); !equalInts(got, []int{4, 3, 4, 5, 4}) {
		t.Fatalf("signatures %v", got)
	}
	// The 3/4 change sits on its downbeat (beat 8).
	if s := m.TimeSignatures()[1]; !near(s.Beat, 8) {
		t.Fatalf("3/4 at %v", s.Beat)
	}
}

func TestBarsNeverOneBeat(t *testing.T) {
	// 16 bars of 4/4 where the detector calls every beat a downbeat for
	// eight beats in the middle, plus one stray downbeat on a backbeat.
	beats, downs := barBeats([]int{4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4})
	var noisy []float64
	for i, d := range downs {
		noisy = append(noisy, d)
		if i == 3 {
			noisy = append(noisy, d+1) // stray, on beat 3
		}
	}
	for k := 28; k < 36; k++ {
		noisy = append(noisy, beats[k])
	}
	sort.Float64s(noisy)
	m, err := Build(beats, noisy, beats[len(beats)-1])
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range numerators(m) {
		if n != 4 {
			t.Fatalf("bars %v: the noise should not change the 4/4", numerators(m))
		}
	}
	if len(m.TimeSignatures()) != 1 {
		t.Fatalf("signatures %+v", m.TimeSignatures())
	}
}

func TestSingleOddBarWithoutSupport(t *testing.T) {
	// A lone bar of 5 detected in 4/4 whose following downbeats are back
	// in the old phase is detector noise: no 5/4.
	beats, downs := barBeats([]int{4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4})
	downs = append(downs[:6], append([]float64{downs[6] - 0.5}, downs[7:]...)...) // one downbeat a beat early
	m, err := Build(beats, downs, beats[len(beats)-1])
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range numerators(m) {
		if n != 4 {
			t.Fatalf("bars %v", numerators(m))
		}
	}
}

// quantize rounds beat times to Beat This!'s 50 fps frames.
func quantize(b []float64) []float64 {
	out := make([]float64, len(b))
	for i, v := range b {
		out[i] = math.Round(v*50) / 50
	}
	return out
}

func TestFrameQuantizedSteadyIsConstant(t *testing.T) {
	// 111 BPM on 20 ms frames: per-beat intervals of 0.54 and 0.56 s
	// (±1.9 %), but one tempo.
	beats := quantize(steady(0.3, 111, 400))
	m, err := Build(beats, every(beats, 4), 220)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Constant || math.Abs(m.BPM()-111) > 0.05 || m.TempoPoints() != nil {
		t.Fatalf("constant=%v bpm=%v segments=%d", m.Constant, m.BPM(), m.Segments())
	}
}

func TestFrameQuantizedStepIsTwoTempos(t *testing.T) {
	// 64 beats at 100 then 64 at 130, on frames, plus a push of 40 ms on
	// one beat: two tempos, the step on the right beat.
	beats := append(steady(1, 100, 64), steady(1+64*0.6, 130, 64)...)
	beats[30] += 0.04
	beats = quantize(beats)
	m, err := Build(beats, every(beats, 4), beats[len(beats)-1]+1)
	if err != nil {
		t.Fatal(err)
	}
	pts := m.TempoPoints()
	if m.Segments() != 2 || len(pts) != 3 || math.Abs(pts[0].BPM-100) > 0.3 || math.Abs(pts[2].BPM-130) > 0.3 {
		t.Fatalf("segments %d, points %+v", m.Segments(), pts)
	}
	if k := pts[1].Beat - m.Beat(beats[0]); math.Abs(k-64) > 1 {
		t.Fatalf("step at beat %v of the song, want 64", k)
	}
}

func TestConfidenceLevelCheck(t *testing.T) {
	beats := steady(0.5, 138, 200)
	ref := 92.0 // 3:2
	// A fully stable grid is kept even against a 3:2 reference ...
	if _, err := BuildWith(beats, every(beats, 4), 90, Options{RefBPM: &ref}); err != nil {
		t.Fatal(err)
	}
	// ... double and half time are the same level.
	for _, r := range []float64{69, 276, 140} {
		if _, err := BuildWith(beats, every(beats, 4), 90, Options{RefBPM: &r}); err != nil {
			t.Fatalf("ref %v: %v", r, err)
		}
	}
	// A wobbly one against 3:2 is refused: every 5th beat 60 ms late
	// breaks the song into short stretches.
	wob := append([]float64(nil), beats...)
	for i := 4; i < len(wob); i += 3 {
		wob[i] += 0.07
		if i+1 < len(wob) {
			wob[i+1] -= 0.07
		}
	}
	_, err := BuildWith(wob, every(wob, 4), 90, Options{RefBPM: &ref})
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("want ErrUncertain, got %v", err)
	}
}
