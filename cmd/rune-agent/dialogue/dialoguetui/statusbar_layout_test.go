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

package dialoguetui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestParseStatusBarLayoutFields(t *testing.T) {
	tests := []struct {
		field string
		want  StatusBarComponentType
		tmpl  string
	}{
		{"Spinner", StatusBarSpinner, "%s"},
		{"Status", StatusBarStatus, "%s"},
		{"Elapsed", StatusBarElapsed, "%s"},
		{"Model", StatusBarModel, "%s"},
		{"Provider", StatusBarProvider, "%s"},
		{"Effort", StatusBarEffort, "%s"},
		{"MaxTokens", StatusBarMaxTokens, "%s"},
		{"TokensSent", StatusBarTokensSent, "%s"},
		{"TokensReceived", StatusBarTokensReceived, "%s"},
		{"ContextTokens", StatusBarContextTokens, "%s"},
		{"ContextWindow", StatusBarContextWindow, "%s"},
		{"ContextPct", StatusBarContextPct, "%s"},
		{"Cache", StatusBarCache, "%s"},
		{"CachePct", StatusBarCachePct, "%s"},
		{"ContextGauge", StatusBarContextGauge, "%s"},
		{"CacheGauge", StatusBarCacheGauge, "%s"},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			got, err := ParseStatusBarLayout("{{ ." + tt.field + " }}")
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tt.want, got[0].Type)
			assert.Equal(t, tt.tmpl, got[0].Template)
		})
	}
}

func TestParseStatusBarLayout(t *testing.T) {
	tests := []struct {
		name   string
		layout string
		want   []StatusBarComponent
		err    string
	}{
		{
			name:   "shift right pivot",
			layout: `{{ .Status }}{{ .ShiftRight }}{{ .Model }}`,
			want: []StatusBarComponent{
				{Type: StatusBarStatus, Template: "%s"},
				{Type: StatusBarVoid},
				{Type: StatusBarModel, Template: "%s"},
			},
		},
		{
			// The pivot is not an element, so a literal behind it has
			// nothing to hang off and must prefix what follows. It
			// used to be appended to the pivot and never drawn.
			name:   "literal after the pivot prefixes the next element",
			layout: `{{ .Status }}{{ .ShiftRight }}cache: {{ .Model }}`,
			want: []StatusBarComponent{
				{Type: StatusBarStatus, Template: "%s"},
				{Type: StatusBarVoid},
				{Type: StatusBarModel, Template: "cache: %s"},
			},
		},
		{
			name:   "single space joins neighbours",
			layout: `{{ .Status }} {{ .Model }}`,
			want: []StatusBarComponent{
				{Type: StatusBarStatus, Template: "%s "},
				{Type: StatusBarModel, Template: "%s"},
			},
		},
		{
			name:   "double space splits into prefix of next",
			layout: `{{ .Status }}  {{ .Model }}`,
			want: []StatusBarComponent{
				{Type: StatusBarStatus, Template: "%s"},
				{Type: StatusBarModel, Template: "  %s"},
			},
		},
		{
			name:   "double space after pivot stays a suffix",
			layout: `{{ .ShiftRight }}{{ .Model }}  {{ .Effort }}`,
			want: []StatusBarComponent{
				{Type: StatusBarVoid},
				{Type: StatusBarModel, Template: "%s  "},
				{Type: StatusBarEffort, Template: "%s"},
			},
		},
		{
			name:   "trailing text appended to last element",
			layout: `{{ .Status }} | `,
			want: []StatusBarComponent{
				{Type: StatusBarStatus, Template: "%s | "},
			},
		},
		{
			name:   "pipe operators",
			layout: `{{ .Status | fg "red" | bg "blue" | bold }}`,
			want: []StatusBarComponent{
				{
					Type:     StatusBarStatus,
					Template: "%s",
					Attributes: term.Attributes{
						Fg:    term.ColorRed,
						Bg:    term.ColorBlue,
						Attrs: term.AttrBold,
					},
				},
			},
		},
		{
			name:   "unknown field",
			layout: `{{ .Nonsense }}`,
			err:    `unknown status bar component: "Nonsense"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStatusBarLayout(tt.layout)
			if tt.err != "" {
				assert.EqualError(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseStatusBarLayoutDefault(t *testing.T) {
	got, err := ParseStatusBarLayout(DefaultStatusBarLayout)
	require.NoError(t, err)
	types := make([]StatusBarComponentType, len(got))
	for i, c := range got {
		types[i] = c.Type
	}
	assert.Equal(t, []StatusBarComponentType{
		StatusBarSpinner, StatusBarStatus, StatusBarModel,
		StatusBarEffort, StatusBarVoid, StatusBarCache,
		StatusBarElapsed, StatusBarConversation, StatusBarContextGauge,
	}, types)

	// The shade glyphs fading the status pill into the bar must stay
	// attached to the status element.
	assert.Equal(t, "%s █▓▒░", got[1].Template)

	// The model and the effort need visible separation, and a gauge
	// keeps the padding the layout puts around it.
	assert.Equal(t, "  %s", got[2].Template, "model")
	assert.Equal(t, "  %s", got[3].Template, "effort")
	assert.Equal(t, "󰗂 %s  ", got[5].Template, "cache")
	assert.Equal(t, "%s  ", got[6].Template, "elapsed")
	assert.Equal(t, "%s ", got[8].Template, "context gauge")

	assert.Equal(t, "%s  ", got[7].Template, "conversation")
}
