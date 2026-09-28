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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

func TestLocationHighlightCommandHighlightsAllLocations(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "bookmarks.go")

	registry := newIndentTestWorkspaceRegistry()
	fileRegistry := NewFileCommandRegistry(cwd, registry)
	bookmarks := []textapi.Location{
		{
			From: term.Coordinates{Y: 0},
			To:   term.Coordinates{Y: 0, X: 1},
		},
		{
			From: term.Coordinates{Y: 2},
			To:   term.Coordinates{Y: 2, X: 1},
		},
	}
	h := &locationCommandTestHandler{
		uri: uri,
		lists: []LocationSet{{
			ID:        "bookmark",
			Priority:  textapi.LocationPriorityInfo,
			Locations: bookmarks,
		}},
	}

	wrapped, err := SubscribeLocationCommands(uri, fileRegistry, h)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, wrapped.Close()) })

	err = registry.sub[cwd.String()][CommandHighlightLocations].HandleCommand(context.Background(), textapi.Command{
		URI:  uri,
		Name: CommandHighlightLocations,
		Args: []string{"bookmark"},
	})
	require.NoError(t, err)

	assert.Equal(t, textapi.LocationPriorityCritical, h.lastPriority)
	assert.Equal(t, selectionLocationListID, h.lastID)
	assert.False(t, h.lastLocationListNil)
	assert.Equal(t, []textapi.Location{
		{
			From: term.Coordinates{Y: 0},
			To:   term.Coordinates{Y: 0, X: 1},
			Attr: term.Attributes{Attrs: term.AttrReverse},
		},
		{
			From: term.Coordinates{Y: 2},
			To:   term.Coordinates{Y: 2, X: 1},
			Attr: term.Attributes{Attrs: term.AttrReverse},
		},
	}, h.lastLocations)

	err = registry.sub[cwd.String()][CommandHighlightLocations].HandleCommand(context.Background(), textapi.Command{
		URI:  uri,
		Name: CommandHighlightLocations,
		Args: []string{"bookmark"},
	})
	require.NoError(t, err)

	assert.Equal(t, textapi.LocationPriorityCritical, h.lastPriority)
	assert.Equal(t, selectionLocationListID, h.lastID)
	assert.False(t, h.lastLocationListNil)
	assert.Empty(t, h.lastLocations)
}

type locationCommandTestHandler struct {
	handler.TestHandler
	uri                 workspaceapi.URI
	lists               []LocationSet
	lastPriority        textapi.LocationPriority
	lastID              string
	lastLocations       []textapi.Location
	lastLocationListNil bool
}

func (h *locationCommandTestHandler) Close() error { return nil }

func (h *locationCommandTestHandler) Resource() workspaceapi.URI { return h.uri }

func (h *locationCommandTestHandler) SetWrap(bool) {}

func (h *locationCommandTestHandler) ShowCommandBar(bool) {}

func (h *locationCommandTestHandler) SetCursorAtScroll(term.Coordinates) bool { return false }

func (h *locationCommandTestHandler) CursorAtScroll() term.Coordinates { return term.Coordinates{} }

func (h *locationCommandTestHandler) SetLocationList(
	priority textapi.LocationPriority, id string, locations LocationList,
) {
	h.lastPriority = priority
	h.lastID = id
	h.lastLocationListNil = locations == nil
	if locations == nil {
		h.lastLocations = nil
		for i, list := range h.lists {
			if list.ID == id {
				h.lists = append(h.lists[:i], h.lists[i+1:]...)
				return
			}
		}
		return
	}
	h.lastLocations = []textapi.Location{}
	scrollStartList(locations)
	for loc, ok := locations.Current(); ok; loc, ok = locations.Next() {
		h.lastLocations = append(h.lastLocations, loc)
	}
	for i, list := range h.lists {
		if list.ID == id {
			h.lists[i].Priority = priority
			h.lists[i].Locations = append([]textapi.Location(nil), h.lastLocations...)
			return
		}
	}
	h.lists = append(h.lists, LocationSet{
		ID:        id,
		Priority:  priority,
		Locations: append([]textapi.Location(nil), h.lastLocations...),
	})
}

func (h *locationCommandTestHandler) LocationLists() []LocationSet { return h.lists }

func (h *locationCommandTestHandler) MoveToNextLocation(string) bool { return false }

func (h *locationCommandTestHandler) MoveToPrevLocation(string) bool { return false }

func (h *locationCommandTestHandler) CellView() cell.View { return nil }

func (h *locationCommandTestHandler) CellEditor() cell.Editor { return nil }

func (h *locationCommandTestHandler) SetDefaultAttributes(term.Attributes) {}

func (h *locationCommandTestHandler) SeekUp() bool { return false }

func (h *locationCommandTestHandler) SeekDown() bool { return false }

func (h *locationCommandTestHandler) SeekOffset() int { return 0 }

func (h *locationCommandTestHandler) MaxSeekOffset() int { return 0 }

func (h *locationCommandTestHandler) Dimensions() (int, int) { return 0, 0 }

func (h *locationCommandTestHandler) IsSearchMode() bool { return false }

func (h *locationCommandTestHandler) IsNormalMode() bool { return false }

var _ Handler = (*locationCommandTestHandler)(nil)
var _ browserapi.Handler = (*locationCommandTestHandler)(nil)
