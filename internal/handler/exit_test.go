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

package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestKeyExitPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		var called int
		h := KeyExitCallback(newPointerLess(t).Scroll(), term.KeyComb{Key: term.KeyEsc},
			func() { called++ })
		handlertest.RunPointerLeaves(t, h, size[0], size[1])
		assert.Zero(t, called, "only the exit key calls back")

		exit, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		assert.True(t, exit)
		assert.True(t, handled)
		assert.Equal(t, 1, called)
	}
}
