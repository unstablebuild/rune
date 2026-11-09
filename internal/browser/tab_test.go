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

package browser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestTabPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		b := newPointerComponent(t, 0)
		tabs := b.Tabs()
		require.NotEmpty(t, tabs)
		for _, tab := range tabs {
			handlertest.RunPointerLeaves(t, tab, size[0], size[1])
		}
		assert.Len(t, b.Tabs(), len(tabs), "the pointer does not close a tab")
	}
}
