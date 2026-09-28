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

package main

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/term"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/handler"
)

var presetFiles = []string{
	"preset_modal.yaml",
	"preset_helix.yaml",
	"preset_standard_darwin.yaml",
	"preset_standard_linux.yaml",
	"preset_emacs.yaml",
}

// commentedOption matches a commented-out YAML mapping entry or nested
// comment, as opposed to the prose in a section banner.
var commentedOption = regexp.MustCompile(
	`^# ( *(?:#.*|(?:[a-z_0-9]+|"[^"]*"|'[^']*') *:.*))$`)

func TestPresetCommentedBlocksUncomment(t *testing.T) {
	for _, name := range presetFiles {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var shipped map[string]any
		if err := yaml.Unmarshal(raw, &shipped); err != nil {
			t.Fatalf("%s as shipped: %v", name, err)
		}
		var out []string
		for _, l := range strings.Split(string(raw), "\n") {
			if m := commentedOption.FindStringSubmatch(l); m != nil {
				out = append(out, m[1])
				continue
			}
			if strings.HasPrefix(l, "#") {
				continue
			}
			out = append(out, l)
		}
		var full map[string]any
		if err := yaml.Unmarshal([]byte(strings.Join(out, "\n")), &full); err != nil {
			t.Errorf("%s fully uncommented: %v", name, err)
			continue
		}
		t.Logf("%s: shipped=%d keys, uncommented=%d keys",
			name, len(shipped), len(full))
	}
}

// TestPresetKeyBindingsParse pins that every shipped binding survives the
// same parse the IDE performs, so a preset cannot ship a key spelling the
// command layer silently drops.
func TestPresetKeyBindingsParse(t *testing.T) {
	for _, name := range presetFiles {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Command struct {
				KeyBindings map[string]any `yaml:"key_bindings"`
			} `yaml:"command"`
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(cfg.Command.KeyBindings) == 0 {
			t.Errorf("%s: no command key bindings", name)
			continue
		}
		for key, cmd := range cfg.Command.KeyBindings {
			if _, err := handler.ParseSequence(key); err == nil {
				continue
			}
			if _, err := term.ParseKey(key); err != nil {
				t.Errorf("%s: key binding %q (%v): %v", name, key, cmd, err)
			}
		}
	}
}

func presetKeyBindings(t *testing.T, name string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Command struct {
			KeyBindings map[string]any `yaml:"key_bindings"`
		} `yaml:"command"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out := make(map[string]string, len(cfg.Command.KeyBindings))
	for key, cmd := range cfg.Command.KeyBindings {
		out[key] = fmt.Sprint(cmd)
	}
	return out
}

// TestPresetsBindBothCursorHistoryDirections pins that a preset which binds
// a step back through the cursor history also binds the step forward, so
// the way back out of a jump is never a typed command.
func TestPresetsBindBothCursorHistoryDirections(t *testing.T) {
	for _, name := range presetFiles {
		bound := map[string]bool{}
		for _, cmd := range presetKeyBindings(t, name) {
			bound[cmd] = true
		}
		if bound["cursorhistory prev"] != bound["cursorhistory next"] {
			t.Errorf("%s binds only one of cursorhistory prev / next", name)
		}
	}
}

// layoutCommand matches a binding that runs, or prefills, a window or tab
// command.
var layoutCommand = regexp.MustCompile(`^(echo \{prompt\})?(window|tab)`)

// TestHelixPresetSharesVimLayoutChords pins that the helix preset binds every
// layout chord the vim preset binds, so both editors share one set of Runic
// layout keys and one tutorial copy. Helix's own <ctrl-w> window menu stays
// as a backup for muscle memory, since a focused terminal swallows it.
func TestHelixPresetSharesVimLayoutChords(t *testing.T) {
	// Chords Helix already binds by default, so the helix preset leaves them
	// to the editor.
	helixOwned := map[string]string{
		"<alt-`>": "switch_to_uppercase",
	}
	vim := presetKeyBindings(t, "preset_modal.yaml")
	helix := presetKeyBindings(t, "preset_helix.yaml")
	for key, cmd := range vim {
		if !layoutCommand.MatchString(cmd) {
			continue
		}
		if strings.HasPrefix(key, "<alt-shift-") && strings.HasPrefix(cmd, "tabmove ") {
			// <alt-shift-N> shares its keys with Helix's shifted-digit
			// commands (A-!, A-*), so the whole row stays Helix's.
			continue
		}
		if reason, ok := helixOwned[key]; ok {
			if helix[key] != "" {
				t.Errorf("helix preset binds %s, which Helix uses for %s", key, reason)
			}
			continue
		}
		if helix[key] != cmd {
			t.Errorf("helix preset binds %s to %q, want the vim preset's %q",
				key, helix[key], cmd)
		}
	}
	// A terminal swallows prefix chords such as <ctrl-w>, so every close
	// the tutorials teach needs a single chord.
	for key, cmd := range map[string]string{
		"<meta-w>":       "windowclose",
		"<shift-meta-w>": "windowcloseall",
		"<alt-w>":        "tabclose",
	} {
		if vim[key] != cmd {
			t.Errorf("vim preset binds %s to %q, want %q", key, vim[key], cmd)
		}
	}
	for key, cmd := range map[string]string{
		"<ctrl-w>q": "windowclose",
		"<ctrl-w>o": "windowcloseall",
		"<ctrl-w>s": "windownew down",
		"<ctrl-w>v": "windownew right",
	} {
		if helix[key] != cmd {
			t.Errorf("helix preset must keep Helix's %s bound to %q as a backup, got %q",
				key, cmd, helix[key])
		}
	}
}

