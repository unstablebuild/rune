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
	"os"
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
