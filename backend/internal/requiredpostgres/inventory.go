// Package requiredpostgres implements the required PostgreSQL test inventory and runner.
package requiredpostgres

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

type Inventory struct {
	Version    int         `json:"version"`
	Groups     []Group     `json:"groups"`
	Exclusions []Exclusion `json:"exclusions"`
}
type Group struct {
	Package string `json:"package"`
	Timeout string `json:"timeout"`
	Tests   []Test `json:"tests"`
}
type Test struct {
	Name          string   `json:"name"`
	RequiredPaths []string `json:"required_paths"`
}
type Exclusion struct {
	Package string `json:"package"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

var packageName = regexp.MustCompile(`^\./internal(?:/[A-Za-z0-9_-]+)+$`)
var testName = regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)
var pathPart = regexp.MustCompile(`^[A-Za-z0-9_=.\-]+$`)

// checkObjectKeys rejects duplicate keys at every depth, including unknown nested objects.
func checkObjectKeys(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return fmt.Errorf("duplicate or invalid JSON key %q", k)
			}
			seen[s] = true
			if err := checkObjectKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case json.Delim('['):
		for d.More() {
			if err := checkObjectKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	default:
		return nil
	}
}

func Load(path string) (Inventory, error) {
	var inv Inventory
	b, err := os.ReadFile(path)
	if err != nil {
		return inv, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if err = checkObjectKeys(d); err != nil {
		return inv, err
	}
	if _, err = d.Token(); err != io.EOF {
		return inv, fmt.Errorf("trailing JSON data")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&inv); err != nil {
		return inv, err
	}
	return inv, inv.Validate()
}

func (inv Inventory) Validate() error {
	if inv.Version != 1 || len(inv.Groups) == 0 || inv.Exclusions == nil {
		return fmt.Errorf("invalid inventory version, empty groups, or missing exclusions")
	}
	groups, entries := map[string]bool{}, map[string]bool{}
	for _, g := range inv.Groups {
		if !packageName.MatchString(g.Package) || groups[g.Package] || len(g.Tests) == 0 {
			return fmt.Errorf("invalid or duplicate package %q", g.Package)
		}
		groups[g.Package] = true
		duration, err := time.ParseDuration(g.Timeout)
		if err != nil || duration <= 0 {
			return fmt.Errorf("invalid timeout for %s", g.Package)
		}
		for _, t := range g.Tests {
			key := g.Package + "/" + t.Name
			if !testName.MatchString(t.Name) || entries[key] {
				return fmt.Errorf("invalid or duplicate test %s", key)
			}
			entries[key] = true
			if t.RequiredPaths == nil {
				return fmt.Errorf("missing required_paths for %s", key)
			}
			paths := map[string]bool{}
			for _, p := range t.RequiredPaths {
				parts := strings.Split(p, "/")
				if parts[0] != t.Name || paths[p] {
					return fmt.Errorf("invalid or duplicate required path %s", p)
				}
				paths[p] = true
				for _, part := range parts[1:] {
					if !pathPart.MatchString(part) {
						return fmt.Errorf("invalid required path %s", p)
					}
				}
			}
		}
	}
	for _, x := range inv.Exclusions {
		key := x.Package + "/" + x.Name
		if !packageName.MatchString(x.Package) || !testName.MatchString(x.Name) || strings.TrimSpace(x.Reason) == "" || entries[key] {
			return fmt.Errorf("invalid exclusion %s", key)
		}
		entries[key] = true
	}
	return nil
}
