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

package extension

import (
	"testing"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

// The "panic" flavour panics on every event by design, so only the
// "slow" flavour, at zero latency, is driven through leaves.
func TestChaosHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		h := &chaosHandler{comp: component.NewStringWithConfig(copy, component.StringConfig{
			Alignment: component.AlignmentCentered,
		})}
		handlertest.RunPointerLeaves(t, h, size[0], size[1])
	}
}

func TestChaosHandlerPointerLeaveDoesNotExit(t *testing.T) {
	t.Parallel()
	h := &chaosHandler{comp: component.NewStringWithConfig(copy, component.StringConfig{
		Alignment: component.AlignmentCentered,
	})}
	h.Resize(60, 15)
	for _, out := range []term.Coordinates{{X: -1, Y: 7}, {X: 60, Y: 7}, {X: 30, Y: -1}, {X: 30, Y: 15}} {
		if exit, _ := h.Handle(term.Event{Type: term.EventMouse, MouseX: out.X, MouseY: out.Y}); exit {
			t.Fatalf("a leave to %v closes the chaos window", out)
		}
	}
}
