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

package vte

import (
	"context"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte/vteparser"
)

var _ vteparser.Handler = (*waitParserHandler)(nil)

type waitParserHandler struct {
	ch      chan func()
	trigger func()
	vteparser.Handler
	// pending counts the callbacks scheduled and not yet done, queued or
	// running. Whoever moves it off zero, or leaves it above zero when a
	// callback is done, asks for the next bell: each add returns to one
	// caller only, so exactly one bell is outstanding while callbacks
	// wait. Deciding from len(ch) instead lets a callback scheduled while
	// the parser answers a bell go unasked for by both sides.
	pending atomic.Int32
}

func newWaitParserHandler(ctx context.Context, h vteparser.Handler) *waitParserHandler {
	ret := &waitParserHandler{
		// NOTE: The channel size MUST BE smaller than the default
		// event-loop channel size so we stop processing callbacks
		// before event loop callbacks get backed up and bell
		// cannot be triggered anymore.
		ch:      make(chan func(), 50),
		Handler: h,
	}
	go debug.CapturePanicReport(func() {
		ret.monitorStarvation(ctx)
	})
	return ret
}

func (w *waitParserHandler) useTrigger(trigger func()) {
	w.trigger = trigger
}

func (w *waitParserHandler) monitorStarvation(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	var prevLength int
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			currLength := len(w.ch)
			// if no events were drained for a second,
			// it's possible bell cannot be triggered: drain events to relieve
			// event loop
			if prevLength > 0 && currLength == prevLength && w.trigger != nil {
				w.log(log.DebugLevel, "scheduler is not making progress: draining %d events", len(w.ch))
				w.drop()
			}
			prevLength = currLength
		}
	}
}

// drop discards every callback waiting for a bell, so that the next
// scheduleBellCallback rings the shell again, and a bell that arrives
// late runs none of them.
func (w *waitParserHandler) drop() {
	for {
		select {
		case <-w.ch:
			w.pending.Add(-1)
			continue
		default:
		}
		return
	}
}

func (w *waitParserHandler) pendingCallbacks() int {
	return len(w.ch)
}

func (w *waitParserHandler) scheduleBellCallback(callback func()) (ok bool) {
	if w.trigger == nil {
		panic("must install first a trigger via useTrigger")
	}

	select {
	case w.ch <- callback:
	default:
		// don't block
		return false
	}
	// Only once queued, or the bell could find no callback to run.
	if w.pending.Add(1) == 1 {
		w.trigger()
	}
	return true
}

func (w *waitParserHandler) Bell() {
	select {
	case cb := <-w.ch:
		cb()
		// After cb, so the bell is ordered after what it wrote to the pty.
		if w.pending.Add(-1) > 0 {
			w.trigger()
		}
	default:
		w.Handler.Bell()
	}
}

func (v *waitParserHandler) log(level log.Level, line string, params ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "vte.waitParserHandler").
		Logf(level, line, params...)
}
