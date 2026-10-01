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
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/component/shader/shaderutils"
	"unstable.build/rune/internal/workspace"
)

// exitedTerminalAttr marks the tab icon of a terminal whose process
// failed.
var exitedTerminalAttr = term.Attributes{Fg: term.ColorRed}

// exitedTerminalBase namespaces the editors failed terminal tabs are
// left with. Opening them publishes an EventTypeOpen that nothing ever
// retracts, so it has to be a scheme the workspace manager can still
// resolve on a reload.
const exitedTerminalBase = workspace.MemoryScheme + ":///terminal"

// exitedTerminalLocationList carries the exit status. It rides a
// location Message because every editor draws one, while a message set
// any other way is cleared by the editor's next cursor move.
const exitedTerminalLocationList = "terminalexit"

// failedTerminalKeeper keeps terminal tabs whose process failed open
// when their vte asks to be dropped, so the failure stays visible
// until the user closes the tab.
type failedTerminalKeeper struct {
	*tabNameAliaser
	e *ex
}

func (k failedTerminalKeeper) OnTabExit(uri workspaceapi.URI) bool {
	return k.e.keepFailedTerminalTab(k.resolve(uri)) ||
		k.tabNameAliaser.OnTabExit(uri)
}

// grayTerminalCells drains the color from cells in place, the way the
// gray shader does, so the output of a dead terminal reads as inert.
// A default foreground is resolved to defaultFg first; a default
// background is kept.
func grayTerminalCells(cells [][]term.Cell, defaultFg term.Color) {
	for _, row := range cells {
		for x := range row {
			row[x].Fg = shaderutils.DesaturateColor(row[x].Fg, 1, defaultFg)
			row[x].Bg = shaderutils.DesaturateColor(row[x].Bg, 1, term.ColorDefault)
		}
	}
}

// exitedTerminalLocations spans every row with the exit status, so the
// editor shows it wherever the cursor is.
func exitedTerminalLocations(rows int, exitErr error) textapi.LocationList {
	return textapi.LocationSlice([]textapi.Location{{
		To:      term.Coordinates{Y: rows},
		Message: exitErr.Error(),
	}})
}
