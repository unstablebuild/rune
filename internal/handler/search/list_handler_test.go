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

package search

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestSimpleHandlerPointerLeaves(t *testing.T) {
	for _, bottom := range []bool{false, true} {
		for _, size := range [][2]int{{60, 15}, {1, 1}} {
			l := NewList(ListConfig{
				Interrupter: term.NopInterrupter(), SyncSearch: true, BottomSearchBar: bottom,
			})
			for i := range 200 {
				l.PushSync(fmt.Appendf(nil, "a rather long list element number %03d", i))
			}
			l.Buffer().WriteString("1")
			l.FocusStart()
			focus, ok := l.Focus()
			require.True(t, ok)
			var picked []string
			h := Handler(l, handler.NewTestHandler(), func(s string) { picked = append(picked, s) })
			handlertest.RunPointerLeaves(t, h, size[0], size[1])
			after, ok := l.Focus()
			require.True(t, ok)
			assert.Equal(t, string(focus.Data()), string(after.Data()), "the pointer does not move the focus")
			assert.Empty(t, picked, "the pointer does not pick an element")
			require.NoError(t, l.Close())
		}
	}
}
