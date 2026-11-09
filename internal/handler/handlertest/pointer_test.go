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

package handlertest_test

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/handler/handlertest"
)

// gridHandler marks the cell under the pointer, the way a handler
// tracking hovers by cell does.
type gridHandler struct {
	cells   [][]bool
	checked bool
}

func (g *gridHandler) Resize(width, height int) {
	g.cells = make([][]bool, height)
	for y := range g.cells {
		g.cells[y] = make([]bool, width)
	}
}

func (g *gridHandler) Draw(term.Writer) {}

func (g *gridHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

func (g *gridHandler) Selection() (string, bool) { return "", false }

func (g *gridHandler) Handle(ev term.Event) (bool, bool) {
	if ev.Type != term.EventMouse {
		return false, false
	}
	if g.checked && (ev.MouseY < 0 || ev.MouseY >= len(g.cells) ||
		ev.MouseX < 0 || ev.MouseX >= len(g.cells[ev.MouseY])) {
		return false, false
	}
	g.cells[ev.MouseY][ev.MouseX] = true
	return false, true
}

// failTB records the failure RunPointerLeaves reports.
type failTB struct {
	testing.TB
	failure string
}

func (f *failTB) Helper() {}

func (f *failTB) Fatalf(format string, args ...any) {
	f.failure = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestRunPointerLeaves(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		checked  bool
		wantFail bool
	}{
		{name: "a handler that indexes by the pointer unchecked", wantFail: true},
		{name: "a handler that checks the pointer is on it", checked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tb := &failTB{TB: t}
			done := make(chan struct{})
			go func() {
				defer close(done)
				handlertest.RunPointerLeaves(tb, &gridHandler{checked: tc.checked}, 6, 4)
			}()
			<-done
			if !tc.wantFail {
				assert.Empty(t, tb.failure)
				return
			}
			assert.Contains(t, tb.failure, "index out of range")
			assert.Contains(t, tb.failure, "then a leave to")
		})
	}
}
