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

//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/debug"
)

// watchGUISignals publishes the quit event on terminate signals so the
// GUI exits through the same close chain as a window ✕. SIGTSTP is
// caught and dropped rather than ignored: SIG_IGN survives execve and
// would leak into child processes such as integrated-terminal shells.
func watchGUISignals(publish func(term.Event) bool, quit term.Event) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP,
		syscall.SIGTSTP)
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		quitSent := false
		for {
			select {
			case sig, ok := <-ch:
				if !ok {
					return
				}
				if sig == syscall.SIGTSTP {
					continue
				}
				// A second terminate signal or a saturated event queue
				// means the graceful path already failed.
				if quitSent || !publish(quit) {
					os.Exit(1)
				}
				quitSent = true
			case <-done:
				return
			}
		}
	})
	return func() {
		close(done)
		signal.Stop(ch)
	}
}
