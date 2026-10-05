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
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestCommandPromptSeparatorCharsetDefaults(t *testing.T) {
	c := newAnimConfig(t, `config = {}`)
	got := c.commandPromptSeparatorCharset()
	assert.Equal(t, defaultCommandPromptSeparatorCharset(), got)
	assert.Empty(t, c.errors)
}

// hintConfig builds an ideConfig with the given command.key_bindings
// map so the key-hint lookup can be exercised directly.
func hintConfig(bindings map[string]any, extra map[string]any) ideConfig {
	command := map[string]any{"key_bindings": bindings}
	for k, v := range extra {
		command[k] = v
	}
	return ideConfig{
		cfg:    map[string]any{"command": command},
		errors: map[string]error{},
	}
}

func TestCommandKeyBindingHintColorDefault(t *testing.T) {
	c := newAnimConfig(t, `config = {}`)
	assert.Equal(t, term.ColorGray, c.commandKeyBindingHintColor())
	assert.Empty(t, c.errors)
}

func TestCommandKeyBindingHintColorNamedAndHex(t *testing.T) {
	named := newAnimConfig(t, `
config = {"command": {"key_binding_hint_color": "silver"}}
`)
	assert.Equal(t, term.GetColor("silver"), named.commandKeyBindingHintColor())
	assert.Empty(t, named.errors)

	hex := newAnimConfig(t, `
config = {"command": {"key_binding_hint_color": "#8a8a8a"}}
`)
	assert.Equal(t, term.GetColor("#8a8a8a"), hex.commandKeyBindingHintColor())
	assert.Empty(t, hex.errors)
}

func TestCommandKeyBindingHintColorWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {"command": {"key_binding_hint_color": ["nope"]}}
`)
	assert.Equal(t, term.ColorGray, c.commandKeyBindingHintColor())
	require.Contains(t, c.errors, "command.key_binding_hint_color")
}

func TestCommandKeyBindingHintFocusColorDefault(t *testing.T) {
	c := newAnimConfig(t, `config = {}`)
	assert.Equal(t, term.ColorSilver, c.commandKeyBindingHintFocusColor())
	assert.Empty(t, c.errors)
}

func TestCommandKeyBindingHintFocusColorNamedAndHex(t *testing.T) {
	named := newAnimConfig(t, `
config = {"command": {"key_binding_hint_focus_color": "gray"}}
`)
	assert.Equal(t, term.GetColor("gray"), named.commandKeyBindingHintFocusColor())
	assert.Empty(t, named.errors)

	hex := newAnimConfig(t, `
config = {"command": {"key_binding_hint_focus_color": "#c0c0c0"}}
`)
	assert.Equal(t, term.GetColor("#c0c0c0"), hex.commandKeyBindingHintFocusColor())
	assert.Empty(t, hex.errors)
}

func TestCommandKeyBindingHintFocusColorWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {"command": {"key_binding_hint_focus_color": ["nope"]}}
`)
	assert.Equal(t, term.ColorSilver, c.commandKeyBindingHintFocusColor())
	require.Contains(t, c.errors, "command.key_binding_hint_focus_color")
}

func TestCommandKeyBindingHintLookupDirect(t *testing.T) {
	c := hintConfig(map[string]any{
		"<m-n>":   "windownew",
		"<a-s-e>": "lsp diagnostics",
	}, nil)
	lookup := c.commandKeyBindingHintLookup()
	assert.Equal(t, "<meta-n>", lookup["windownew"])
	assert.Equal(t, "<alt-shift-e>", lookup["lsp diagnostics"])
	assert.Empty(t, lookup["tabclose"])
	assert.Empty(t, c.errors)
}

func TestCommandKeyBindingHintLookupPrefersPrintableAlias(t *testing.T) {
	c := hintConfig(map[string]any{
		"<c-a-m-left>": "windowresize decrease width",
		"<c-a-m-j>":    "windowresize decrease width",
	}, nil)

	for range 20 {
		lookup := c.commandKeyBindingHintLookup()
		assert.Equal(t, "<ctrl-alt-meta-j>", lookup["windowresize decrease width"])
	}
	assert.Empty(t, c.errors)
}

func TestCommandKeyBindingHintLookupEchoTopLevel(t *testing.T) {
	c := hintConfig(map[string]any{
		"<m-t>": "echo {prompt}edit<space>",
	}, nil)
	lookup := c.commandKeyBindingHintLookup()
	assert.Equal(t, "<meta-t>", lookup["edit"])
	assert.Empty(t, lookup["edit hello.go"])
	assert.Empty(t, c.errors)
}

func TestCommandKeyBindingHintLookupDirectWinsOverEcho(t *testing.T) {
	c := hintConfig(map[string]any{
		"<a-t>":   "lsp hover",
		"<a-s-t>": "echo {prompt}lsp<space>hover<space>",
	}, nil)
	lookup := c.commandKeyBindingHintLookup()
	assert.Equal(t, "<alt-t>", lookup["lsp hover"])
	// the multi-word echo body must not register a bare `lsp` hint
	assert.Empty(t, lookup["lsp"])
	assert.Empty(t, c.errors)
}

func TestEchoPromptSingleCommand(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{"single word", "{prompt}edit<space>", "edit", true},
		{"multi word", "{prompt}lsp<space>hover<space>", "", false},
		{"no trailing space", "{prompt}edit", "", false},
		{"no prompt instruction", "windowconverttab<space>", "", false},
		{"empty word", "{prompt}<space>", "", false},
		{"trailing key after space", "{prompt}edit<space>x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := echoPromptSingleCommand(tc.body)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCommandPromptSeparatorCharsetOverrides(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "command": {
        "separator_charset": {
            "left":             "🭼",
            "horizontal_left":  "▁",
            "horizontal_right": "▁",
            "right":            "🭿",
        },
    },
}
`)
	got := c.commandPromptSeparatorCharset()
	assert.Equal(t, '🭼', got.Left)
	assert.Equal(t, '▁', got.HorizontalLeft)
	assert.Equal(t, '▁', got.HorizontalRight)
	assert.Equal(t, '🭿', got.Right)
	assert.Empty(t, c.errors)
}

func TestCommandPromptSeparatorCharsetPartialOverride(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "command": {
        "separator_charset": {
            "left":  "╠",
            "right": "╣",
        },
    },
}
`)
	got := c.commandPromptSeparatorCharset()
	def := defaultCommandPromptSeparatorCharset()
	assert.Equal(t, '╠', got.Left)
	assert.Equal(t, def.HorizontalLeft, got.HorizontalLeft)
	assert.Equal(t, def.HorizontalRight, got.HorizontalRight)
	assert.Equal(t, '╣', got.Right)
	assert.Empty(t, c.errors)
}

func TestCommandPromptSeparatorCharsetWrongType(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "command": {
        "separator_charset": {
            "left": ["nope"],
        },
    },
}
`)
	got := c.commandPromptSeparatorCharset()
	def := defaultCommandPromptSeparatorCharset()
	assert.Equal(t, def.Left, got.Left,
		"wrong type must keep default Left glyph")
	require.Contains(t, c.errors, "command.separator_charset.left")
}

func TestCommandPromptCfgComposes(t *testing.T) {
	c := newAnimConfig(t, `
config = {
    "animations": {"command_prompt": False},
    "command": {
        "separator_charset": {
            "left":  "╠",
            "right": "╣",
        },
    },
}
`)
	got := c.commandPromptCfg()
	assert.False(t, got.shader.enabled)
	assert.Equal(t, '╠', got.separator.Left)
	assert.Equal(t, '╣', got.separator.Right)
	assert.Empty(t, c.errors)
}
