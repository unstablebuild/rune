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
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func newTestSelection(multiSelect bool) *Selection {
	return NewSelection(
		"Pick a database", "Database",
		[]SelectionOption{
			{Label: "PostgreSQL", Description: "Mature RDBMS"},
			{Label: "SQLite", Description: "Lightweight"},
			{Label: "MySQL"},
		},
		multiSelect,
		SelectionConfig{},
	)
}

func TestSelectionHeight(t *testing.T) {
	sel := newTestSelection(false)
	// title (1) + blank (1) + 3 options (label + desc, label + desc, label) = 1+1+2+2+1 = 7
	assert.Equal(t, 7, sel.Height(30))
}

func TestSelectionHeightZeroWidth(t *testing.T) {
	sel := newTestSelection(false)
	assert.Equal(t, 0, sel.Height(0))
}

func TestSelectionRender(t *testing.T) {
	sel := newTestSelection(false)
	w := term.NewStringWriter(30, 7)

	comptest.TestComponent(t, sel, w, []comptest.TestCase{
		{
			Action: func() {
				sel.Resize(30, 7)
			},
			Expected: "Pick a database     [Database]" +
				"\n                              " +
				"\n> PostgreSQL                  " +
				"\n      Mature RDBMS            " +
				"\n  SQLite                      " +
				"\n      Lightweight             " +
				"\n  MySQL                       ",
		},
	})
}

func TestSelectionCursorMoveDown(t *testing.T) {
	sel := newTestSelection(false)
	sel.Resize(30, 7)

	sel.MoveDown()
	assert.Equal(t, []string{"SQLite"}, sel.Selected())

	sel.MoveDown()
	assert.Equal(t, []string{"MySQL"}, sel.Selected())

	// Wrap around
	sel.MoveDown()
	assert.Equal(t, []string{"PostgreSQL"}, sel.Selected())
}

func TestSelectionCursorMoveUp(t *testing.T) {
	sel := newTestSelection(false)
	sel.Resize(30, 7)

	// Wrap from top to bottom
	sel.MoveUp()
	assert.Equal(t, []string{"MySQL"}, sel.Selected())

	sel.MoveUp()
	assert.Equal(t, []string{"SQLite"}, sel.Selected())
}

func TestSelectionSelectedSingleSelect(t *testing.T) {
	sel := newTestSelection(false)
	require.Equal(t, []string{"PostgreSQL"}, sel.Selected())

	sel.MoveDown()
	require.Equal(t, []string{"SQLite"}, sel.Selected())
}

func TestSelectionMultiSelectToggle(t *testing.T) {
	sel := newTestSelection(true)

	// Initially nothing checked
	assert.Empty(t, sel.Selected())

	// Toggle first item
	sel.Toggle()
	assert.Equal(t, []string{"PostgreSQL"}, sel.Selected())

	// Move down and toggle second
	sel.MoveDown()
	sel.Toggle()
	assert.Equal(t, []string{"PostgreSQL", "SQLite"}, sel.Selected())

	// Untoggle first
	sel.MoveUp()
	sel.Toggle()
	assert.Equal(t, []string{"SQLite"}, sel.Selected())
}

func TestSelectionCursorLabelMultiSelect(t *testing.T) {
	sel := newTestSelection(true)

	// Cursor starts on PostgreSQL; nothing checked.
	assert.Equal(t, "PostgreSQL", sel.CursorLabel())

	// Check PostgreSQL, then move to SQLite without checking it.
	// CursorLabel must follow the cursor, not the checked set.
	sel.Toggle()
	sel.MoveDown()
	assert.Equal(t, "SQLite", sel.CursorLabel())
	assert.Equal(t, []string{"PostgreSQL"}, sel.Selected())

	// Move to the last, unchecked option.
	sel.MoveDown()
	assert.Equal(t, "MySQL", sel.CursorLabel())
}

