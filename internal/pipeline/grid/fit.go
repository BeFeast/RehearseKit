package grid

import "math"

const (
	// fitTol is how far a detected beat may sit from the fitted grid
	// before the tempo has to change: beat trackers place beats on 20 ms
	// frames and players push and drag, so per-beat intervals jump by
	// several per cent on a steady song while the beats stay within a few
	// tens of milliseconds of one straight line.
	fitTol = 0.03
	// outlierTol: an isolated beat up to this far off the line (neighbours
	// on it) is a misplaced detection, not a tempo change.
	outlierTol = 0.075
	// fitLookahead is how many further beats a segment tries to reach past
	// a beat that does not fit before it is closed.
	fitLookahead = 4
	// stableLen is the shortest segment (in beats) counted as a stable
	// tempo for the confidence check.
	stableLen = 8
	// looseTol: where the tracker wobbles (short segments), one tempo that
	// keeps those beats within looseTol is a better grid than a staircase
	// of one- and two-beat tempos.
	looseTol = 0.06
	// maxBridge is the longest stretch (in beats) of tracker noise between
	// two stable segments that bridge replaces with even beats;
	// bridgeTempo is the relative tempo difference the two may have,
	// bridgeStretch how far the even beats may be from that tempo, and
	// bridgeSupport the share of them that needs a detection within
	// looseTol when the stretch is longer than stableLen beats (a stretch
	// at another tempo between two equal ones must not be bridged; a
	// fill of a bar or two is bridged regardless, the tracker loses the
	// beat there).
	maxBridge     = 32
	bridgeTempo   = 0.03
	bridgeStretch = 0.08
	bridgeSupport = 0.6
)

// segment is a stretch of constant tempo: beats start..end (inclusive)
// lie on one line, beat k at t0 + (k-start)·period.
type segment struct {
	start, end int
	period     float64
}

// fitSegments splits beat times into the fewest stretches of constant
// tempo that keep every beat within fitTol of its line (isolated beats
// within outlierTol are tolerated). Neighbouring segments share their
// boundary beat. Greedy from the left is optimal here: a sub-range of a
// range that fits a line fits it too.
func fitSegments(b []float64) []segment {
	var segs []segment
	s := 0
	for s < len(b)-1 {
		end := s + 1 // two beats always fit
		for j, misses := s+2, 0; j < len(b) && misses < fitLookahead; j++ {
			if fits(b[s : j+1]) {
				end, misses = j, 0
			} else {
				misses++
			}
		}
		segs = append(segs, segment{start: s, end: end})
		s = end
	}
	return mergeShort(b, segs)
}

// mergeShort joins a short segment with a neighbour when one line keeps
// the short segment's beats within looseTol and the long one's within
// fitTol, until nothing merges.
func mergeShort(b []float64, segs []segment) []segment {
	short := func(s segment) bool { return s.end-s.start+1 < stableLen }
	for merged := true; merged; {
		merged = false
		for i := 0; i+1 < len(segs); i++ {
			a, c := segs[i], segs[i+1]
			if !short(a) && !short(c) {
				continue
			}
			tol := func(k int) float64 { // k relative to a.start
				owner := a
				if a.start+k > c.start {
					owner = c
				}
				if short(owner) {
					return looseTol
				}
				return fitTol
			}
			if fitsTol(b[a.start:c.end+1], tol) {
				segs[i] = segment{start: a.start, end: c.end}
				segs = append(segs[:i+1], segs[i+2:]...)
				merged = true
			}
		}
	}
	return segs
}

// bridge finds two long segments of the same tempo with only short
// segments between them (a fill the tracker split into subdivisions, a
// break it mis-counted) and, when a whole number of beats of that tempo
// fills the gap between them, replaces the detections in between with
// evenly spaced beats. It returns the new beats, how many detections were
// removed and beats inserted, and false when there was nothing to bridge.
// A half-beat phase slip is not bridged: no whole number of beats fits it.
func bridge(b []float64, segs []segment) (out []float64, removed, inserted int, ok bool) {
	long := func(s segment) bool { return s.end-s.start+1 >= stableLen }
	for i := 0; i < len(segs); i++ {
		if !long(segs[i]) {
			continue
		}
		j := i + 1
		for j < len(segs) && !long(segs[j]) {
			j++
		}
		if j == len(segs) {
			break
		}
		a, c := segs[i], segs[j]
		if j == i+1 || c.start-a.end > maxBridge {
			continue
		}
		pa, _ := line(b[a.start : a.end+1])
		pc, _ := line(b[c.start : c.end+1])
		p := (pa + pc) / 2
		gap := b[c.start] - b[a.end]
		n := int(math.Round(gap / p))
		if math.Abs(pa/pc-1) > bridgeTempo || n < 1 || math.Abs(gap/float64(n)/p-1) > bridgeStretch {
			continue
		}
		even := make([]float64, 0, n-1)
		supported := 0
		for k := 1; k < n; k++ {
			t := b[a.end] + gap*float64(k)/float64(n)
			even = append(even, t)
			if d := b[a.end : c.start+1]; math.Abs(d[nearest(d, t)]-t) <= looseTol {
				supported++
			}
		}
		if n-1 > stableLen && float64(supported) < bridgeSupport*float64(n-1) {
			continue
		}
		out = append(append(append(out, b[:a.end+1]...), even...), b[c.start:]...)
		return out, c.start - a.end - 1, n - 1, true
	}
	return b, 0, 0, false
}

