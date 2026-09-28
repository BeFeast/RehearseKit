package grid

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
)

var report = flag.Bool("report", false, "print grid quality metrics for every testdata/grid-*.json")

// quality is what a DAW shows for a Map, measured the way the head-to-head
// dawgrid.py measures a project.xml: integer beats inside the audio mapped
// to seconds, bar lines drawn from beat 0 by the transport signature and
// the signature changes.
type quality struct {
	TempoPoints int
	MedianBPM   float64
	P5, P95     float64 // per-beat BPM
	Jitter      float64 // median |ΔBPM| between adjacent beats / median, %
	Spike       float64 // share of beats more than ±10 % off the median, %
	Bars        int
	ShortBars   int // bars shorter than 2 beats
	ShortRun    int // longest run of consecutive short bars
	SigChanges  int
	BeatF       float64 // vs the reference, ±70 ms; NaN without one
	DownF       float64
}

func measure(m *Map, duration float64, refBeats, refDowns []float64) quality {
	q := quality{TempoPoints: len(m.TempoPoints()), BeatF: math.NaN(), DownF: math.NaN()}
	k0, k1 := math.Ceil(m.Beat(0)), math.Floor(m.Beat(duration))
	var at []float64
	for k := k0; k <= k1; k++ {
		at = append(at, m.Seconds(k))
	}
	var bpm []float64
	for i := 1; i < len(at); i++ {
		bpm = append(bpm, 60/(at[i]-at[i-1]))
	}
	if len(bpm) > 1 {
		q.MedianBPM = median(bpm)
		s := append([]float64(nil), bpm...)
		sort.Float64s(s)
		q.P5, q.P95 = pct(s, 0.05), pct(s, 0.95)
		var d []float64
		spikes := 0
		for i, v := range bpm {
			if i > 0 {
				d = append(d, math.Abs(v-bpm[i-1]))
			}
			if math.Abs(v/q.MedianBPM-1) > 0.1 {
				spikes++
			}
		}
		q.Jitter = median(d) / q.MedianBPM * 100
		q.Spike = float64(spikes) / float64(len(bpm)) * 100
	}
	// Bar lines as the DAW draws them.
	sigs := m.TimeSignatures()
	q.SigChanges = len(sigs) - 1
	num, si, run := m.Numerator(), 0, 0
	var downs []float64
	for b := 0.0; b <= k1; b += float64(num) {
		for si < len(sigs) && sigs[si].Beat <= b+1e-6 {
			num = sigs[si].Numerator
			si++
		}
		q.Bars++
		if num < 2 {
			q.ShortBars++
			run++
			q.ShortRun = max(q.ShortRun, run)
		} else {
			run = 0
		}
		if b >= k0 {
			downs = append(downs, m.Seconds(b))
		}
	}
	if len(refBeats) > 0 {
		q.BeatF = fMeasure(refBeats, at, 0.07)
	}
	if len(refDowns) > 0 {
		q.DownF = fMeasure(refDowns, downs, 0.07)
	}
	return q
}

