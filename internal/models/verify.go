package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Inventory is what a runner found in its models dir at startup.
type Inventory struct {
	// Recipes lists the recipe ids whose checkpoints are all present,
	// verified and allowed for the runner's stack, sorted.
	Recipes []string
	// Verified maps checkpoint id → path of the files that matched.
	Verified map[string]string
}

// Scan verifies the checkpoints under dir for a runner of stack. A present
// file with the wrong size or sha256 is an error (a tampered or half-copied
// image must not run), and so is any internal checkpoint on a public runner:
// the public image must not carry non-public weights at all. Missing files
// only make the recipes that need them unavailable. Package-relative
// checkpoints (transcription adapters) are checked by the image build.
func Scan(dir, stack string) (Inventory, error) { return reg.scan(dir, stack) }

func (reg registry) scan(dir, stack string) (Inventory, error) {
	if !ValidStack(stack) {
		return Inventory{}, fmt.Errorf("models: invalid runner stack %q", stack)
	}
	inv := Inventory{Verified: map[string]string{}}
	for _, c := range reg.order {
		if c.Package != "" {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(c.Path))
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !Allowed(stack, c.Class) {
			return Inventory{}, fmt.Errorf("models: %s checkpoint %s present on a %s runner (%s)", c.Class, c.ID, stack, p)
		}
		if st.Size() != c.Size {
			return Inventory{}, fmt.Errorf("models: %s: size %d, manifest says %d", p, st.Size(), c.Size)
		}
		sum, err := fileSHA256(p)
		if err != nil {
			return Inventory{}, err
		}
		if sum != c.SHA256 {
			return Inventory{}, fmt.Errorf("models: %s: sha256 %s, manifest says %s", p, sum, c.SHA256)
		}
		inv.Verified[c.ID] = p
	}
	for _, rc := range reg.recipes {
		ok := true
		for _, id := range rc.Checkpoints {
			if _, v := inv.Verified[id]; !v {
				ok = false
				break
			}
		}
		if ok {
			inv.Recipes = append(inv.Recipes, rc.ID)
		}
	}
	sort.Strings(inv.Recipes)
	return inv, nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
