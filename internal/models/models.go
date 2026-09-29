// Package models is the registry of model weights and separation recipes,
// and the entitlement rule that picks a recipe for a job.
//
// Two stacks exist. "public" is what the service offers to everyone and may
// only contain checkpoints whose publisher gave a permissive licence.
// "internal" (owner and an allowlist, never sold) may use everything,
// including research-only and unlicensed weights. The invariant — a public
// job never loads an internal checkpoint — is checked here (Validate, at
// init), again by the runner before it loads anything (Check), and by the
// runner image build (deploy/gpu-runner/fetch_models.py), which reads the
// same manifest.json.
package models

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// Stacks and checkpoint classes share names: a public stack may only load
// public checkpoints, an internal stack loads both.
const (
	Public   = "public"
	Internal = "internal"
)

// Engines run a recipe on the runner.
const (
	EngineDemucs = "demucs" // python -m demucs -n <recipe id>
	EngineMSST   = "msst"   // tools/separate/separate.py --recipe <id>
)

// Stem name sets.
var (
	FourStems = []string{"vocals", "drums", "bass", "other"}
	SixStems  = []string{"vocals", "drums", "bass", "other", "guitar", "piano"}
)

// Checkpoint is one pinned weights (or config) file.
type Checkpoint struct {
	ID      string `json:"id"`
	Class   string `json:"class"`
	Licence string `json:"licence"`
	// Path is relative to the runner's models dir, or to the installed
	// Python package when Package is set.
	Path    string `json:"path"`
	Package string `json:"package,omitempty"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	URL     string `json:"url"`
	// Role "transcribe" marks weights used by the transcription adapters
	// rather than by a separation recipe.
	Role string `json:"role,omitempty"`
}

// Recipe is a separation pipeline: the engine, the stems it yields and the
// checkpoints it loads.
type Recipe struct {
	ID          string   `json:"id"`
	Engine      string   `json:"engine"`
	StemCount   int      `json:"stems"`
	Checkpoints []string `json:"checkpoints"`
	Label       string   `json:"label"`
}

// Stems returns the stem names the recipe produces.
func (r Recipe) Stems() []string {
	if r.StemCount == 6 {
		return SixStems
	}
	return FourStems
}

// Class is the most restrictive class among the recipe's checkpoints.
func (r Recipe) Class() string {
	for _, id := range r.Checkpoints {
		if c, ok := reg.checkpoints[id]; !ok || c.Class != Public {
			return Internal
		}
	}
	return Public
}

//go:embed manifest.json
var manifestJSON []byte

type manifest struct {
	Checkpoints []Checkpoint                 `json:"checkpoints"`
	Recipes     []Recipe                     `json:"recipes"`
	Stacks      map[string]map[string]string `json:"stacks"`
}

type registry struct {
	checkpoints map[string]Checkpoint
	recipes     map[string]Recipe
	stacks      map[string]map[string]string
	order       []Checkpoint
}

var reg = mustLoad(manifestJSON)

func mustLoad(b []byte) registry {
	r, err := load(b)
	if err != nil {
		panic("models: manifest.json: " + err.Error())
	}
	return r
}

func load(b []byte) (registry, error) {
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return registry{}, err
	}
	r := registry{checkpoints: map[string]Checkpoint{}, recipes: map[string]Recipe{}, stacks: m.Stacks, order: m.Checkpoints}
	for _, c := range m.Checkpoints {
		r.checkpoints[c.ID] = c
	}
	for _, rc := range m.Recipes {
		r.recipes[rc.ID] = rc
	}
	return r, validate(r)
}

// validate enforces the manifest's own rules: every checkpoint is pinned,
// every recipe refers to known checkpoints, and a public stack maps only
// to recipes whose checkpoints are all public.
func validate(r registry) error {
	for _, c := range r.order {
		if c.Class != Public && c.Class != Internal {
			return fmt.Errorf("checkpoint %s: class %q", c.ID, c.Class)
		}
		if len(c.SHA256) != 64 || c.Size <= 0 || c.Path == "" || c.URL == "" {
			return fmt.Errorf("checkpoint %s: sha256, size, path and url are required", c.ID)
		}
	}
	for _, rc := range r.recipes {
		if rc.Engine != EngineDemucs && rc.Engine != EngineMSST {
			return fmt.Errorf("recipe %s: engine %q", rc.ID, rc.Engine)
		}
		if rc.StemCount != 4 && rc.StemCount != 6 {
			return fmt.Errorf("recipe %s: %d stems", rc.ID, rc.StemCount)
		}
		for _, id := range rc.Checkpoints {
			if _, ok := r.checkpoints[id]; !ok {
				return fmt.Errorf("recipe %s: unknown checkpoint %s", rc.ID, id)
			}
		}
	}
	for stack, qs := range r.stacks {
		if stack != Public && stack != Internal {
			return fmt.Errorf("stack %q", stack)
		}
		for q, id := range qs {
			rc, ok := r.recipes[id]
			if !ok {
				return fmt.Errorf("stack %s quality %s: unknown recipe %s", stack, q, id)
			}
			if stack == Public {
				for _, cid := range rc.Checkpoints {
					if c := r.checkpoints[cid]; c.Class != Public {
						return fmt.Errorf("stack public quality %s: recipe %s loads %s checkpoint %s", q, id, c.Class, cid)
					}
				}
			}
		}
	}
	return nil
}

// ValidStack reports whether s names a stack.
func ValidStack(s string) bool { return s == Public || s == Internal }

// RecipeFor returns the recipe a stack uses for a quality preset; ok is
// false when the stack does not offer that quality.
func RecipeFor(stack, quality string) (Recipe, bool) {
	id, ok := reg.stacks[stack][quality]
	if !ok {
		return Recipe{}, false
	}
	return reg.recipes[id], true
}

// Lookup returns a recipe by id.
func Lookup(id string) (Recipe, bool) {
	r, ok := reg.recipes[id]
	return r, ok
}

// Qualities lists the quality presets a stack offers, in display order.
func Qualities(stack string) []string {
	order := []string{"fast", "high", "high6", "hifi"}
	var out []string
	for _, q := range order {
		if _, ok := reg.stacks[stack][q]; ok {
			out = append(out, q)
		}
	}
	return out
}

// CheckpointByID returns a checkpoint.
func CheckpointByID(id string) (Checkpoint, bool) {
	c, ok := reg.checkpoints[id]
	return c, ok
}

// Checkpoints returns every checkpoint in manifest order.
func Checkpoints() []Checkpoint { return slices.Clone(reg.order) }

// Recipes returns every recipe sorted by id.
func Recipes() []Recipe {
	out := make([]Recipe, 0, len(reg.recipes))
	for _, r := range reg.recipes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Allowed reports whether a job on stack may load checkpoints of class.
func Allowed(stack, class string) bool {
	return class == Public || (class == Internal && stack == Internal)
}

// Check is the runner-side guard run before any weights are loaded: the
// recipe must exist, and every checkpoint it loads must be allowed for both
// the job's stack and the runner's own stack.
func Check(recipeID, jobStack, runnerStack string) (Recipe, error) {
	return reg.check(recipeID, jobStack, runnerStack)
}

func (reg registry) check(recipeID, jobStack, runnerStack string) (Recipe, error) {
	rc, ok := reg.recipes[recipeID]
	if !ok {
		return Recipe{}, fmt.Errorf("models: unknown recipe %q", recipeID)
	}
	if !ValidStack(jobStack) || !ValidStack(runnerStack) {
		return Recipe{}, fmt.Errorf("models: invalid stack (job %q, runner %q)", jobStack, runnerStack)
	}
	for _, id := range rc.Checkpoints {
		c := reg.checkpoints[id]
		if !Allowed(jobStack, c.Class) || !Allowed(runnerStack, c.Class) {
			return Recipe{}, fmt.Errorf("models: recipe %s loads %s checkpoint %s, not allowed for a %s job on a %s runner",
				recipeID, c.Class, id, jobStack, runnerStack)
		}
	}
	return rc, nil
}

// LegacyRecipes are what a runner that predates the model registry can run
// (it sends no recipe list): the three Demucs presets.
var LegacyRecipes = []string{"htdemucs", "htdemucs_ft", "htdemucs_6s"}
