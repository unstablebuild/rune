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

package text

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/template"
)

func TestStatusBarLayout(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantComps  []StatusBarComponent
		wantErr    bool
		wantErrSub string // substring to look for in error
	}{
		{
			name:  "single component - language",
			input: `{{ .Language }}`,
			wantComps: []StatusBarComponent{
				{Type: StatusBarLanguage, Template: "%s"},
			},
		},
		{
			name:  "prefix before component",
			input: `⚑{{ .Status }}`,
			wantComps: []StatusBarComponent{
				{Type: StatusBarStatus, Template: "⚑%s"},
			},
		},
		{
			name:  "multiple components with separator",
			input: `{{ .GitDiffAdd }}+{{ .GitDiffDel }}`,
			wantComps: []StatusBarComponent{
				{Type: StatusBarGitDiffAdded, Template: "%d+"},
				{Type: StatusBarGitDiffDeleted, Template: "%d"},
			},
		},
		{
			name:  "trailing padding appended to last component",
			input: `{{ .Language }}  `,
			wantComps: []StatusBarComponent{
				{Type: StatusBarLanguage, Template: "%s  "},
			},
		},
		{
			name:  "prefix/mid/trailing is parsed into templates",
			input: `pre {{ .CursorColumn }} mid {{ .CursorLine }} post`,
			wantComps: []StatusBarComponent{
				{Type: StatusBarCoordinatesCursorX, Template: "pre %d mid "},
				// second receives " mid " during parsing, plus trailing " post" appended after loop
				{Type: StatusBarCoordinatesCursorY, Template: "%d post"},
			},
		},
		{
			name:       "unknown component name returns error",
			input:      `{{ .IAmNoComponent }}`,
			wantErr:    true,
			wantErrSub: "unknown status bar component",
		},
		{
			name:       "unsupported action (function call) returns error",
			input:      `{{ printf "%s" "x" }}`,
			wantErr:    true,
			wantErrSub: "template: status_bar.layout:1: function \"printf\" not defined",
		},
		{
			name:       "unsupported node type (if) returns error",
			input:      `{{ if true }}{{ .Language }}{{ end }}`,
			wantErr:    true,
			wantErrSub: "unsupported node type",
		},
		{
			name:  "fg color - quoted",
			input: `{{ .Status | fg "red" }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarStatus,
					Template:   "%s",
					Attributes: term.Attributes{Fg: term.ColorRed},
				},
			},
		},
		{
			name:  "bg color - quoted",
			input: `{{ .Status | bg "blue" }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarStatus,
					Template:   "%s",
					Attributes: term.Attributes{Bg: term.ColorBlue},
				},
			},
		},
		{
			name:  "fg and bg together",
			input: `{{ .Status | fg "black" | bg "red" }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarStatus,
					Template:   "%s",
					Attributes: term.Attributes{Fg: term.ColorBlack, Bg: term.ColorRed},
				},
			},
		},
		{
			name:  "bold style",
			input: `{{ .Language | bold }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarLanguage,
					Template:   "%s",
					Attributes: term.Attributes{Attrs: term.AttrBold},
				},
			},
		},
		{
			name:  "italic style",
			input: `{{ .Filepath | italic }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarFilePath,
					Template:   "%s",
					Attributes: term.Attributes{Attrs: term.AttrItalic},
				},
			},
		},
		{
			name:  "multiple style attributes combined",
			input: `{{ .Status | bold | underline }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarStatus,
					Template:   "%s",
					Attributes: term.Attributes{Attrs: term.AttrBold | term.AttrUnderline},
				},
			},
		},
		{
			name:  "full styling - colors and attributes",
			input: `{{ .Status | bg "red" | fg "black" | bold }}`,
			wantComps: []StatusBarComponent{
				{
					Type:     StatusBarStatus,
					Template: "%s",
					Attributes: term.Attributes{
						Bg:    term.ColorRed,
						Fg:    term.ColorBlack,
						Attrs: term.AttrBold,
					},
				},
			},
		},
		{
			name:  "hex color - with hash",
			input: `{{ .Status | fg "#ff5500" }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarStatus,
					Template:   "%s",
					Attributes: term.Attributes{Fg: term.NewRGBColor(0xff, 0x55, 0x00)},
				},
			},
		},
		{
			name:       "hex color - without hash is an error",
			input:      `{{ .Status | bg "00ff00" }}`,
			wantErr:    true,
			wantErrSub: "error is not a hex color starting with #, nor a known named W3C color in lowercase",
		},
		{
			name:  "multiple components with different attributes",
			input: `{{ .GitDiffAdd | fg "green" }}  {{ .GitDiffDel | fg "red" }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarGitDiffAdded,
					Template:   "%d",
					Attributes: term.Attributes{Fg: term.ColorGreen},
				},
				{
					Type:       StatusBarGitDiffDeleted,
					Template:   "  %d",
					Attributes: term.Attributes{Fg: term.ColorRed},
				},
			},
		},
		{
			name:  "blank lines should be grouped with the succeeding compoent, before shift right",
			input: `█{{ .Status | bg "red" | bold }}█▓▒░  {{ .GitDiffDel }}`,
			wantComps: []StatusBarComponent{
				{
					Type:     StatusBarStatus,
					Template: "█%s█▓▒░",
					Attributes: term.Attributes{
						Bg:    term.ColorRed,
						Attrs: term.AttrBold,
					},
				},
				{
					Type:       StatusBarGitDiffDeleted,
					Template:   "  %d",
					Attributes: term.Attributes{},
				},
			},
		},
		{
			name:  "components are grouped by double blank space",
			input: `█{{ .Status }}█▓▒░  {{ .TotalLines | bg "red" }} lines  {{ .GitDiffDel }} {{ .GitDiffAdd }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarStatus,
					Template:   "█%s█▓▒░",
					Attributes: term.Attributes{},
				},
				{
					Type:     StatusBarTotalLines,
					Template: "  %d lines",
					Attributes: term.Attributes{
						Bg: term.ColorRed,
					},
				},
				{
					Type:       StatusBarGitDiffDeleted,
					Template:   "  %d ",
					Attributes: term.Attributes{},
				},
				{
					Type:       StatusBarGitDiffAdded,
					Template:   "%d",
					Attributes: term.Attributes{},
				},
			},
		},
		{
			name:  "all style attributes",
			input: `{{ .Status | bold | italic | underline | dim | reverse | strikethrough | blink }}`,
			wantComps: []StatusBarComponent{
				{
					Type:     StatusBarStatus,
					Template: "%s",
					Attributes: term.Attributes{
						Attrs: term.AttrBold | term.AttrItalic | term.AttrUnderline |
							term.AttrDim | term.AttrReverse | term.AttrStrikeThrough | term.AttrBlink,
					},
				},
			},
		},
		{
			name:  "void component with attributes ignored",
			input: `{{ .ShiftRight }}`,
			wantComps: []StatusBarComponent{
				{Type: StatusBarVoid, Template: ""},
			},
		},
		{
			name:  "default color",
			input: `{{ .Status | fg "default" }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarStatus,
					Template:   "%s",
					Attributes: term.Attributes{Fg: term.ColorDefault},
				},
			},
		},
		{
			name:  "image with its literals and directives",
			input: `{{ .Image | src "file:///tmp/logo.png" | width 3 | z_index -1 | bg "navy" }} {{ .Status }}`,
			wantComps: []StatusBarComponent{
				{
					Type:       StatusBarImage,
					Template:   "%s ",
					Attributes: term.Attributes{Bg: term.ColorNavy},
					Image: template.Image{
						URI:     "file:///tmp/logo.png",
						Width:   template.Length{N: 3},
						Height:  1,
						Reserve: 3,
						Fit:     term.ImageFitContain,
						ZIndex:  -1,
						Alt:     template.DefaultImageAlt,
						TTL:     24 * time.Hour,
					},
				},
				{Type: StatusBarStatus, Template: "%s"},
			},
		},
		{
			name:       "image without src",
			input:      `{{ .Image | width 3 }}`,
			wantErr:    true,
			wantErrSub: ".Image requires src",
		},
		{
			name:       "image directive on another component",
			input:      `{{ .Filepath | src "file:///tmp/logo.png" }}`,
			wantErr:    true,
			wantErrSub: "src only applies to .Image, not .Filepath",
		},
		{
			name:       "image layer is not a directive",
			input:      `{{ .Image | src "file:///tmp/logo.png" | layer "below" }}`,
			wantErr:    true,
			wantErrSub: `function "layer" not defined`,
		},
		// Error cases
		{
			name:       "unknown color name",
			input:      `{{ .Status | fg "belindings" }}`,
			wantErr:    true,
			wantErrSub: "error is not a hex color starting with #, nor a known named W3C color in lowercase",
		},
		{
			name:       "fg missing color argument",
			input:      `{{ .Status | fg }}`,
			wantErr:    true,
			wantErrSub: "requires a color argument",
		},
		{
			name:       "bg missing color argument",
			input:      `{{ .Status | bg }}`,
			wantErr:    true,
			wantErrSub: "requires a color argument",
		},
		{
			name:       "unknown attribute command",
			input:      `{{ .Status | sparkle }}`,
			wantErr:    true,
			wantErrSub: "template: status_bar.layout:1: function \"sparkle\" not defined",
		},
		{
			name:       "invalid hex color - too short",
			input:      `{{ .Status | fg "#fff" }}`,
			wantErr:    true,
			wantErrSub: "error is not a hex color starting with #, nor a known named W3C color in lowercase",
		},
		{
			name:       "invalid hex color - bad characters",
			input:      `{{ .Status | fg "#gggggg" }}`,
			wantErr:    true,
			wantErrSub: "error is not a hex color starting with #, nor a known named W3C color in lowercase",
		},
		{
			name:  "blank lines should be grouped with the preceding component, after shiftright",
			input: `{{ .ShiftRight }}{{ .CursorColumn }}:{{ .CursorLine }}  {{ .TotalLines }} lines   {{ .Language | bg "navy" | bold }} `,
			wantComps: []StatusBarComponent{
				{
					Type:     StatusBarVoid,
					Template: "",
				},
				{
					Type:     StatusBarCoordinatesCursorX,
					Template: "%d:",
				},
				{
					Type:       StatusBarCoordinatesCursorY,
					Template:   "%d  ",
					Attributes: term.Attributes{Fg: term.ColorDefault},
				},
				{
					Type:       StatusBarTotalLines,
					Template:   "%d lines  ",
					Attributes: term.Attributes{Fg: term.ColorDefault},
				},
				{
					Type:       StatusBarLanguage,
					Template:   " %s ",
					Attributes: term.Attributes{Bg: term.ColorNavy, Attrs: term.AttrBold},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStatusBarLayout(tt.input)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrSub != "" {
					assert.Contains(t, err.Error(), tt.wantErrSub)
				}
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantComps, got)
		})
	}
}
