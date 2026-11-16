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

package vteregistry

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestAttachmentPointerLeaves(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner, p := f.create(t, 60, 15)
	cropped := f.attach(t, owner.ID())
	cropped.Resize(30, 5)
	for i := range 40 {
		p.Print(fmt.Sprintf("line %02d: some text in the scrollback\r\n", i))
	}
	p.Print("READY")
	require.Eventually(t, func() bool {
		return strings.Contains(drawn(owner, 60, 15), "READY")
	}, 5*time.Second, 5*time.Millisecond)

	for _, v := range []*attachment{owner, cropped} {
		for _, size := range []term.Coordinates{{X: 60, Y: 15}, {X: 1, Y: 1}} {
			handlertest.RunPointerLeaves(t, v, size.X, size.Y)
		}
	}

	p.HangUp()
	require.Eventually(t, func() bool {
		exit, _ := owner.Handle(term.Event{Type: term.EventNone})
		return exit
	}, 5*time.Second, 5*time.Millisecond)
	for _, v := range []*attachment{owner, cropped} {
		exit, _ := v.Handle(term.Event{Type: term.EventMouse, MouseX: -1, MouseY: 3})
		assert.False(t, exit, "a pointer leaving must not close the dead terminal")
		assert.True(t, v.IsComplete(), "a leave must not hide that the terminal exited, "+
			"or releasing it would pool it")
	}
}
