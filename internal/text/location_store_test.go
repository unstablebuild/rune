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
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

func TestLocationStoreCursorIntegrationSortedLocations(t *testing.T) {
	t.Run("sorts by level", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "\na\nb\nc\n", false)

		infoList := LocationSlice([]textapi.Location{
			{
				From:    term.Coordinates{Y: 1},
				To:      term.Coordinates{Y: 1, X: 1},
				Attr:    abcAttr,
				Message: "info",
			},
			{
				From:    term.Coordinates{Y: 11},
				To:      term.Coordinates{Y: 11, X: 11},
				Attr:    abcAttr,
				Message: "info2",
			},
		})
		errList := LocationSlice([]textapi.Location{
			{
				From:    term.Coordinates{Y: 2},
				To:      term.Coordinates{Y: 2, X: 2},
				Attr:    abcAttr,
				Message: "err",
			},
		})
		criticalList := LocationSlice([]textapi.Location{
			{
				From:    term.Coordinates{Y: 3},
				To:      term.Coordinates{Y: 3, X: 3},
				Attr:    abcAttr,
				Message: "critical",
			},
		})
		warnList := LocationSlice([]textapi.Location{
			{
				From:    term.Coordinates{Y: 4},
				To:      term.Coordinates{Y: 4, X: 4},
				Attr:    abcAttr,
				Message: "warn",
			},
		})
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityError, "errList", errList))
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, "infoList", infoList))
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityCritical, "criticalList", criticalList))
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityWarning, "warnList", warnList))

		locations := c.SortedLocations()
		require.Len(t, locations, 5)

		assert.Equal(t, term.Coordinates{Y: 1}, locations[0].From)
		assert.Equal(t, term.Coordinates{Y: 1, X: 1}, locations[0].To)
		assert.Equal(t, "info", locations[0].Message)

		assert.Equal(t, "info2", locations[1].Message)
		assert.Equal(t, "warn", locations[2].Message)
		assert.Equal(t, "err", locations[3].Message)
		assert.Equal(t, "critical", locations[4].Message)
	})

	t.Run("sorts by id if level is the same", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "\na\nb\nc\n", false)
		infoList := LocationSlice([]textapi.Location{
			{
				From:    term.Coordinates{Y: 1},
				To:      term.Coordinates{Y: 1, X: 1},
				Attr:    abcAttr,
				Message: "info",
			},
			{
				From:    term.Coordinates{Y: 11},
				To:      term.Coordinates{Y: 11, X: 11},
				Attr:    abcAttr,
				Message: "info1",
			},
		})
		infoList2 := LocationSlice([]textapi.Location{
			{
				From:    term.Coordinates{Y: 2},
				To:      term.Coordinates{Y: 2, X: 2},
				Attr:    abcAttr,
				Message: "info2",
			},
		})
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, "infoList2", infoList2))
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, "infoList", infoList))

		locations := c.SortedLocations()
		require.Len(t, locations, 3)

		assert.Equal(t, term.Coordinates{Y: 1}, locations[0].From)
		assert.Equal(t, term.Coordinates{Y: 1, X: 1}, locations[0].To)
		assert.Equal(t, "info", locations[0].Message)
		assert.Equal(t, "info1", locations[1].Message)
		assert.Equal(t, "info2", locations[2].Message)
	})

	t.Run("idempotency", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "\na\nb\nc\n", false)
		infoList := LocationSlice([]textapi.Location{
			{
				From:    term.Coordinates{Y: 1},
				To:      term.Coordinates{Y: 1, X: 1},
				Attr:    abcAttr,
				Message: "info",
			},
			{
				From:    term.Coordinates{Y: 11},
				To:      term.Coordinates{Y: 11, X: 11},
				Attr:    abcAttr,
				Message: "info1",
			},
		})
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, "infoList", infoList))

		require.Len(t, c.SortedLocations(), 2)
		require.Len(t, c.SortedLocations(), 2)
		require.Len(t, c.SortedLocations(), 2)
		require.Len(t, c.SortedLocations(), 2)
	})
}

