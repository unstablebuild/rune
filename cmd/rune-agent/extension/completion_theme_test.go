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

package extension

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"gopkg.in/yaml.v3"
)

const (
	runeStarPath  = "../../rune/rune.star"
	agentYAMLPath = "../config.yaml"
)

var (
	starAttrRe = regexp.MustCompile(
		`"(\w+)":\s*attr\(([^)]*)\)`)
	starFieldRe = regexp.MustCompile(
		`(\w+)\s*=\s*(?:"([^"]*)"|\[([^\]]*)\])`)
)

// parseStarAttrs extracts the attr(...) literals declared in the given
// slice of rune.star source.
func parseStarAttrs(t *testing.T, src string) map[string]term.Attributes {
	t.Helper()
	out := make(map[string]term.Attributes)
	for _, m := range starAttrRe.FindAllStringSubmatch(src, -1) {
		var attr term.Attributes
		for _, f := range starFieldRe.FindAllStringSubmatch(m[2], -1) {
			switch f[1] {
			case "fg":
				attr.Fg = starColor(f[2])
			case "bg":
				attr.Bg = starColor(f[2])
			case "flags":
				for flag := range strings.SplitSeq(f[3], ",") {
					attr.Attrs |= starFlag(t, strings.Trim(
						strings.TrimSpace(flag), `"`))
				}
			}
		}
		out[m[1]] = attr
	}
	return out
}

func starColor(name string) term.Color {
	if name == "default" || name == "" {
		return term.ColorDefault
	}
	return term.GetColor(name)
}

func starFlag(t *testing.T, name string) term.AttrMask {
	t.Helper()
	switch name {
	case "":
		return 0
	case "bold":
		return term.AttrBold
	case "dim":
		return term.AttrDim
	case "italic":
		return term.AttrItalic
	case "underline":
		return term.AttrUnderline
	case "reverse":
		return term.AttrReverse
	}
	t.Fatalf("unhandled rune.star attr flag %q; extend starFlag", name)
	return 0
}

// section returns the rune.star source between the start anchor and the
// following end anchor, failing when either anchor moves.
func section(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	require.GreaterOrEqual(t, i, 0,
		"anchor %q missing from rune.star; update this test", start)
	rest := src[i+len(start):]
	j := strings.Index(rest, end)
	require.GreaterOrEqual(t, j, 0,
		"anchor %q missing from rune.star; update this test", end)
	return rest[:j]
}

// promptTheme is the command prompt styling declared in rune.star. The
// radar sweep is intentionally not part of it: the completion overlay
// uses a neutral color instead of the command prompt's hue.
type promptTheme struct {
	matched, focus, element term.Attributes
}

func loadPromptTheme(t *testing.T) promptTheme {
	t.Helper()
	raw, err := os.ReadFile(runeStarPath)
	require.NoError(t, err)
	src := string(raw)

	attrs := parseStarAttrs(t,
		section(t, src, "# Prompt colors.", `"separator_charset"`))
	for _, k := range []string{"matched_text_attr", "focus_element_attr", "element_attr"} {
		require.Contains(t, attrs, k, "rune.star command prompt is missing %q", k)
	}

	return promptTheme{
		matched: attrs["matched_text_attr"],
		focus:   attrs["focus_element_attr"],
		element: attrs["element_attr"],
	}
}

func TestCompletionThemeDefaultsMatchCommandPrompt(t *testing.T) {
	want := loadPromptTheme(t)

	assert.Equal(t, want.matched, defaultComponentCfg.CompletionMatchedTextAttr)
	assert.Equal(t, want.focus, defaultComponentCfg.CompletionFocusElementAttr)
	assert.Equal(t, want.element, defaultComponentCfg.CompletionElementAttr)
}

func TestShippedAgentConfigMatchesCommandPrompt(t *testing.T) {
	want := loadPromptTheme(t)

	raw, err := os.ReadFile(agentYAMLPath)
	require.NoError(t, err)
	var doc struct {
		Extensions struct {
			RuneAgent struct {
				Config map[string]any `yaml:"config"`
			} `yaml:"rune-agent"`
		} `yaml:"extensions"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	pconfig := config.MapConfig(doc.Extensions.RuneAgent.Config)
	require.NotEmpty(t, doc.Extensions.RuneAgent.Config,
		"extensions.rune-agent.config missing from %s", agentYAMLPath)

	for _, tc := range []struct {
		key  string
		want term.Attributes
	}{
		{"completion_matched_text_attr", want.matched},
		{"completion_focus_element_attr", want.focus},
		{"completion_element_attr", want.element},
		{"inline_attachment_attr", defaultComponentCfg.InlineAttachmentAttr},
	} {
		got, err := config.GetAttributes(pconfig, tc.key)
		require.NoError(t, err, "reading %q from %s", tc.key, agentYAMLPath)
		assert.Equal(t, tc.want, got, "%s in %s", tc.key, agentYAMLPath)
	}

	// The radar tracks defaultComponentCfg rather than rune.star, but the
	// two files still have to agree: config.yaml ships next to the binary
	// and overrides the Go default at runtime.
	radar, err := pconfig.GetColor("completion_radar_color")
	require.NoError(t, err)
	assert.Equal(t, defaultComponentCfg.CompletionRadarColor, radar,
		"completion_radar_color in %s", agentYAMLPath)
}
