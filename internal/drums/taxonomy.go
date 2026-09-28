// Package drums is the drum editor's server side: the articulation
// taxonomy, the output mapping profiles (articulation → MIDI key), the edit
// revision stored as jobs/<id>/edits/drums.json, and the HTTP routes the SPA
// editor talks to. The model output (notes/drums.json) is never rewritten;
// an edit revision is a full snapshot of the events derived from it.
package drums

// Group is a row group of the editor (one drum, several articulations).
type Group struct {
	Key           string
	Label         string
	Articulations []Articulation
}

// Articulation is one playing technique with its own MIDI row.
type Articulation struct {
	Key   string
	Label string
}

// Groups is the editor taxonomy in row order. The order also decides
// which articulation a GM key seeds when several share it (none do in the
// GM profile, but a future profile may).
var Groups = []Group{
	{Key: "kick", Label: "Kick", Articulations: []Articulation{{"kick", "Kick"}}},
	{Key: "snare", Label: "Snare", Articulations: []Articulation{{"snare", "Snare"}, {"stick", "Side Stick"}}},
	{Key: "hh", Label: "Hi-Hat", Articulations: []Articulation{{"hhc", "Closed"}, {"hho", "Open"}, {"hhp", "Pedal"}}},
	{Key: "toms", Label: "Toms", Articulations: []Articulation{{"tomh", "High"}, {"tomm", "Mid"}, {"tomf", "Floor"}}},
	{Key: "ride", Label: "Ride", Articulations: []Articulation{{"ride", "Bow"}, {"bell", "Bell"}}},
	{Key: "crash", Label: "Crash", Articulations: []Articulation{{"crash", "Crash"}}},
}

// Articulations lists every articulation key in row order.
var Articulations = func() []string {
	var out []string
	for _, g := range Groups {
		for _, a := range g.Articulations {
			out = append(out, a.Key)
		}
	}
	return out
}()

var articulationSet = func() map[string]bool {
	m := map[string]bool{}
	for _, a := range Articulations {
		m[a] = true
	}
	return m
}()

// ValidArticulation reports whether key is in the taxonomy.
func ValidArticulation(key string) bool { return articulationSet[key] }