func TestLocationStoreCursorIntegrationLocationLists(t *testing.T) {
	c := setupCursorContent(t, 10, 10, "\na\nb\nc\n", false)
	infoList := LocationSlice([]textapi.Location{
		{
			From:    term.Coordinates{Y: 1},
			To:      term.Coordinates{Y: 1, X: 1},
			Attr:    abcAttr,
			Message: "info",
		},
		{
			From:    term.Coordinates{Y: 11},
			To:      term.Coordinates{Y: 11, X: 11},
			Attr:    abcAttr,
			Message: "info1",
		},
	})
	infoList2 := LocationSlice([]textapi.Location{
		{
			From:    term.Coordinates{Y: 2},
			To:      term.Coordinates{Y: 2, X: 2},
			Attr:    abcAttr,
			Message: "info2",
		},
	})
	assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, "infoList2", infoList2))
	assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, "infoList", infoList))

	expected := []LocationSet{
		{
			ID:       "infoList2",
			Priority: 0,
			Locations: []textapi.Location{
				{
					From:    term.Coordinates{X: 0, Y: 2},
					To:      term.Coordinates{X: 2, Y: 2},
					Attr:    abcAttr,
					Message: "info2",
				},
			},
		},
		{
			ID:       "infoList",
			Priority: 0,
			Locations: []textapi.Location{
				{
					From:    term.Coordinates{X: 0, Y: 1},
					To:      term.Coordinates{X: 1, Y: 1},
					Attr:    abcAttr,
					Message: "info",
				},
				{
					From:    term.Coordinates{X: 0, Y: 11},
					To:      term.Coordinates{X: 11, Y: 11},
					Attr:    abcAttr,
					Message: "info1",
				},
			},
		},
	}

	actual := c.LocationLists()
	require.Len(t, actual, 2)
	assert.ElementsMatch(t, expected, actual)
}

func TestLocationStoreCursorIntegrationSetLocationListMessages(t *testing.T) {
	content := "\naaa\nbbb\nccc\n"
	messageLocations := []textapi.Location{
		{
			From:    term.Coordinates{Y: 1},
			To:      term.Coordinates{Y: 1, X: 2},
			Attr:    abcAttr,
			Message: "1",
		},
		{
			From:    term.Coordinates{Y: 2},
			To:      term.Coordinates{Y: 2, X: 2},
			Attr:    abcAttr,
			Message: "2",
		},
		{
			From:    term.Coordinates{Y: 3},
			To:      term.Coordinates{Y: 3, X: 2},
			Attr:    abcAttr,
			Message: "3",
		},
	}

	assertMessages := func(t *testing.T, c *Cursor) {
		for i := range 3 {
			locs, ok := c.LocationsAtCursor()
			require.True(t, ok)
			require.Len(t, locs, 1)
			assert.Equal(t, locs[0].Message, strconv.Itoa(i+1))
			c.MoveDown()
		}
	}

	t.Run("returns nil/false if cursor is not in from, to or in between", func(t *testing.T) {
		c := setupCursorContent(t, 10, 2, content, false)
		abcList := LocationSlice(messageLocations)
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))

		_, ok := c.LocationsAtCursor()
		assert.False(t, ok)
	})

	t.Run("return messages if cursor is at From", func(t *testing.T) {
		c := setupCursorContent(t, 10, 2, content, false)
		abcList := LocationSlice(messageLocations)
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))
		require.True(t, c.MoveDown())

		assertMessages(t, c)
	})

	t.Run("return messages if cursor between From/To", func(t *testing.T) {
		c := setupCursorContent(t, 10, 2, content, false)

		abcList := LocationSlice(messageLocations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))
		require.True(t, c.MoveDown())
		require.True(t, c.MoveRight())

		assertMessages(t, c)
	})

	t.Run("return messages if cursor is at To", func(t *testing.T) {
		c := setupCursorContent(t, 10, 2, content, false)

		abcList := LocationSlice(messageLocations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))
		require.True(t, c.MoveDown())
		c.MoveRight()
		c.MoveRight()

		assertMessages(t, c)
	})
}

