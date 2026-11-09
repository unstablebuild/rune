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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestPinnedMessageLayout(t *testing.T) {
	const width = 12
	suite := []struct {
		desc     string
		text     string
		maxRows  int
		wantRows []string
	}{
		{
			desc:     "a short message keeps its one row",
			text:     "hello",
			maxRows:  3,
			wantRows: []string{"hello"},
		},
		{
			desc:     "line breaks are flattened",
			text:     "one\n\n  two\nthree",
			maxRows:  3,
			wantRows: []string{"one two", "three"},
		},
		{
			desc:     "a long message is cut short with an ellipsis",
			text:     "alpha beta gamma delta epsilon zeta eta theta",
			maxRows:  2,
			wantRows: []string{"alpha beta", "gamma delta…"},
		},
		{
			desc:     "a single row fits the ellipsis in",
			text:     "alpha beta gamma",
			maxRows:  1,
			wantRows: []string{"alpha beta…"},
		},
		{
			desc:    "no rows draws nothing",
			text:    "hello",
			maxRows: 0,
		},
	}
	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			p := pinnedMessage{strCfg: component.StringResponsiveConfig{NoSplitWords: true}}
			rows := p.layout(tc.text, width, tc.maxRows)
			require.Equal(t, len(tc.wantRows), rows)
			if rows == 0 {
				return
			}
			w := term.NewStringWriter(width, rows)
			p.draw(w)
			require.NoError(t, w.Flush())
			got := strings.Split(w.String(), "\n")
			for i := range got {
				got[i] = strings.TrimRight(got[i], " ")
			}
			assert.Equal(t, tc.wantRows, got[:rows])
		})
	}
}
