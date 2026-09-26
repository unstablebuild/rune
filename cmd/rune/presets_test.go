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
	"preset_vim_darwin.yaml",
	"preset_vim_linux.yaml",
	"preset_helix_darwin.yaml",
	"preset_helix_linux.yaml",
	"preset_standard_darwin.yaml",
	"preset_standard_linux.yaml",
	"preset_emacs_darwin.yaml",
	"preset_emacs_linux.yaml",
}

var linuxPresetFiles = []string{
	"preset_vim_linux.yaml",
	"preset_helix_linux.yaml",
	"preset_standard_linux.yaml",
	"preset_emacs_linux.yaml",
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
	for _, tc := range []struct {
		os, vim, helix string
		// Chords Helix already binds by default, so the helix preset
		// leaves them to the editor.
		helixOwned map[string]string
		// A terminal swallows prefix chords such as <ctrl-w>, so every
		// close the tutorials teach needs a single chord.
		closes map[string]string
	}{
		{
			os:    "darwin",
			vim:   "preset_vim_darwin.yaml",
			helix: "preset_helix_darwin.yaml",
			helixOwned: map[string]string{
				"<alt-`>": "switch_to_uppercase",
			},
			closes: map[string]string{
				"<meta-w>":       "windowclose",
				"<shift-meta-w>": "windowcloseall",
				"<alt-w>":        "tabclose",
			},
		},
		{
			os:    "linux",
			vim:   "preset_vim_linux.yaml",
			helix: "preset_helix_linux.yaml",
			closes: map[string]string{
				"<alt-w>":       "windowclose",
				"<alt-shift-w>": "windowcloseall",
				"<ctrl-alt-w>":  "tabclose",
			},
		},
	} {
		t.Run(tc.os, func(t *testing.T) {
			vim := presetKeyBindings(t, tc.vim)
			helix := presetKeyBindings(t, tc.helix)
			for key, cmd := range vim {
				if !layoutCommand.MatchString(cmd) {
					continue
				}
				if strings.HasPrefix(key, "<alt-shift-") && strings.HasPrefix(cmd, "tabmove ") &&
					cmd != "tabmove left" && cmd != "tabmove right" {
					// <alt-shift-N> shares its keys with Helix's
					// shifted-digit commands (A-!, A-*), so the whole row
					// stays Helix's.
					continue
				}
				if reason, ok := tc.helixOwned[key]; ok {
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
			for key, cmd := range tc.closes {
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
		})
	}
}

var helixPresetFiles = []string{"preset_helix_darwin.yaml", "preset_helix_linux.yaml"}

// TestHelixPresetKeepsHelixSpellings pins the Helix key spellings the helix
// preset keeps as backups for muscle memory: the window menu with <ctrl>
// still held or with arrows, as Helix accepts it, and the workspace
// diagnostics picker, which Rune's diagnostics list already covers. The
// jumplist keys reach the cursor history once the editor's own jumplist
// declines them, so both directions stay bound, and <space>j opens the
// history picker as it opens Helix's jumplist picker.
func TestHelixPresetKeepsHelixSpellings(t *testing.T) {
	want := map[string]string{
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
	}
	for _, name := range helixPresetFiles {
		helix := presetKeyBindings(t, name)
		for key, cmd := range want {
			if helix[key] != cmd {
				t.Errorf("%s binds %s to %q, want %q", name, key, helix[key], cmd)
			}
		}
	}
}

// TestHelixPresetTypableCommandAliases pins the Helix typable command names
// the helix preset aliases to their Rune counterparts. Rune has no per-split
// quit, so :q and :wq close the window, as <ctrl-w>q does.
func TestHelixPresetTypableCommandAliases(t *testing.T) {
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
	for _, name := range helixPresetFiles {
		raw, err := os.ReadFile(name)
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
		if !reflect.DeepEqual(cfg.Command.Aliases, want) {
			t.Errorf("%s aliases:\n got %v\nwant %v", name, cfg.Command.Aliases, want)
		}
	}
}

// presetKeyCombs returns every key combination a preset binds or names as
// a prompt key, keyed by the spelling it ships under. A sequence
// contributes both of its keys.
func presetKeyCombs(t *testing.T, name string) map[string][]term.KeyComb {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Command struct {
			Key         string         `yaml:"key"`
			HistoryKey  string         `yaml:"history_key"`
			KeyBindings map[string]any `yaml:"key_bindings"`
		} `yaml:"command"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	keys := make([]string, 0, len(cfg.Command.KeyBindings)+2)
	for key := range cfg.Command.KeyBindings {
		keys = append(keys, key)
	}
	for _, key := range []string{cfg.Command.Key, cfg.Command.HistoryKey} {
		if key != "" {
			keys = append(keys, key)
		}
	}
	out := make(map[string][]term.KeyComb, len(keys))
	for _, key := range keys {
		if seq, err := handler.ParseSequence(key); err == nil {
			out[key] = []term.KeyComb{seq.First, seq.Last}
			continue
		}
		k, err := term.ParseKey(key)
		if err != nil {
			t.Fatalf("%s: key %q: %v", name, key, err)
		}
		out[key] = []term.KeyComb{k}
	}
	return out
}

// TestLinuxPresetsShipNoSuperChords pins that no Linux preset binds Super:
// the desktop owns it, and a Rune chord there would either never arrive or
// take a shortcut away from the window manager.
func TestLinuxPresetsShipNoSuperChords(t *testing.T) {
	for _, name := range linuxPresetFiles {
		for key, combs := range presetKeyCombs(t, name) {
			for _, k := range combs {
				if k.Mod&term.ModMeta != 0 {
					t.Errorf("%s binds %s, which needs Super", name, key)
				}
			}
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// Commented examples are copied into user configs verbatim.
		for i, l := range strings.Split(string(raw), "\n") {
			if strings.Contains(l, "<meta") || strings.Contains(l, "-meta-") {
				t.Errorf("%s:%d mentions a Super chord: %s", name, i+1, l)
			}
		}
	}
}

// linuxDesktopChords lists the chords common Linux desktops reserve before
// the focused window sees them: virtual consoles, workspace switching,
// screen lock, terminal launch, show desktop, logout, the X server kill
// switch, the run dialog and the window switchers.
func linuxDesktopChords() map[term.KeyComb]string {
	out := map[term.KeyComb]string{
		{Mod: term.ModCtrlAlt, Ch: 'l'}:                "lock screen",
		{Mod: term.ModCtrlAlt, Ch: 't'}:                "open a terminal",
		{Mod: term.ModCtrlAlt, Ch: 'd'}:                "show desktop",
		{Mod: term.ModCtrlAlt, Key: term.KeyDelete}:    "log out",
		{Mod: term.ModCtrlAlt, Key: term.KeyBackspace}: "kill the X server",
		{Mod: term.ModAlt, Key: term.KeyTab}:           "switch windows",
		{Mod: term.ModAltShift, Key: term.KeyTab}:      "switch windows",
		{Mod: term.ModAlt, Key: term.KeySpace}:         "window menu",
		{Mod: term.ModAlt, Key: term.KeyEsc}:           "switch windows",
	}
	for _, k := range []term.Key{
		term.KeyArrowUp, term.KeyArrowDown, term.KeyArrowLeft, term.KeyArrowRight,
	} {
		out[term.KeyComb{Mod: term.ModCtrlAlt, Key: k}] = "switch workspace"
		out[term.KeyComb{Mod: term.ModCtrlShiftAlt, Key: k}] = "move window to workspace"
	}
	for k := term.KeyF1; k <= term.KeyF12; k++ {
		out[term.KeyComb{Mod: term.ModCtrlAlt, Key: k}] = "switch virtual console"
		out[term.KeyComb{Mod: term.ModAlt, Key: k}] = "desktop shortcut"
	}
	return out
}

// TestLinuxPresetsAvoidDesktopChords pins that no Linux preset binds a
// chord the desktop takes before Rune can see it. The vim and helix
// presets keep <ctrl-alt-l> on the h/j/k/l resize row: GNOME locks the
// screen with Super+L, and users on desktops that still use <ctrl-alt-l>
// remap it.
func TestLinuxPresetsAvoidDesktopChords(t *testing.T) {
	reserved := linuxDesktopChords()
	accepted := map[string]string{
		"<ctrl-alt-l>": "windowresize increase width",
	}
	for _, name := range linuxPresetFiles {
		bindings := presetKeyBindings(t, name)
		for key, combs := range presetKeyCombs(t, name) {
			if cmd, ok := accepted[key]; ok && bindings[key] == cmd {
				continue
			}
			for _, k := range combs {
				if reason, ok := reserved[k]; ok {
					t.Errorf("%s binds %s, which the desktop uses to %s", name, key, reason)
				}
			}
		}
	}
}

// TestPresetsBindEachChordOnce pins that no preset binds the same key
// combination under two spellings, since only one of them would survive
// the config merge.
func TestPresetsBindEachChordOnce(t *testing.T) {
	for _, name := range presetFiles {
		seen := map[string]string{}
		for key, combs := range presetKeyCombs(t, name) {
			id := fmt.Sprint(combs)
			if prev, ok := seen[id]; ok && prev != key {
				t.Errorf("%s binds %s and %s to the same chord", name, prev, key)
			}
			seen[id] = key
		}
	}
}