func TestCursorDrawLocationListsIntegration(t *testing.T) {
	t.Run("sets location list attrs", func(t *testing.T) {
		c := setupCursorContent(t, 1, 5, "\na\nb\nc\n", false)

		expected := [][]term.Cell{
			{{}},
			{term.NewCell(0, 0, abcAttr)},
			{term.NewCell(0, 0, abcAttr)},
			{term.NewCell(0, 0, abcAttr)},
			{{}},
		}

		abcList := LocationSlice(abcLocations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))

		w := cell.NewBufferWriter(context.Background(), 1, 5)
		DrawLocations(c.SortedLocations(), c.scroll, w)
		assert.Equal(t, expected, w.RawCells())
	})

	t.Run("clears location lists", func(t *testing.T) {
		c := setupCursorContent(t, 1, 5, "\na\nb\nc\n", false)
		abcList := LocationSlice(abcLocations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))
		assert.NotNil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, nil))

		expected := [][]term.Cell{
			{{}},
			{{}},
			{{}},
			{{}},
			{{}},
		}

		w := cell.NewBufferWriter(context.Background(), 1, 5)
		DrawLocations(c.SortedLocations(), c.scroll, w)
		assert.Equal(t, expected, w.RawCells())
	})

	t.Run("lower priority lists do not override higher priority list attrs", func(t *testing.T) {
		infoLocations := []textapi.Location{
			{
				From: term.Coordinates{Y: 1},
				To:   term.Coordinates{Y: 1, X: 1},
				Attr: term.Attributes{Fg: term.ColorRed, Bg: term.ColorGreen},
			},
			{
				From: term.Coordinates{Y: 2},
				To:   term.Coordinates{Y: 2, X: 1},
				Attr: term.Attributes{Fg: term.ColorRed, Bg: term.ColorGreen},
			},
			{
				From: term.Coordinates{Y: 3},
				To:   term.Coordinates{Y: 3, X: 1},
				Attr: term.Attributes{Fg: term.ColorRed, Bg: term.ColorGreen},
			},
		}
		criticalLocations := []textapi.Location{
			{
				From: term.Coordinates{Y: 1},
				To:   term.Coordinates{Y: 1, X: 1},
				Attr: term.Attributes{Attrs: term.AttrUnderline, Bg: term.ColorBlack},
			},
			{
				From: term.Coordinates{Y: 2},
				To:   term.Coordinates{Y: 2, X: 1},
				Attr: term.Attributes{Attrs: term.AttrUnderline, Bg: term.ColorBlack},
			},
			{
				From: term.Coordinates{Y: 3},
				To:   term.Coordinates{Y: 3, X: 1},
				Attr: term.Attributes{Attrs: term.AttrUnderline, Bg: term.ColorBlack},
			},
		}
		c := setupCursorContent(t, 1, 5, "\na\nb\nc\n", false)

		expected := [][]term.Cell{
			{{}},
			{term.NewCell(0, 0, term.Attributes{Fg: term.ColorRed, Attrs: term.AttrUnderline, Bg: term.ColorBlack})},
			{term.NewCell(0, 0, term.Attributes{Fg: term.ColorRed, Attrs: term.AttrUnderline, Bg: term.ColorBlack})},
			{term.NewCell(0, 0, term.Attributes{Fg: term.ColorRed, Attrs: term.AttrUnderline, Bg: term.ColorBlack})},
			{{}},
		}

		criticalList := LocationSlice(criticalLocations)
		infoList := LocationSlice(infoLocations)
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityCritical, "list1", criticalList))
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, "list2", infoList))

		w := cell.NewBufferWriter(context.Background(), 1, 5)
		DrawLocations(c.SortedLocations(), c.scroll, w)
		assert.Equal(t, expected, w.RawCells())
	})

	t.Run("trims to fit location To line if From is in bounds", func(t *testing.T) {
		c := setupCursorContent(t, 1, 5, "\na\nb\nc\n", false)

		expected := [][]term.Cell{
			{{}},
			{{}},
			{term.NewCell(0, 0, abcAttr)},
			{term.NewCell(0, 0, abcAttr)},
			{term.NewCell(0, 0, abcAttr)},
		}
		locations := []textapi.Location{
			{
				From: term.Coordinates{Y: 2},
				To:   term.Coordinates{Y: 6, X: 1},
				Attr: abcAttr,
			},
		}

		abcList := LocationSlice(locations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))

		w := cell.NewBufferWriter(context.Background(), 1, 5)
		DrawLocations(c.SortedLocations(), c.scroll, w)
		assert.Equal(t, expected, w.RawCells())
	})

	t.Run("draws partial location that starts above viewport", func(t *testing.T) {
		var content strings.Builder
		for i := range 15 {
			content.WriteString(strconv.Itoa(i))
			content.WriteString("\n")
		}
		c := setupCursorContent(t, 1, 5, content.String(), false)
		c.scroll.SetOffset(term.Coordinates{Y: 10})

		expected := [][]term.Cell{
			{term.NewCell(0, 0, abcAttr)},
			{term.NewCell(0, 0, abcAttr)},
			{term.NewCell(0, 0, abcAttr)},
			{{}},
			{{}},
		}
		locations := []textapi.Location{
			{
				From: term.Coordinates{Y: 5},
				To:   term.Coordinates{Y: 12, X: 1},
				Attr: abcAttr,
			},
		}

		abcList := LocationSlice(locations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))

		w := cell.NewBufferWriter(context.Background(), 1, 5)
		DrawLocations(c.SortedLocations(), c.scroll, w)
		assert.Equal(t, expected, w.RawCells())
	})

	t.Run("does not panic if width == 0 in wrap mode", func(t *testing.T) {
		c := setupCursorContent(t, 0, 5, "\na\nb\nc\n", false)
		c.scroll.Wrap = true

		expected := [][]term.Cell{
			{{}},
			{{}},
			{{}},
			{{}},
			{{}},
		}
		locations := []textapi.Location{
			{
				From: term.Coordinates{Y: 0},
				To:   term.Coordinates{Y: 1, X: 1},
				Attr: abcAttr,
			},
		}

		abcList := LocationSlice(locations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))

		w := cell.NewBufferWriter(context.Background(), 1, 5)
		DrawLocations(c.SortedLocations(), c.scroll, w)
		assert.Equal(t, expected, w.RawCells())
	})

	t.Run("sets full height worth of locations when there are hidden lines", func(t *testing.T) {
		c := setupCursorContent(t, 1, 5, "\na\nb\nc\n", false)

		require.True(t, c.scroll.MarkHidden(1, 3))

		locations := []textapi.Location{
			{
				From:    term.Coordinates{Y: 1},
				To:      term.Coordinates{Y: 1, X: 1},
				Attr:    term.Attributes{Bg: term.ColorRed},
				Message: "blabla",
			},
			{
				From: term.Coordinates{Y: 2},
				To:   term.Coordinates{Y: 2, X: 1},
				Attr: term.Attributes{Bg: term.GetColor("orange")},
			},
			{
				From: term.Coordinates{Y: 3},
				To:   term.Coordinates{Y: 3, X: 1},
				Attr: term.Attributes{Bg: term.ColorYellow},
			},
		}

		expected := [][]term.Cell{
			{{}, {}},
			{term.NewCell(0, 0, term.Attributes{Bg: term.ColorRed}),
				term.NewCell(0, 0, term.Attributes{Bg: 0})},
			{{}, {}},
			{{}, {}},
			{{}, {}},
		}
		abcList := LocationSlice(locations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))

		w := cell.NewBufferWriter(context.Background(), 2, 5)
		DrawLocations(c.SortedLocations(), c.scroll, w)
		assert.Equal(t, expected, w.RawCells())
	})

	t.Run("shows cue for messages hidden inside hidden lines", func(t *testing.T) {
		c := setupCursorContent(t, 1, 5, "\na\nb\nc\n", false)

		require.True(t, c.scroll.MarkHidden(1, 3))

		locations := []textapi.Location{
			{
				From:    term.Coordinates{Y: 2},
				To:      term.Coordinates{Y: 2, X: 1},
				Attr:    term.Attributes{Bg: term.ColorGreen},
				Message: "B HAS A MESSAGE FOR YOU",
			},
		}

		abcList := LocationSlice(locations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))

		expected := [][]term.Cell{
			{{}, {}},
			{term.NewCell(0, 0, term.Attributes{Bg: term.ColorGreen}),
				term.NewCell(0, 0, term.Attributes{Bg: 0})},
			{{}, {}},
			{{}, {}},
			{{}, {}},
		}

		w := cell.NewBufferWriter(context.Background(), 2, 5)
		DrawLocations(c.SortedLocations(), c.scroll, w)
		assert.Equal(t, expected, w.RawCells())
	})

	t.Run("does not render attrs past last rendered line", func(t *testing.T) {
		c := setupCursorContent(t, 1, 5, "\na\nb\nc\n", false)

		require.True(t, c.scroll.MarkHidden(1, 3))

		locations := []textapi.Location{
			{
				From: term.Coordinates{Y: 1},
				To:   term.Coordinates{Y: 1, X: 1},
				Attr: term.Attributes{Bg: term.ColorRed},
			},
			{
				From: term.Coordinates{Y: 2},
				To:   term.Coordinates{Y: 2, X: 1},
				Attr: term.Attributes{Bg: term.GetColor("orange")},
			},
			{
				From: term.Coordinates{Y: 3},
				To:   term.Coordinates{Y: 3, X: 1},
				Attr: term.Attributes{Bg: term.ColorYellow},
			},
		}

		expected := [][]term.Cell{
			{{}, {}},
			{{}, {}},
			{{}, {}},
			{{}, {}},
			{{}, {}},
		}
		abcList := LocationSlice(locations)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, abcList))

		c.scroll.Resize(1, 1)
		w := cell.NewBufferWriter(context.Background(), 2, 5)
		DrawLocations(c.SortedLocations(), c.scroll, w)
		assert.Equal(t, expected, w.RawCells())
	})

	// A superimposed message or search bar is drawn right below the
	// scroll, so a location spanning past the viewport must not paint it.
	for _, wrap := range []bool{false, true} {
		t.Run(fmt.Sprintf("does not render multi-line location below viewport (wrap=%t)", wrap), func(t *testing.T) {
			c := setupCursorContent(t, 1, 5, "a\nb\nc\nd\ne\nf\ng\n", false)
			c.scroll.Wrap = wrap
			c.scroll.Resize(1, 3)

			locations := []textapi.Location{
				{
					From: term.Coordinates{Y: 0},
					To:   term.Coordinates{Y: 6, X: 1},
					Attr: abcAttr,
				},
			}
			assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(locations)))

			expected := [][]term.Cell{
				{term.NewCell(0, 0, abcAttr)},
				{term.NewCell(0, 0, abcAttr)},
				{term.NewCell(0, 0, abcAttr)},
				{{}},
				{{}},
			}
			w := cell.NewBufferWriter(context.Background(), 1, 5)
			DrawLocations(c.SortedLocations(), c.scroll, w)
			assert.Equal(t, expected, w.RawCells())
		})
	}

	attrRow := []term.Cell{term.NewCell(0, 0, abcAttr), term.NewCell(0, 0, abcAttr)}
	blankRow := []term.Cell{{}, {}}
	for _, tc := range []struct {
		name     string
		content  string
		wrap     bool
		loc      textapi.Location
		expected [][]term.Cell
	}{
		{
			name:    "renders location on last visible line wrapping past viewport",
			content: "a\nb\ncdef\ng\n",
			wrap:    true,
			loc: textapi.Location{
				From: term.Coordinates{Y: 2},
				To:   term.Coordinates{Y: 2, X: 4},
				Attr: abcAttr,
			},
			expected: [][]term.Cell{blankRow, blankRow, attrRow, blankRow, blankRow},
		},
		{
			name:    "does not render location on line below viewport",
			content: "a\nb\nc\nde\n",
			loc: textapi.Location{
				From: term.Coordinates{Y: 3},
				To:   term.Coordinates{Y: 3, X: 2},
				Attr: abcAttr,
			},
			expected: [][]term.Cell{blankRow, blankRow, blankRow, blankRow, blankRow},
		},
		{
			name:    "does not render location on line below viewport (wrap)",
			content: "a\nb\nc\nde\n",
			wrap:    true,
			loc: textapi.Location{
				From: term.Coordinates{Y: 3},
				To:   term.Coordinates{Y: 3, X: 2},
				Attr: abcAttr,
			},
			expected: [][]term.Cell{blankRow, blankRow, blankRow, blankRow, blankRow},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 2, 3, tc.content, tc.wrap)
			assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID,
				LocationSlice([]textapi.Location{tc.loc})))

			w := cell.NewBufferWriter(context.Background(), 2, 5)
			DrawLocations(c.SortedLocations(), c.scroll, w)
			assert.Equal(t, tc.expected, w.RawCells())
		})
	}
}
