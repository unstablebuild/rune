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
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestCommandPromptSeparatorCharsetDefaults(t *testing.T) {
	c := newAnimConfig(t, `config = {}`)
	got := c.commandPromptCfg().separator
	assert.Equal(t, defaultCommandPromptSeparatorCharset(), got)
	assert.Empty(t, c.errors)
}

func TestCommandPromptSeparatorCharsetGraphics(t *testing.T) {
	for _, tc := range []struct {
		name, command       string
		horizontal          rune
		left, image, right  string
		overlay             bool
		top, bottom, inward int
		errors              []string
	}{
		{name: "window inheritance", command: `{}`, horizontal: '=', image: "file:///window.png", overlay: true},
		{name: "command frame inheritance", command: `{"frame_charset": {"horizontalbottom": "~", "graphics": {"horizontalbottom": "file:///command.png", "mode": "replace"}}}`, horizontal: '~', image: "file:///command.png"},
		{name: "partial command frame", command: `{"frame_charset": {"horizontalbottom": "~"}}`, horizontal: '~', image: "file:///window.png", overlay: true},
		{name: "clear inherited image", command: `{"separator_charset": {"graphics": {"horizontal": ""}}}`, horizontal: '=', overlay: true},
		{name: "three images", command: `{"separator_charset": {"graphics": {"left": "file:///left.png", "horizontal": "file:///middle.png", "right": "file:///right.png"}}}`, horizontal: '=', left: "file:///left.png", image: "file:///middle.png", right: "file:///right.png", overlay: true},
		{name: "partial images", command: `{"separator_charset": {"graphics": {"right": "file:///right.png"}}}`, horizontal: '=', image: "file:///window.png", right: "file:///right.png", overlay: true},
		{name: "replace", command: `{"separator_charset": {"graphics": {"mode": "replace"}}}`, horizontal: '=', image: "file:///window.png"},
		{name: "overlay override", command: `{"frame_charset": {"graphics": {"mode": "replace"}}, "separator_charset": {"graphics": {"mode": "overlay"}}}`, horizontal: '=', image: "file:///window.png", overlay: true},
		{name: "invalid image is atomic", command: `{"separator_charset": {"graphics": {"left": "file:///left.png", "horizontal": 12, "mode": "replace"}}}`, horizontal: '=', image: "file:///window.png", overlay: true, errors: []string{"command.separator_charset"}},
		{name: "invalid mode is atomic", command: `{"separator_charset": {"graphics": {"left": "file:///left.png", "mode": "blend"}}}`, horizontal: '=', image: "file:///window.png", overlay: true, errors: []string{"command.separator_charset"}},
		{name: "invalid graphics table", command: `{"separator_charset": {"graphics": 12}}`, horizontal: '=', image: "file:///window.png", overlay: true, errors: []string{"command.separator_charset"}},
		{name: "outsets", command: `{"separator_charset": {"graphics": {"outset": {"top": 1, "bottom": 2, "inward": 3}}}}`, horizontal: '=', image: "file:///window.png", overlay: true, top: 1, bottom: 2, inward: 3},
		{name: "invalid outsets preserve valid sides", command: `{"separator_charset": {"graphics": {"outset": {"top": -1, "bottom": "two", "inward": 3}}}}`, horizontal: '=', image: "file:///window.png", overlay: true, inward: 3, errors: []string{"command.separator_charset.graphics.outset.top", "command.separator_charset.graphics.outset.bottom"}},
		{name: "invalid outset table", command: `{"separator_charset": {"graphics": {"outset": 1}}}`, horizontal: '=', image: "file:///window.png", overlay: true, errors: []string{"command.separator_charset.graphics.outset"}},
		{name: "invalid separator table", command: `{"separator_charset": 1}`, horizontal: '=', image: "file:///window.png", overlay: true, errors: []string{"command.separator_charset"}},
		{name: "invalid frame falls back", command: `{"frame_charset": {"graphics": {"mode": "blend"}}}`, horizontal: '=', image: "file:///window.png", overlay: true, errors: []string{"command.frame_charset"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newAnimConfig(t, `config = {
    "browser": {"window_manager": {"frame_charset": {
        "horizontalbottom": "=",
        "graphics": {"horizontalbottom": "file:///window.png", "bottomleft": "file:///corner.png", "verticalright": "file:///side.png", "mode": "overlay"},
    }}},
    "command": `+tc.command+`,
}`)
			got := c.commandPromptCfg().separator
			assert.Equal(t, tc.horizontal, got.HorizontalLeft)
			assert.Equal(t, tc.horizontal, got.HorizontalRight)
			assert.Equal(t, tc.left, got.Graphics.Left)
			assert.Equal(t, tc.image, got.Graphics.Horizontal)
			assert.Equal(t, tc.right, got.Graphics.Right)
			assert.Equal(t, tc.overlay, got.Graphics.Overlay)
			assert.Equal(t, tc.top, got.Graphics.Outset.Top)
			assert.Equal(t, tc.bottom, got.Graphics.Outset.Bottom)
			assert.Equal(t, tc.inward, got.Graphics.Outset.Inward)
			assert.Len(t, c.errors, len(tc.errors))
			for _, key := range tc.errors {
				assert.Contains(t, c.errors, key)
			}
		})
	}
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
	got := c.commandPromptCfg().separator
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
	got := c.commandPromptCfg().separator
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
	got := c.commandPromptCfg().separator
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

func TestCommandPromptFrameCharset(t *testing.T) {
	window := component.FrameCharSetDefault()
	window.Graphics = component.FrameCharSetGraphics{TopRight: "file:///window/tr.png"}
	channel := window
	channel.TopLeft, channel.VerticalLeft = '\U00100008', '\U0010000E'
	channel.Graphics.TopLeft = "file:///prompt/tl.png"
	channel.Graphics.Overlay = true

	suite := []struct {
		description string
		command     string
		want        *component.FrameCharSet
		wantErr     bool
	}{
		{
			description: "unset leaves the prompt to the window charsets",
			command:     `{}`,
		},
		{
			description: "missing slots keep the window frame charset's",
			command: `{"frame_charset": {
                "topleft": "\U00100008",
                "verticalleft": "\U0010000E",
                "graphics": {"mode": "overlay", "topleft": "file:///prompt/tl.png"},
            }}`,
			want: &channel,
		},
		{
			description: "an invalid charset is reported and left unset",
			command:     `{"frame_charset": {"graphics": {"mode": "blend"}}}`,
			wantErr:     true,
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			c := newAnimConfig(t, `
config = {
    "browser": {"window_manager": {"frame_charset": {
        "graphics": {"topright": "file:///window/tr.png"},
    }}},
    "command": `+test.command+`,
}
`)
			got := c.commandPromptCfg().frameCharSet
			assert.Equal(t, test.want, got)
			if test.wantErr {
				assert.Contains(t, c.errors, "command.frame_charset")
			} else {
				assert.Empty(t, c.errors)
			}
		})
	}
}
