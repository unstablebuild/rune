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

package gui

import (
	"time"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// PublishEvent enqueues ev for the next frame and wakes the run loop.
func (g *GUI) PublishEvent(ev term.Event) bool {
	if ev.Type == term.EventInterrupt && ev.Raw == nil && ev.UserFunc == nil {
		g.interruptPending.Store(true)
		ebiten.ScheduleFrame()
		return true
	}
	select {
	case g.updateChan <- ev:
		ebiten.ScheduleFrame()
		return true
	default:
		return false
	}
}

func (g *GUI) awaitEchoInterrupt() bool {
	deadline := time.Now().Add(echoWaitBudget)
	for !g.interruptPending.Load() {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(echoPollInterval)
	}
	return true
}
