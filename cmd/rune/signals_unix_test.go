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
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// A terminate-class signal must route to the quit event so the GUI
// exits through the deferred-close chain that kills extensions.
func TestWatchGUISignalsPublishesQuit(t *testing.T) {
	published := make(chan term.Event, 1)
	quit := term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'q'}
	stop := watchGUISignals(func(ev term.Event) bool {
		published <- ev
		return true
	}, quit)
	defer stop()

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))
	select {
	case ev := <-published:
		assert.Equal(t, quit, ev)
	case <-time.After(5 * time.Second):
		t.Fatal("no quit event published on SIGTERM")
	}
}

// SIGTSTP must be caught and dropped, not SIG_IGN'd: ignore
// dispositions survive execve and would disable Ctrl+Z inside
// integrated-terminal child processes. If it were neither caught nor
// ignored, this kill would stop the test process.
func TestWatchGUISignalsDropsTSTP(t *testing.T) {
	published := make(chan term.Event, 1)
	stop := watchGUISignals(func(ev term.Event) bool {
		published <- ev
		return true
	}, term.Event{})
	defer stop()
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTSTP))
	if runtime.GOOS == "linux" {
		caught, err := signalMasked(syscall.SIGTSTP, "SigCgt:")
		require.NoError(t, err)
		assert.True(t, caught, "SIGTSTP must be caught in GUI mode")
	}
	select {
	case ev := <-published:
		t.Fatalf("SIGTSTP published event %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

// signalMasked parses /proc/self/status: bit N of the named signal
// mask field (SigIgn, SigCgt, ...) is signal N.
func signalMasked(sig syscall.Signal, field string) (bool, error) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, field) {
			var mask uint64
			if _, err := fmt.Sscanf(strings.TrimPrefix(line, field),
				"%x", &mask); err != nil {
				return false, err
			}
			return mask&(1<<uint(sig-1)) != 0, nil
		}
	}
	return false, nil
}