func TestSelectionMultiSelectRender(t *testing.T) {
	sel := newTestSelection(true)
	sel.Toggle() // check PostgreSQL
	w := term.NewStringWriter(30, 7)

	comptest.TestComponent(t, sel, w, []comptest.TestCase{
		{
			Action: func() {
				sel.Resize(30, 7)
			},
			Expected: "Pick a database     [Database]" +
				"\n                              " +
				"\n>[x] PostgreSQL               " +
				"\n      Mature RDBMS            " +
				"\n[ ] SQLite                    " +
				"\n      Lightweight             " +
				"\n[ ] MySQL                     ",
		},
	})
}

func TestSelectionDescriptionWraps(t *testing.T) {
	sel := NewSelection(
		"Choose", "",
		[]SelectionOption{
			{Label: "Option A", Description: "This is a really long description text"},
		},
		false,
		SelectionConfig{},
	)
	// width=20, indent=6, usable=14. 37 chars -> ceil(37/14) = 3 lines for desc
	// total: title(1) + blank(1) + label(1) + desc(3) = 6
	assert.Equal(t, 6, sel.Height(20))
}

func TestSelectionToggleNoOpForSingleSelect(t *testing.T) {
	sel := newTestSelection(false)
	sel.Toggle() // should be a no-op
	assert.Equal(t, []string{"PostgreSQL"}, sel.Selected())
}

func TestSelectionTitleWrapsHeight(t *testing.T) {
	sel := NewSelection(
		"What is your favorite color?", "", // 28 chars, no chip
		[]SelectionOption{
			{Label: "Red"},
			{Label: "Blue"},
		},
		false,
		SelectionConfig{},
	)
	// width=20: 28 chars wraps to 2 lines
	// title(2) + blank(1) + 2 options = 5
	assert.Equal(t, 5, sel.Height(20))
}

func TestSelectionTitleWrapsRender(t *testing.T) {
	sel := NewSelection(
		"What is your favorite color?", "Colors",
		[]SelectionOption{
			{Label: "Red"},
			{Label: "Blue"},
		},
		false,
		SelectionConfig{},
	)
	// width=20: title wraps to 2 lines, chip "[Colors]" can't fit on first line
	w := term.NewStringWriter(20, 5)

	comptest.TestComponent(t, sel, w, []comptest.TestCase{
		{
			Action: func() {
				sel.Resize(20, 5)
			},
			Expected: "What is your favorit" +
				"\ne color?            " +
				"\n                    " +
				"\n> Red               " +
				"\n  Blue              ",
		},
	})
}

func TestDrawText(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		x         int
		maxWidth  int
		wantCells map[int]term.Cell
	}{
		{
			name:     "ascii",
			text:     "ab",
			maxWidth: 10,
			wantCells: map[int]term.Cell{
				0: {Ch: 'a', Width: 1},
				1: {Ch: 'b', Width: 1},
			},
		},
		{
			name:     "emoji advances two columns",
			text:     "a🚀b",
			maxWidth: 10,
			wantCells: map[int]term.Cell{
				0: {Ch: 'a', Width: 1},
				1: {Ch: '🚀', Width: 2},
				2: {},
				3: {Ch: 'b', Width: 1},
			},
		},
		{
			name:     "cjk label",
			text:     "中x",
			maxWidth: 10,
			wantCells: map[int]term.Cell{
				0: {Ch: '中', Width: 2},
				1: {},
				2: {Ch: 'x', Width: 1},
			},
		},
		{
			name:     "maxWidth is relative to the starting column",
			text:     "abc",
			x:        3,
			maxWidth: 2,
			wantCells: map[int]term.Cell{
				3: {Ch: 'a', Width: 1},
				4: {Ch: 'b', Width: 1},
				5: {},
			},
		},
		{
			name:     "wide cluster does not straddle the clip",
			text:     "a🚀",
			maxWidth: 2,
			wantCells: map[int]term.Cell{
				0: {Ch: 'a', Width: 1},
				1: {},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := term.NewStringWriter(10, 1)
			require.NoError(t, w.Clear(term.Attributes{}))
			drawText(w, tt.x, 0, tt.text, term.Attributes{}, tt.maxWidth)
			cells := w.Cells()
			for col, want := range tt.wantCells {
				assert.Equal(t, want.Ch, cells[col].Ch, "cell %d rune", col)
				assert.Equal(t, want.Width, cells[col].Width, "cell %d width", col)
			}
		})
	}
}
