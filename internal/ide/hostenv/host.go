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

package hostenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/config"
)

// Host owns the environment of a Rune host process: the process that runs
// commands and terminals on a machine, whether it is the GUI, a `rune -x`
// workspace server or a headless node. It is safe for concurrent use.
type Host struct {
	dataDir    string
	shellRCDir string
	windows    bool

	mu        sync.Mutex
	inherited []string
	captured  bool
	base      []string
	env       config.Config
	applied   bool
	original  map[string]originalVar
	placed    []string
}

type originalVar struct {
	value string
	set   bool
}

// New returns the environment of the Rune host whose data directory is
// dataDir. Apply writes the shell fragments terminals source into
// shellRCDir, the directory returned by workspace.InstallShellRC; an empty
// shellRCDir writes none.
func New(dataDir, shellRCDir string) *Host {
	return &Host{
		dataDir:    dataDir,
		shellRCDir: shellRCDir,
		windows:    runtime.GOOS == "windows",
		original:   map[string]originalVar{},
	}
}

// SetBasePATH makes p, typically the PATH of the user's login shell, the PATH
// that Rune's entries are placed in front of. Entries of the PATH the process
// inherited that p lacks are kept after it, so a PATH set by a service
// manager survives. If Apply has run, the last environment is applied again
// on the new base.
func (h *Host) SetBasePATH(p string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.captureInherited()
	var base []string
	for _, e := range splitPATH(p, h.windows) {
		// The probed shell inherited Rune's entries from this process;
		// they are Rune's to place, not part of the user's PATH.
		if hasPATHDir(h.placed, e, h.windows) && !hasPATHDir(h.inherited, e, h.windows) {
			continue
		}
		base = append(base, e)
	}
	for _, e := range h.inherited {
		if !hasPATHDir(base, e, h.windows) {
			base = append(base, e)
		}
	}
	h.base = base
	if !h.applied {
		return nil
	}
	return h.apply()
}

// Apply sets up the process environment from env, the gui.env config block,
// which may be nil:
//
//   - RUNE_DATADIR names the data directory, whose bin and lib directories
//     are created;
//   - every gui.env variable but PATH is set, expanded against the process
//     environment;
//   - PATH is <datadir>/bin, then the entries gui.env.PATH lists before
//     $PATH, then the base PATH, then the entries it lists after $PATH;
//   - the shell fragments that re-apply all of the above in terminal shells
//     are written to the shell rc directory.
//
// Applying the same env again yields the same environment: entries are not
// stacked and a variable that refers to itself is expanded against the
// value it had before Rune set it.
func (h *Host) Apply(env config.Config) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if env == nil {
		env = config.NopConfig()
	}
	h.env = env
	h.applied = true
	h.captureInherited()
	return h.apply()
}

func (h *Host) captureInherited() {
	if h.captured {
		return
	}
	h.captured = true
	h.inherited = splitPATH(os.Getenv("PATH"), h.windows)
	if h.base == nil {
		h.base = h.inherited
	}
}

func (h *Host) apply() error {
	var errs []error
	if err := os.Setenv(DataDirVar, h.dataDir); err != nil {
		errs = append(errs, fmt.Errorf("set env %s: %w", DataDirVar, err))
	}
	if err := makePkgDirs(h.dataDir); err != nil {
		errs = append(errs, err)
	}

	vars := map[string]any{}
	h.env.Iterate(func(k string, v any) { vars[k] = v })
	pathValue, hasPATH := vars["PATH"]
	delete(vars, "PATH")

	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	exports := make([]envVar, 0, len(keys))
	for _, k := range keys {
		if _, ok := h.original[k]; !ok {
			v, set := os.LookupEnv(k)
			h.original[k] = originalVar{value: v, set: set}
		}
		v := evalVar(vars[k], h.lookup(k))
		if err := os.Setenv(k, v); err != nil {
			errs = append(errs, fmt.Errorf("set env %s: %w", k, err))
			continue
		}
		exports = append(exports, envVar{name: k, value: v})
	}

	prefix := []string{filepath.Join(h.dataDir, "bin")}
	var suffix []string
	if hasPATH {
		lookup := h.lookup("PATH")
		afterBase := false
		for _, chunk := range splitPATH(evalVar(pathValue, nil), h.windows) {
			if chunk == "$PATH" || chunk == "${PATH}" {
				afterBase = true
				continue
			}
			for _, e := range splitPATH(os.Expand(chunk, lookup), h.windows) {
				if afterBase {
					suffix = append(suffix, e)
				} else {
					prefix = append(prefix, e)
				}
			}
		}
	}
	prefix = dedupPATH(prefix, h.windows)
	var after []string
	for _, e := range dedupPATH(suffix, h.windows) {
		if !hasPATHDir(prefix, e, h.windows) && !hasPATHDir(h.base, e, h.windows) {
			after = append(after, e)
		}
	}
	h.placed = append(slices.Clone(prefix), after...)
	if err := os.Setenv("PATH", composePATH(h.windows, prefix, h.base, after)); err != nil {
		errs = append(errs, fmt.Errorf("set env PATH: %w", err))
	}

	if h.shellRCDir != "" {
		if err := writeShellFragments(h.shellRCDir, exports, prefix, after); err != nil {
			errs = append(errs, fmt.Errorf("write shell environment: %w", err))
		}
	}
	return errors.Join(errs...)
}

// lookup expands a gui.env value of the variable self: PATH is the base
// PATH and self is the value it had before Rune set it.
func (h *Host) lookup(self string) func(string) string {
	return func(name string) string {
		switch name {
		case "PATH":
			return composePATH(h.windows, nil, h.base, nil)
		case DataDirVar:
			return h.dataDir
		case self:
			return h.original[self].value
		}
		return os.Getenv(name)
	}
}

// evalVar renders a gui.env value: strings are expanded with lookup, or kept
// verbatim when lookup is nil, and other values are formatted with %v.
func evalVar(value any, lookup func(string) string) string {
	str, ok := value.(string)
	if !ok {
		return fmt.Sprintf("%v", value)
	}
	if lookup == nil {
		return str
	}
	return os.Expand(str, lookup)
}

func makePkgDirs(dataDir string) error {
	for _, sub := range []string{"bin", "lib"} {
		dir := filepath.Join(dataDir, sub)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return nil
}
