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

func TestWatchGUISignalsPublishesQuit(t *testing.T) {
	published := make(chan term.Event, 1)
	quit := term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'q'}
	root := &bootstrapHandler{publishEvent: func(ev term.Event) bool {
		published <- ev
		return true
	}}
	stop := watchGUISignals(root, quit)
	defer stop()

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))
	select {
	case ev := <-published:
		assert.Equal(t, quit, ev)
	case <-time.After(5 * time.Second):
		t.Fatal("no quit event published on SIGTERM")
	}
}

func TestWatchGUISignalsDropsTSTP(t *testing.T) {
	published := make(chan term.Event, 1)
	root := &bootstrapHandler{publishEvent: func(ev term.Event) bool {
		published <- ev
		return true
	}}
	stop := watchGUISignals(root, term.Event{})
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

func TestWatchGUISignalsRequestsScreenshot(t *testing.T) {
	config := "editor:\n  mode: modal\nworkspace:\n  wallpaper_ascii: %s\n" +
		"animations:\n  intro: false\n  loading_workspace: false\n  open_workspace: false\n"
	configured := newConfiguredBootstrapForEnvTest(t, configFilename,
		fmt.Sprintf(config, "before-reload"), t.TempDir())
	render := func() string {
		configured.mu.Lock()
		defer configured.mu.Unlock()
		configured.Resize(80, 24)
		w := term.NewStringWriter(80, 24)
		configured.Draw(w)
		require.NoError(t, w.Flush())
		return w.String()
	}
	require.Contains(t, render(), "before-reload")
	require.NoError(t, os.WriteFile(configured.configPath,
		fmt.Appendf(nil, config, "after-reload"), 0o644))

	published := make(chan term.Event, 1)
	root := &bootstrapHandler{
		realIDE:         configured.realIDE,
		quickMenu:       configured.quickMenu,
		quickMenuSounds: configured.quickMenuSounds,
		publishEvent: func(ev term.Event) bool {
			published <- ev
			return true
		},
	}
	stop := watchGUISignals(root, term.Event{})
	defer stop()

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGUSR2))
	next := func(msg string) {
		t.Helper()
		select {
		case ev := <-published:
			require.Equal(t, term.EventInterrupt, ev.Type)
			require.NotNil(t, ev.UserFunc)
			configured.mu.Lock()
			defer configured.mu.Unlock()
			ev.UserFunc()
		case <-time.After(5 * time.Second):
			t.Fatal(msg)
		}
	}
	next("no screenshot requested on SIGUSR2")
	require.Contains(t, render(), "before-reload")
	next("the HUD reload queued no tick to install it")
	assert.Contains(t, render(), "after-reload")
	next("no screenshot queued after applying the HUD")
	assert.Empty(t, published)
}

func TestDispatchGUISignal(t *testing.T) {
	for _, tt := range []struct {
		name             string
		signals          []os.Signal
		acceptQuit       bool
		acceptScreenshot bool
		wantQuit         int
		wantScreenshots  int
		wantExit         bool
	}{
		{name: "screenshot", signals: []os.Signal{syscall.SIGUSR2}, wantScreenshots: 1, acceptScreenshot: true},
		{name: "repeated screenshots", signals: []os.Signal{syscall.SIGUSR2, syscall.SIGUSR2}, wantScreenshots: 2, acceptScreenshot: true},
		{name: "rejected screenshot", signals: []os.Signal{syscall.SIGUSR2}, wantScreenshots: 1},
		{name: "suspend", signals: []os.Signal{syscall.SIGTSTP}},
		{name: "interrupt", signals: []os.Signal{syscall.SIGINT}, acceptQuit: true, wantQuit: 1},
		{name: "terminate", signals: []os.Signal{syscall.SIGTERM}, acceptQuit: true, wantQuit: 1},
		{name: "hangup", signals: []os.Signal{syscall.SIGHUP}, acceptQuit: true, wantQuit: 1},
		{name: "second terminate", signals: []os.Signal{syscall.SIGINT, syscall.SIGTERM}, acceptQuit: true, wantQuit: 1, wantExit: true},
		{name: "rejected quit", signals: []os.Signal{syscall.SIGTERM}, wantQuit: 1, wantExit: true},
		{name: "screenshot after quit", signals: []os.Signal{syscall.SIGTERM, syscall.SIGUSR2, syscall.SIGTSTP}, acceptQuit: true, wantQuit: 1, wantScreenshots: 1},
		{name: "quit after screenshot", signals: []os.Signal{syscall.SIGUSR2, syscall.SIGTERM}, acceptQuit: true, wantQuit: 1, wantScreenshots: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			quit := term.Event{Type: term.EventKey, Ch: 'q'}
			var quits, screenshots int
			var quitSent, forceExit bool
			root := &bootstrapHandler{publishEvent: func(ev term.Event) bool {
				if ev.Type == term.EventInterrupt {
					screenshots++
					require.NotNil(t, ev.UserFunc)
					return tt.acceptScreenshot
				}
				quits++
				require.Equal(t, quit, ev)
				return tt.acceptQuit
			}}
			for _, sig := range tt.signals {
				forceExit = dispatchGUISignal(sig, root, quit, &quitSent)
				if forceExit {
					break
				}
			}
			require.Equal(t, tt.wantQuit, quits)
			require.Equal(t, tt.wantScreenshots, screenshots)
			require.Equal(t, tt.wantExit, forceExit)
			require.Equal(t, tt.acceptQuit && quits > 0, quitSent)
		})
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