// fMeasure is mir_eval.beat.f_measure: one-to-one matches within ±window.
// Both lists are sorted; two pointers find the maximum matching on a line.
func fMeasure(ref, est []float64, window float64) float64 {
	if len(ref) == 0 || len(est) == 0 {
		return 0
	}
	hits, i, j := 0, 0, 0
	for i < len(ref) && j < len(est) {
		switch d := est[j] - ref[i]; {
		case math.Abs(d) <= window:
			hits++
			i++
			j++
		case d < 0:
			j++
		default:
			i++
		}
	}
	p, r := float64(hits)/float64(len(est)), float64(hits)/float64(len(ref))
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

func pct(sorted []float64, q float64) float64 {
	return sorted[int(math.Min(float64(len(sorted)-1), math.Floor(q*float64(len(sorted)))))]
}

func (q quality) String() string {
	f := func(v float64) string {
		if math.IsNaN(v) {
			return "–"
		}
		return fmt.Sprintf("%.3f", v)
	}
	return fmt.Sprintf("| %d | %.2f | %.1f–%.1f | %.2f | %.1f | %d | %d | %d | %d | %s | %s |",
		q.TempoPoints, q.MedianBPM, q.P5, q.P95, q.Jitter, q.Spike, q.Bars, q.ShortBars, q.ShortRun, q.SigChanges, f(q.BeatF), f(q.DownF))
}

// TestQualityReport prints the metrics table (go test -run QualityReport -report).
func TestQualityReport(t *testing.T) {
	if !*report {
		t.Skip("run with -report")
	}
	var b strings.Builder
	b.WriteString("| fixture | tempo points | median BPM | BPM p5–p95 | jitter % | spike % | bars | bars < 2 | longest run | sig changes | beat F | down F |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, in := range loadInputs(t) {
		m, err := BuildWith(in.Beats, in.Downbeats, in.Duration, Options{RefBPM: in.LibrosaBPM})
		if err != nil {
			fmt.Fprintf(&b, "| %s | fallback: %v |\n", in.Name, err)
			if m, err = buildInput(in); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "| %s (forced) %s\n", in.Name, measure(m, in.Duration, in.RefBeats, in.RefDownbeats))
			continue
		}
		fmt.Fprintf(&b, "| %s %s\n", in.Name, measure(m, in.Duration, in.RefBeats, in.RefDownbeats))
	}
	t.Log("\n" + b.String())
}

// TestAcceptance holds the #32 acceptance on the fixtures: no bars shorter
// than two beats anywhere, a steady song gets a (nearly) constant tempo,
// the synthetic track keeps its beats and gains downbeats, and the
// odd-meter song with an unclear metrical level is refused.
func TestAcceptance(t *testing.T) {
	for _, in := range loadInputs(t) {
		m, err := buildInput(in) // forced: bars are checked on refused grids too
		if err != nil {
			t.Fatal(err)
		}
		q := measure(m, in.Duration, in.RefBeats, in.RefDownbeats)
		if q.ShortBars != 0 {
			t.Errorf("%s: %d bars shorter than 2 beats", in.Name, q.ShortBars)
		}
		_, gated := BuildWith(in.Beats, in.Downbeats, in.Duration, Options{RefBPM: in.LibrosaBPM})
		switch in.Name {
		case "1928", "1928-subframe":
			// Frame-by-frame per-beat tempo was 1033 automation points with a
			// p5–p95 of 107–115 BPM. The song moves between 109 and 113 BPM
			// and has one fill the tracker loses.
			if gated != nil || q.TempoPoints > 40 || q.Spike > 1 || q.P95-q.P5 > 7 {
				t.Errorf("%s: err %v, %+v", in.Name, gated, q)
			}
		case "click", "click-frame":
			// Old grid: beat F 0.864, downbeat F 0.630, 405 tempo points.
			if gated != nil || q.BeatF < 0.855 || q.DownF < 0.73 || q.TempoPoints > 40 {
				t.Errorf("%s: err %v, %+v", in.Name, gated, q)
			}
			// The sections the tracker follows keep their tempo.
			for _, c := range []struct{ at, bpm float64 }{{10, 100}, {25, 128}, {90, 110}} {
				k := m.Beat(c.at)
				if got := 60 / (m.Seconds(k+0.5) - m.Seconds(k-0.5)); math.Abs(got/c.bpm-1) > 0.01 {
					t.Errorf("%s: %.1f BPM at %v s, want %v", in.Name, got, c.at, c.bpm)
				}
			}
		case "electric-sunrise", "clip45":
			if !errors.Is(gated, ErrUncertain) {
				t.Errorf("%s: want ErrUncertain, got %v", in.Name, gated)
			}
		default:
			t.Errorf("fixture %s has no acceptance case", in.Name)
		}
	}
}
