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

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
)

func TestComponentForwardsDragToBrowser(t *testing.T) {
	c := new(Component)
	c.comp.Init(browser.DefaultConfig())
	c.comp.Resize(40, 10)

	pos := term.Coordinates{X: 5, Y: 5}
	assert.True(t, c.DragHover(pos), "hover must reach the browser component")
	assert.True(t, c.DragDrop(pos, []string{"/tmp/a.png"}),
		"drop must reach the browser component")
	c.DragCancel()
}
