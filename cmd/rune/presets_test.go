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
	"unstable.build/rune/internal/ide/idepreset"
	"unstable.build/rune/internal/ide/keymeta"
	"unstable.build/rune/internal/ide/starlarkconfig"
)

var presetFiles = []string{
	"preset_modal_darwin.yaml",
	"preset_modal_linux.yaml",
	"preset_helix_darwin.yaml",
	"preset_helix_linux.yaml",
	"preset_standard_darwin.yaml",
	"preset_standard_linux.yaml",
	"preset_emacs_darwin.yaml",
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

var linuxPresetModes = map[string]string{
	"preset_modal_linux.yaml":    "vim",
	"preset_helix_linux.yaml":    "helix",
	"preset_standard_linux.yaml": "standard",
	"preset_emacs_linux.yaml":    "emacs",
}

// TestLinuxPresetsRenderEveryMetaKey renders each Linux preset with every
// <meta> meaning its editor is offered, whatever the host OS, so the
// files cannot drift from the prompt's option table.
func TestLinuxPresetsRenderEveryMetaKey(t *testing.T) {
	for name, mode := range linuxPresetModes {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, meta := range keymeta.Options("linux", mode) {
			for _, telemetry := range []bool{true, false} {
				body, err := idepreset.Render(string(raw),
					idepreset.Data{Meta: meta, Telemetry: telemetry})
				if err != nil {
					t.Errorf("%s with %s: %v", name, meta, err)
					continue
				}
				if !strings.Contains(body, fmt.Sprintf("meta_key: %q", meta.String())) {
					t.Errorf("%s with %s: meta_key not written", name, meta)
				}
			}
		}
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
	return decodeKeyBindings(t, name, raw)
}

// decodeKeyBindings returns command.key_bindings of a preset body, with list
// values formatted by fmt.Sprint, so a check does not depend on how the
// preset quotes a command.
func decodeKeyBindings(t *testing.T, name string, raw []byte) map[string]string {
	t.Helper()
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

// TestPresetsSpellPackageKeysLikeThePackage pins that a preset overriding a
// chord the fuzzy_search package binds spells it exactly as the package
// does. Package installs merge key bindings by their raw spelling, so a
// different spelling of the same chord lands next to the preset's and
// which one runs is left to map order.
func TestPresetsSpellPackageKeysLikeThePackage(t *testing.T) {
	src, err := os.ReadFile("../extension_fuzzy_search/config.star")
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{
		"modal": "vim", "helix": "helix", "standard": "standard", "emacs": "emacs",
	}
	for _, name := range presetFiles {
		mode := modes[strings.Split(name, "_")[1]]
		pkg, err := starlarkconfig.Decode(starlarkconfig.Source{
			Src:      src,
			Filename: "config.star",
			Params: map[string]any{
				"RUNE_DATADIR":     "/data",
				"RUNE_PKG_ID":      "fuzzy_search",
				"RUNE_PKG_VERSION": "1",
				"RUNE_EDITOR_MODE": mode,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		pkgKeys := map[handler.Sequence]string{}
		pkgBindings := pkg["command"].(map[string]any)["key_bindings"].(map[string]any)
		for key := range pkgBindings {
			pkgKeys[parseBinding(t, key)] = key
		}
		for key, cmd := range presetKeyBindings(t, name) {
			pkgKey, ok := pkgKeys[parseBinding(t, key)]
			if !ok || pkgKey == key || fmt.Sprint(pkgBindings[pkgKey]) == cmd {
				continue
			}
			t.Errorf("%s binds %s to %q; spell it %s like fuzzy_search does",
				name, key, cmd, pkgKey)
		}
	}
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

// TestHelixPresetSharesVimLayoutChords pins that the macOS helix preset
// binds every layout chord the macOS vim preset binds, so both editors
// share one set of Runic layout keys and one tutorial copy. Helix's own
// <ctrl-w> window menu stays as a backup for muscle memory, since a
// focused terminal swallows it. The Linux presets diverge on purpose: see
// TestLinuxPresetsSurviveAltAsMeta.
func TestHelixPresetSharesVimLayoutChords(t *testing.T) {
	// Chords Helix already binds by default, so the helix preset leaves them
	// to the editor.
	helixOwned := map[string]string{
		"<alt-`>": "switch_to_uppercase",
	}
	vim := presetKeyBindings(t, "preset_modal_darwin.yaml")
	helix := presetKeyBindings(t, "preset_helix_darwin.yaml")
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
}

// TestHelixPresetKeepsHelixSpellings pins the Helix key spellings the helix
// presets keep as backups for muscle memory on every OS: the window menu,
// also with <ctrl> still held or with arrows, as Helix accepts it, and the
// workspace diagnostics picker, which Rune's diagnostics list already
// covers. The jumplist keys reach the cursor history once the editor's
// own jumplist declines them, so both directions stay bound, and <space>j
// opens the history picker as it opens Helix's jumplist picker.
func TestHelixPresetKeepsHelixSpellings(t *testing.T) {
	for _, name := range []string{"preset_helix_darwin.yaml", "preset_helix_linux.yaml"} {
		helix := presetKeyBindings(t, name)
		for key, cmd := range map[string]string{
			"<ctrl-w>q":        "windowclose",
			"<ctrl-w>o":        "windowcloseall",
			"<ctrl-w>s":        "windownew down",
			"<ctrl-w>v":        "windownew right",
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
	for _, name := range []string{"preset_helix_darwin.yaml", "preset_helix_linux.yaml"} {
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

// TestLinuxPresetsResizeDirectionally pins that every Linux preset binds
// the four directional resizes, so no editor leaves them to typed commands.
func TestLinuxPresetsResizeDirectionally(t *testing.T) {
	for name := range linuxPresetModes {
		bound := map[string]bool{}
		for _, cmd := range presetKeyBindings(t, name) {
			bound[cmd] = true
		}
		for _, cmd := range []string{
			"windowresize increase height", "windowresize decrease width",
			"windowresize decrease height", "windowresize increase width",
		} {
			if !bound[cmd] {
				t.Errorf("%s does not bind %q", name, cmd)
			}
		}
	}
}

// TestLinuxPresetsKeepBindingsApartUnderEveryMetaKey pins that no two
// bindings of a Linux preset land on the same keys under any <meta> meaning
// its editor is offered, since one of them would silently never run.
func TestLinuxPresetsKeepBindingsApartUnderEveryMetaKey(t *testing.T) {
	for name, mode := range linuxPresetModes {
		bindings := presetKeyBindings(t, name)
		for _, meta := range keymeta.Options("linux", mode) {
			seen := map[handler.Sequence]string{}
			for key, cmd := range bindings {
				if cmd == "" {
					continue
				}
				seq := meta.ApplySequence(parseBinding(t, key))
				if other, ok := seen[seq]; ok && bindings[other] != cmd {
					t.Errorf("%s with %s: %s and %s land on the same key",
						name, meta, key, other)
				}
				seen[seq] = key
			}
		}
	}
}

// parseBinding parses a command.key_bindings key the way the IDE does: as
// a two-key sequence when possible, otherwise as a single chord.
func parseBinding(t *testing.T, key string) handler.Sequence {
	t.Helper()
	if seq, err := handler.ParseSequence(key); err == nil {
		return seq
	}
	comb, err := term.ParseKey(key)
	if err != nil {
		t.Fatalf("parse %q: %v", key, err)
	}
	return handler.Sequence{First: comb}
}

// emacsGNUChords are the only <alt> and <ctrl-alt> command bindings the
// Linux emacs preset may carry: chords GNU Emacs itself defines for the
// same action. Everything Rune-only lives on <meta>.
var emacsGNUChords = map[string]bool{
	"<alt-shift-x>": true, "<alt-,>": true, "<ctrl-alt-,>": true,
	"<alt-.>": true, "<ctrl-alt-.>": true, "<alt-shift-/>": true,
	"<ctrl-alt-shift-/>": true, "<alt-s>o": true, `<ctrl-alt-\\>`: true,
	"<ctrl-alt-i>": true,
}

// TestLinuxPresetsKeepRuneOffAlt pins that every Linux preset keeps its
// Rune commands on the <meta> layer, so <alt> stays with the editors and
// the terminal. A binding to "" only frees a chord an extension claimed,
// and a chord that also holds <meta> is still on Rune's layer.
func TestLinuxPresetsKeepRuneOffAlt(t *testing.T) {
	for _, name := range []string{
		"preset_modal_linux.yaml", "preset_helix_linux.yaml",
		"preset_standard_linux.yaml", "preset_emacs_linux.yaml",
	} {
		for key, cmd := range presetKeyBindings(t, name) {
			if cmd == "" {
				continue
			}
			seq := parseBinding(t, key)
			mod := seq.First.Mod | seq.Last.Mod
			if mod&term.ModAlt == 0 || mod&term.ModMeta != 0 {
				continue
			}
			if name == "preset_emacs_linux.yaml" && emacsGNUChords[key] {
				continue
			}
			t.Errorf("%s binds %s to %q on <alt>", name, key, cmd)
		}
	}
}