// trimEdges drops the beats of a short segment at either end of the song
// that do not continue the neighbouring stable tempo within looseTol:
// detections in a silent intro or a decaying tail. A pickup in time stays.
// It returns the remaining beats and how many were dropped.
func trimEdges(b []float64, segs []segment) ([]float64, int) {
	long := func(s segment) bool { return s.end-s.start+1 >= stableLen }
	off := func(ref segment, ks []int) bool {
		slope, icpt := line(b[ref.start : ref.end+1])
		for _, k := range ks {
			if math.Abs(b[k]-(icpt+slope*float64(k-ref.start))) > looseTol {
				return true
			}
		}
		return false
	}
	lo, hi := 0, len(b)
	if len(segs) > 1 && !long(segs[0]) && long(segs[1]) {
		var ks []int
		for k := segs[0].start; k < segs[0].end; k++ {
			ks = append(ks, k)
		}
		if off(segs[1], ks) {
			lo = segs[0].end
		}
	}
	if n := len(segs); n > 1 && !long(segs[n-1]) && long(segs[n-2]) {
		var ks []int
		for k := segs[n-1].start + 1; k <= segs[n-1].end; k++ {
			ks = append(ks, k)
		}
		if off(segs[n-2], ks) {
			hi = segs[n-1].start + 1
		}
	}
	if hi-lo < MinBeats {
		return b, 0
	}
	return b[lo:hi], lo + len(b) - hi
}

// fits reports whether the least-squares line through b keeps every beat
// within fitTol, allowing isolated beats within outlierTol; the first and
// last beat must fit so a segment never ends on an outlier.
func fits(b []float64) bool {
	return fitsTol(b, func(int) float64 { return fitTol })
}

// fitsTol is fits with a per-beat tolerance.
func fitsTol(b []float64, tol func(k int) float64) bool {
	slope, icpt := line(b)
	prevOut := false
	for k, t := range b {
		r := math.Abs(t - (icpt + slope*float64(k)))
		out := r > tol(k)
		if out && (r > outlierTol || prevOut || k == len(b)-1 || k == 0) {
			return false
		}
		prevOut = out
	}
	return true
}

// line is the least-squares fit t = icpt + slope·k over k = 0..len(b)-1.
func line(b []float64) (slope, icpt float64) {
	n := float64(len(b))
	var sk, st, skk, skt float64
	for k, t := range b {
		x := float64(k)
		sk += x
		st += t
		skk += x * x
		skt += x * t
	}
	slope = (n*skt - sk*st) / (n*skk - sk*sk)
	return slope, (st - slope*sk) / n
}

// fitGrid fits one continuous piecewise-linear map beat index → seconds
// with a knot at every segment boundary (least squares over all beats),
// returns the fitted beat times and fills in each segment's period.
// Continuity matters: the grid is one tempo lane, and a jump at a knot
// would be a one-beat tempo spike.
func fitGrid(b []float64, segs []segment) []float64 {
	// Unknowns: x[0] = time of beat 0, x[1+i] = period of segment i.
	// Beat k in segment i: x0 + Σ_{j<i} len_j·x[1+j] + (k-start_i)·x[1+i].
	m := len(segs) + 1
	ata := make([][]float64, m)
	for i := range ata {
		ata[i] = make([]float64, m)
	}
	atb := make([]float64, m)
	row := make([]float64, m)
	add := func(t float64) {
		for i, ri := range row {
			if ri == 0 {
				continue
			}
			atb[i] += ri * t
			for j, rj := range row {
				ata[i][j] += ri * rj
			}
		}
	}
	for i, sg := range segs {
		first := sg.start
		if i > 0 {
			first++ // the shared boundary beat belongs to the previous segment
		}
		for k := first; k <= sg.end; k++ {
			for j := range row {
				row[j] = 0
			}
			row[0] = 1
			for j := 0; j < i; j++ {
				row[1+j] = float64(segs[j].end - segs[j].start)
			}
			row[1+i] = float64(k - sg.start)
			add(b[k])
		}
	}
	x := solve(ata, atb)
	out := make([]float64, len(b))
	t := x[0]
	for i := range segs {
		segs[i].period = x[1+i]
		for k := segs[i].start; k <= segs[i].end; k++ {
			out[k] = t + float64(k-segs[i].start)*x[1+i]
		}
		t += float64(segs[i].end-segs[i].start) * x[1+i]
	}
	return out
}

// solve solves the symmetric positive-definite system a·x = y in place
// (Gaussian elimination with partial pivoting; a few hundred unknowns).
func solve(a [][]float64, y []float64) []float64 {
	n := len(y)
	for c := 0; c < n; c++ {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		a[c], a[p] = a[p], a[c]
		y[c], y[p] = y[p], y[c]
		for r := c + 1; r < n; r++ {
			f := a[r][c] / a[c][c]
			if f == 0 {
				continue
			}
			for k := c; k < n; k++ {
				a[r][k] -= f * a[c][k]
			}
			y[r] -= f * y[c]
		}
	}
	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		s := y[r]
		for k := r + 1; k < n; k++ {
			s -= a[r][k] * x[k]
		}
		x[r] = s / a[r][r]
	}
	return x
}

// stableShare is the share of beat intervals inside segments of at least
// stableLen beats.
func stableShare(segs []segment, n int) float64 {
	if n < 2 {
		return 0
	}
	in := 0
	for _, s := range segs {
		if s.end-s.start+1 >= stableLen {
			in += s.end - s.start
		}
	}
	return float64(in) / float64(n-1)
}