// TestHelixPresetKeepsHelixSpellings pins the Helix key spellings the helix
// preset keeps as backups for muscle memory: the window menu with <ctrl>
// still held or with arrows, as Helix accepts it, and the workspace
// diagnostics picker, which Rune's diagnostics list already covers. The
// jumplist keys reach the cursor history once the editor's own jumplist
// declines them, so both directions stay bound, and <space>j opens the
// history picker as it opens Helix's jumplist picker.
func TestHelixPresetKeepsHelixSpellings(t *testing.T) {
	helix := presetKeyBindings(t, "preset_helix.yaml")
	for key, cmd := range map[string]string{
		"<ctrl-w><ctrl-h>": "windowfocus left",
		"<ctrl-w><ctrl-j>": "windowfocus down",
		"<ctrl-w><ctrl-k>": "windowfocus up",
		"<ctrl-w><ctrl-l>": "windowfocus right",
		"<ctrl-w><left>":   "windowfocus left",
		"<ctrl-w><down>":   "windowfocus down",
		"<ctrl-w><up>":     "windowfocus up",
		"<ctrl-w><right>":  "windowfocus right",
		"<ctrl-w><ctrl-w>": "windowfocus other",
		"<ctrl-w><ctrl-s>": "windownew down",
		"<ctrl-w><ctrl-v>": "windownew right",
		"<ctrl-w><ctrl-q>": "windowclose",
		"<ctrl-w><ctrl-o>": "windowcloseall",
		"<space>D":         "lsp diagnostics",
		"<ctrl-o>":         "cursorhistory prev",
		"<ctrl-i>":         "cursorhistory next",
		"<space>j":         "cursorhistory jump",
	} {
		if helix[key] != cmd {
			t.Errorf("helix preset binds %s to %q, want %q", key, helix[key], cmd)
		}
	}
}

// TestHelixPresetTypableCommandAliases pins the Helix typable command names
// the helix preset aliases to their Rune counterparts. Rune has no per-split
// quit, so :q and :wq close the window, as <ctrl-w>q does.
func TestHelixPresetTypableCommandAliases(t *testing.T) {
	raw, err := os.ReadFile("preset_helix.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Command struct {
			Aliases map[string]any `yaml:"aliases"`
		} `yaml:"command"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	openFile := map[string]any{"command": "edit", "completer": "files"}
	want := map[string]any{
		"q":   "windowclose",
		"wq":  []any{"write", "windowclose"},
		"x":   []any{"write", "windowclose"},
		"qa":  "quit",
		"qa!": "forcequit!",
		"wa":  "writeall",
		"o":   openFile,
		"bc":  "tabclose",
		"bca": "tabcloseall",
		"bn":  "tabnext",
		"bp":  "tabprevious",
		"vs":  "windownew right",
		"hs":  "windownew down",
		"sp":  "windownew down",
		"fmt": "lsp format",
		"rl":  "reloadfile!",
	}
	if !reflect.DeepEqual(cfg.Command.Aliases, want) {
		t.Errorf("helix preset aliases:\n got %v\nwant %v", cfg.Command.Aliases, want)
	}
}
