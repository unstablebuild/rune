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
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestKeyMappedLessHandle(t *testing.T) {
	cases := getLessHandleTestFlow([26]term.Event{
		{},
		{Ch: 'k', Type: term.EventKey},
		{Ch: 'U', Type: term.EventKey},
		{Ch: 'v', Type: term.EventKey},
		{Ch: '%', Type: term.EventKey},
		{Key: term.KeyArrowRight, Type: term.EventKey},
		{Key: term.KeyArrowLeft, Type: term.EventKey},
		{Ch: 'G', Type: term.EventKey},
		{Ch: 'g', Type: term.EventKey},
		{Ch: '\\', Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Key: term.KeyBackspace, Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Key: term.KeyEnter, Type: term.EventKey},
		{Ch: 'g', Type: term.EventKey},
		{Ch: 'N', Type: term.EventKey, Mod: term.ModAlt},
		{Ch: 'n', Type: term.EventKey, Mod: term.ModAlt},
		{Ch: 'G', Type: term.EventKey},
		{Ch: '_', Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Ch: 'X', Type: term.EventKey},
		{Key: term.KeyEnter, Type: term.EventKey},
		{Ch: 'n', Type: term.EventKey, Mod: term.ModAlt},
		{Ch: 'n', Type: term.EventKey, Mod: term.ModAlt},
		{Ch: 'N', Type: term.EventKey, Mod: term.ModAlt},
	})
	less1, writer3 := setup(t, nil, 8, 4)
	handlertest.TestHandler(t, WithMapping(less1, map[term.KeyComb]term.KeyComb{
		{Ch: 'k'}:                   {Ch: 'k'},
		{Ch: 'U'}:                   {Ch: 'j'},
		{Ch: '%'}:                   {Ch: 'h'},
		{Ch: 'v'}:                   {Ch: 'l'},
		{Ch: '\\'}:                  {Ch: '/'},
		{Ch: '_'}:                   {Ch: '?'},
		{Key: term.KeyArrowRight}:   {Ch: '$'},
		{Key: term.KeyArrowLeft}:    {Ch: '0'},
		{Ch: 'N', Mod: term.ModAlt}: {Ch: 'N'},
		{Ch: 'n', Mod: term.ModAlt}: {Ch: 'n'},
	}), cases, writer3)
}

func TestKeyMappingCursor(t *testing.T) {
	handler := &handler.TestHandler{CursorStyle: term.CursorStyleBlinkingBlock}
	cursor, style, _ := handler.Cursor()
	kmCursor, kmStyle, _ := WithMapping(handler, nil).Cursor()
	assert.Equal(t, cursor, kmCursor)
	assert.Equal(t, style, kmStyle)
}

func TestKeyMappingHandlerPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		h := WithMapping(newPointerLess(t), map[term.KeyComb]term.KeyComb{
			{Ch: 'x'}: {Ch: 'j'},
		})
		handlertest.RunPointerLeaves(t, h, size[0], size[1])
	}
}
