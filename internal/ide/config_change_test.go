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

package ide

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func TestConfigChange(t *testing.T) {
	t.Parallel()
	parse := func(t *testing.T, src string) map[string]any {
		t.Helper()
		var m map[string]any
		if err := yaml.Unmarshal([]byte(src), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	const old = `
editor:
  mode: vim
  tabspaces: 4
gui:
  env:
    GOROOT: /usr/local/go
  default_theme: romero
extensions:
  rune-agent:
    path: rune-agent-bin
    config: {status_bar: {enabled: true}}
  styled:
    config: {theme: dark}
  gone:
    path: gone-bin
tutorials:
  intro: intro.star
sounds:
  ding: ding.wav
`
	const cur = `
editor:
  mode: emacs
  tabspaces: 4
gui:
  env:
    GOROOT: /opt/go
    PATH: /opt/go/bin:$PATH
  default_theme: romero
extensions:
  rune-agent:
    path: rune-agent-bin
    config: {status_bar: {enabled: false}}
  styled:
    path: styled-bin
    config: {theme: dark}
  new:
    path: new-bin
  configured:
    config: {a: 1}
tutorials:
  intro: intro.star
  advanced: advanced.star
sounds:
  ding: ding.wav
acp:
  claude: {command: claude}
`
	change := ConfigChange{Old: parse(t, old), New: parse(t, cur), Source: "test"}

	t.Run("TouchesPath", func(t *testing.T) {
		t.Parallel()
		for _, tt := range []struct {
			path []string
			want bool
		}{
			{[]string{"editor"}, true},
			{[]string{"editor", "mode"}, true},
			{[]string{"editor", "tabspaces"}, false},
			{[]string{"gui"}, true},
			{[]string{"gui", "env"}, true},
			{[]string{"gui", "default_theme"}, false},
			{[]string{"gui", "themes"}, false},
			{[]string{"acp"}, true},
			{[]string{"sounds"}, false},
			{[]string{"missing", "deep"}, false},
		} {
			assert.Equal(t, tt.want, change.TouchesPath(tt.path...), "%v", tt.path)
		}
	})

	t.Run("AddedExtensionIDs", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, []string{"new", "styled"}, change.AddedExtensionIDs(),
			"the extensions the change gives a path, not the one only configured")
	})

	t.Run("UnchangedExtensionPaths", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, []string{"rune-agent"}, change.UnchangedExtensionPaths())
	})

	t.Run("AddedTutorialNames", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, []string{"advanced"}, change.AddedTutorialNames())
	})

	t.Run("ChangedPaths", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, [][]string{
			{"acp", "claude"},
			{"editor", "mode"},
			{"extensions", "configured"},
			{"extensions", "gone"},
			{"extensions", "new"},
			{"extensions", "rune-agent"},
			{"extensions", "styled"},
			{"gui", "env"},
			{"tutorials", "advanced"},
		}, change.ChangedPaths())
	})

	t.Run("summarize", func(t *testing.T) {
		t.Parallel()
		inEffect, pending := change.summarize([][]string{
			{"gui", "env"}, {"extensions", "new"}, {"extensions", "styled"}, {"acp"},
		})
		assert.Equal(t, []string{"acp.claude", "extensions.new", "extensions.styled", "gui.env"},
			inEffect)
		assert.Equal(t, []string{"editor.mode", "extensions.configured", "extensions.gone",
			"extensions.rune-agent", "tutorials.advanced"}, pending)
	})

	t.Run("a nil tree is empty", func(t *testing.T) {
		t.Parallel()
		fromNothing := ConfigChange{New: parse(t, cur)}
		assert.True(t, fromNothing.TouchesPath("editor", "mode"))
		assert.Equal(t, []string{"new", "rune-agent", "styled"}, fromNothing.AddedExtensionIDs())
		assert.Empty(t, fromNothing.UnchangedExtensionPaths())
		assert.Equal(t, []string{"advanced", "intro"}, fromNothing.AddedTutorialNames())
		assert.Contains(t, fromNothing.ChangedPaths(), []string{"sounds", "ding"})
		assert.Empty(t, ConfigChange{}.ChangedPaths())
	})
}
