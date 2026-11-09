// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package sounds

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// Registry is a Player that resolves names to Sounds and plays them
// through an Output. It knows the reserved sounds plus the entries given
// to Set. It is safe for concurrent use.
type Registry struct {
	output Backend

	mu      sync.RWMutex
	entries map[string]string
}

var _ Player = (*Registry)(nil)

// NewRegistry returns a Registry that plays through o and knows only the
// reserved sounds.
func NewRegistry(o Backend) *Registry {
	return &Registry{output: o}
}

// Set replaces the registry's entries; see Validate for their form. An
// invalid entry is dropped and reported in the returned error, and the
// rest still apply.
func (r *Registry) Set(entries map[string]string) error {
	valid, err := Validate(entries)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = valid
	return err
}

// Validate returns the valid subset of entries and an error describing
// every entry it left out. Each entry maps a sound name to one of:
//
//   - an audio file path, which is any value containing a "/";
//   - the name of another sound, reserved or in entries;
//   - "", which silences the sound.
//
// An entry for a reserved name overrides its built-in tone. A name must
// not be empty or contain a "/", and an entry is invalid when following
// names from it reaches an unknown sound or comes back to itself.
func Validate(entries map[string]string) (map[string]string, error) {
	valid := make(map[string]string, len(entries))
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		if name == "" || strings.Contains(name, "/") {
			errs = append(errs, fmt.Errorf(
				"sound name %q must not be empty or contain \"/\"", name))
			continue
		}
		valid[name] = entries[name]
	}
	// Every name on a valid entry's chain has a valid chain of its own,
	// so checking each entry against the unpruned map is enough.
	var invalid []string
	for _, name := range slices.Sorted(maps.Keys(valid)) {
		if err := checkChain(valid, name); err != nil {
			errs = append(errs, fmt.Errorf("sound %q: %w", name, err))
			invalid = append(invalid, name)
		}
	}
	for _, name := range invalid {
		delete(valid, name)
	}
	return valid, errors.Join(errs...)
}

func checkChain(entries map[string]string, name string) error {
	seen := map[string]bool{name: true}
	for value := entries[name]; isName(value); value = entries[value] {
		if seen[value] {
			return fmt.Errorf("refers back to itself through %q", value)
		}
		seen[value] = true
		if _, ok := entries[value]; !ok {
			if IsReserved(value) {
				return nil
			}
			return fmt.Errorf("refers to unknown sound %q", value)
		}
	}
	return nil
}

func isName(value string) bool {
	return value != "" && !strings.Contains(value, "/")
}

// Names returns the reserved sounds and every name with an entry, even
// one that silences it, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := slices.Collect(maps.Keys(r.entries))
	for _, name := range reserved {
		if _, ok := r.entries[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// resolve follows name through the entries to the Sound it plays. It
// returns false when name is unknown or silenced.
func (r *Registry) resolve(name string) (Sound, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ret := Sound{Name: name}
	for current := name; ; {
		if IsReserved(current) {
			ret.BuiltIn = current
		}
		value, ok := r.entries[current]
		switch {
		case !ok && IsReserved(current):
			return ret, true
		case !ok:
			return Sound{}, false
		case value == "":
			return Sound{}, false
		case !isName(value):
			ret.Path = value
			return ret, true
		}
		current = value
	}
}

// Play resolves name and plays it, dropping unknown and silenced names.
func (r *Registry) Play(name string) {
	if s, ok := r.resolve(name); ok {
		r.output.Play(s)
	}
}
