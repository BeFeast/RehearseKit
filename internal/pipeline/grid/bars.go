package grid

import "math"

const (
	// minBar and maxBar bound the beats per bar the bar tracker may choose.
	// A one-beat bar is never a musical bar: it is a downbeat detector
	// firing on consecutive beats.
	minBar = 2
	maxBar = 8

	// Bar-tracker costs. A bar line on a beat the detector did not call a
	// downbeat, and a detected downbeat that is not a bar line, each cost
	// one; a change of time signature costs barChange, and a signature that
	// lasts a single bar costs barSingle on top (a lone odd bar survives only
	// when the following downbeats stay in phase with it).
	barMiss   = 1.0
	barChange = 2.0
	barSingle = 2.0
)

// barPrior is the per-bar cost of a numerator: common meters are free,
// long bars need evidence (a run of missed downbeats must not turn into
// 8/4).
func barPrior(n int) float64 {
	switch n {
	case 4:
		return 0
	case 3:
		return 0.1
	case 8:
		return 2
	default:
		return 0.5
	}
}

// trackBars places bar lines on the n beats given the detected downbeat
// indices (ascending): the cheapest sequence of bars of minBar..maxBar
// beats under the costs above. It returns the first bar line and the
// numerator of every bar; the last bar may run past the last beat.
func trackBars(n int, down []int) (first int, nums []int) {
	isDown := make([]bool, n)
	for _, d := range down {
		isDown[d] = true
	}
	// prefix[i] = detected downbeats among beats 0..i-1.
	prefix := make([]int, n+1)
	for i := 0; i < n; i++ {
		prefix[i+1] = prefix[i]
		if isDown[i] {
			prefix[i+1]++
		}
	}
	inside := func(p, l int) int { // detected downbeats strictly inside the bar
		hi := min(p+l, n)
		if p+1 >= hi {
			return 0
		}
		return prefix[hi] - prefix[p+1]
	}
	barCost := func(p, l int) float64 {
		c := barPrior(l) + barMiss*float64(inside(p, l))
		if !isDown[p] {
			c += barMiss
		}
		return c
	}

	// State: a bar starts at p with l beats and is the r-th bar (r = 1, or
	// 2 for "two or more") of its signature.
	const nl = maxBar + 1
	type key struct{ p, l, r int }
	inf := math.Inf(1)
	cost := make([][nl][3]float64, n)
	from := make([][nl][3]key, n)
	for p := range cost {
		for l := range cost[p] {
			cost[p][l] = [3]float64{inf, inf, inf}
		}
	}
	// The first bar line may sit on any of the first maxBar beats; detected
	// downbeats before it are misses.
	for p := 0; p < min(n, maxBar); p++ {
		for l := minBar; l <= maxBar; l++ {
			cost[p][l][1] = barMiss*float64(prefix[p]) + barCost(p, l)
			from[p][l][1] = key{-1, 0, 0}
		}
	}
	best, end := inf, key{-1, 0, 0}
	for p := 0; p < n; p++ {
		for l := minBar; l <= maxBar; l++ {
			for r := 1; r <= 2; r++ {
				c := cost[p][l][r]
				if math.IsInf(c, 1) {
					continue
				}
				q := p + l
				if q >= n { // this bar holds the last beat
					if r == 1 && from[p][l][r].l != 0 {
						c += barSingle
					}
					if c < best {
						best, end = c, key{p, l, r}
					}
					continue
				}
				// q < n here: the next bar starts on a beat, so cost[q] exists.
				for l2 := minBar; l2 <= maxBar; l2++ {
					r2, add := min(r+1, 2), 0.0
					if l2 != l {
						r2, add = 1, barChange
						if r == 1 && from[p][l][r].l != 0 {
							add += barSingle
						}
					}
					if v := c + add + barCost(q, l2); v < cost[q][l2][r2] {
						cost[q][l2][r2] = v
						from[q][l2][r2] = key{p, l, r}
					}
				}
			}
		}
	}
	var rev []int
	for k := end; k.p >= 0; k = from[k.p][k.l][k.r] {
		rev = append(rev, k.l)
		first = k.p
	}
	nums = make([]int, len(rev))
	for i, l := range rev {
		nums[len(rev)-1-i] = l
	}
	return first, nums
}
