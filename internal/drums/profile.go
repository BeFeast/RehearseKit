package drums

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed profiles/*.json
var profileFS embed.FS

// Profile maps articulations to MIDI keys for one target (GM now; an SD3
// preset later, as another JSON file next to gm.json).
type Profile struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Notes map[string]int `json:"notes"`
}

// DefaultProfile is the profile used when an edit document names none.
const DefaultProfile = "gm"

var profiles = func() map[string]Profile {
	entries, err := profileFS.ReadDir("profiles")
	if err != nil {
		panic(err)
	}
	out := map[string]Profile{}
	for _, e := range entries {
		b, err := profileFS.ReadFile("profiles/" + e.Name())
		if err != nil {
			panic(err)
		}
		var p Profile
		if err := json.Unmarshal(b, &p); err != nil {
			panic(fmt.Sprintf("drums: profile %s: %v", e.Name(), err))
		}
		for _, a := range Articulations {
			if _, ok := p.Notes[a]; !ok {
				panic(fmt.Sprintf("drums: profile %s lacks %s", p.ID, a))
			}
		}
		out[p.ID] = p
	}
	return out
}()

// ProfileByID returns a mapping profile.
func ProfileByID(id string) (Profile, bool) {
	p, ok := profiles[id]
	return p, ok
}

// ProfileIDs lists the known profiles.
func ProfileIDs() []string {
	var ids []string
	for id := range profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Key returns the MIDI key of an articulation.
func (p Profile) Key(art string) (int, bool) {
	k, ok := p.Notes[art]
	return k, ok
}

// Articulation is the inverse of Key: the first articulation in row order
// that the profile writes to key.
func (p Profile) Articulation(key int) (string, bool) {
	for _, a := range Articulations {
		if p.Notes[a] == key {
			return a, true
		}
	}
	return "", false
}
