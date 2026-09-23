package grid

import (
	"math"
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
	pts := m.TempoPoints()
	if len(pts) != 1+2*(len(beats)-2) || !near(pts[0].BPM, 120) {
		t.Fatalf("points %+v", pts)
	}
	// At beat 8 the pair is (150? no: T_7 = 60/(t8-t7)=120, then T_8=150).
	var found bool
	for i := 1; i+1 < len(pts); i += 2 {
		if near(pts[i].Beat, 8) {
			found = near(pts[i].BPM, 120) && near(pts[i+1].BPM, 150) && near(pts[i+1].Beat, 8)
		}
	}
	if !found {
		t.Fatalf("no 120→150 step at beat 8: %+v", pts)
	}
	lo, hi := m.Range()
	if !near(lo, 120) || !near(hi, 150) {
		t.Fatalf("range %v %v", lo, hi)
	}
	// Start, 15 interior beats (beat 0 is the start itself), end.
	w := m.Warps(7.5)
	if len(w) != 17 || !near(w[len(w)-1].Seconds, 7.5) || !near(w[len(w)-1].Beat, 16.75) {
		t.Fatalf("warps %d %+v", len(w), w[len(w)-1])
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

func TestTimeSignatures(t *testing.T) {
	// Bars of 4,4,3,3,4,4,8,4,4,5,4,4: 3/4 run kept, 8 merged (missed
	// downbeat), isolated 5 kept.
	counts := []int{4, 4, 3, 3, 4, 4, 8, 4, 4, 5, 4, 4}
	var beats, downs []float64
	tm := 0.0
	for _, c := range counts {
		downs = append(downs, tm)
		for i := 0; i < c; i++ {
			beats = append(beats, tm)
			tm += 0.5
		}
	}
	beats = append(beats, tm) // closing downbeat
	downs = append(downs, tm)
	m, err := Build(beats, downs, tm)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{4, 4, 3, 3, 4, 4, 4, 4, 4, 5, 4, 4}
	for i, b := range m.Bars {
		if b.Numerator != want[i] || b.Count != counts[i] {
			t.Fatalf("bar %d: %+v want numerator %d", i, b, want[i])
		}
	}
	sigs := m.TimeSignatures()
	got := []int{}
	for _, s := range sigs {
		got = append(got, s.Numerator)
	}
	if len(got) != 5 || got[0] != 4 || got[1] != 3 || got[2] != 4 || got[3] != 5 || got[4] != 4 {
		t.Fatalf("signatures %v (%+v)", got, sigs)
	}
	// The 3/4 change sits on its downbeat (beat 8).
	if !near(sigs[1].Beat, 8) {
		t.Fatalf("3/4 at %v", sigs[1].Beat)
	}
	// Edges: a leading or trailing 8 next to 4s is a missed downbeat too.
	if got := effectiveNumerators([]int{8, 4, 4, 8}); got[0] != 4 || got[3] != 4 {
		t.Fatalf("edge merge %v", got)
	}
	if got := effectiveNumerators([]int{5, 4, 4, 3}); got[0] != 5 || got[3] != 3 {
		t.Fatalf("edge non-multiple kept %v", got)
	}
}
