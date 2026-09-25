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

package vte

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestTerminalSnapshotStorageRoundTripTermCells(t *testing.T) {
	type doc struct {
		Snapshot Snapshot
	}

	stored := doc{Snapshot: Snapshot{
		Schema:       terminalSnapshotVersion,
		Title:        "saved",
		Width:        80,
		Height:       24,
		ScrollOffset: term.Coordinates{Y: 3, X: 1},
		Primary: ScreenSnapshot{
			Cursor: term.Coordinates{Y: 7, X: 4},
			Cells: [][]term.Cell{
				{
					{
						Fg:    term.ColorRed,
						Bg:    term.ColorBlue,
						Attrs: term.AttrBold | term.AttrUnderline,
						Ch:    'e',
						Extra: &term.CellExtra{Combining: []rune{'\u0301'}},
						Width: 1,
						Bytes: 3,
					},
					{Ch: '界', Width: 2, Bytes: 3},
				},
			},
		},
		Alternate: ScreenSnapshot{
			Cursor: term.Coordinates{Y: 1, X: 2},
			Cells: [][]term.Cell{
				{
					{Ch: 'a', Width: 1, Bytes: 1},
					{Ch: 'b', Width: 1, Bytes: 1},
				},
			},
		},
	}}

	svc := storagestub.NewInMemoryService()
	require.NoError(t, svc.Set(context.Background(), "terminal", stored))

	var actual doc
	require.NoError(t, svc.Get(context.Background(), "terminal", &actual))
	require.Equal(t, stored, actual)
}
