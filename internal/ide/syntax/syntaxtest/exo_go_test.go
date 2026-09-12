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

//go:build e2e

package syntaxtest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/exoeditor"
	"unstable.build/rune/internal/workspace"
)

// TestExoSyntaxHighlightsOverlayScrolling drives the full
// exo → vte → real-vim pipeline against a real Go file. text.Component
// installs the tree-sitter syntax tree which calls SetLocationList on
// the exo handler; Draw then overlays those locations on top of vim's
// rendered output. The test verifies that the overlay tracks the
// embedded editor as the cursor scrolls through the file.
//
// term.StringWriter with ForegroundCh = '#' renders every cell that
// carries a non-default foreground attribute as '#': the location
// overlay sets such an attribute, so every '#' in the rendered frame
// corresponds to a Rune-managed highlight cell. Cells the embedded
// editor renders without an overlay show their literal rune (vim's
// own attributes are stripped by ignoreAttrWriter under
// experimental_highlights=true).
//
// Skips when the local machine has no vim binary or no Go tree-sitter
// grammar artefacts.
func TestExoSyntaxHighlightsOverlayScrolling(t *testing.T) {
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim binary not available")
	}

	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = canonical

	const sample = `package main

import "fmt"

func main() {
	fmt.Println("hello")
	for i := 0; i < 10; i++ {
		fmt.Println(i)
	}
}
`
	path := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(path, []byte(sample), 0o644))

	const width, height = 60, 16
	c, h, mu := newExoGoTestComponent(t, dir, path, width, height)
	defer func() { _ = c.Close() }()

	// Every method that reads or writes the exo handler's
	// LocationStore (SetLocationList from the syntax goroutine,
	// LocationLists/Draw from the test) shares the schedule mutex
	// in production via ScheduleNextTick. The test mimics that by
	// running each observation under mu.
	locked := func(fn func()) {
		mu.Lock()
		defer mu.Unlock()
		fn()
	}

	// Allow vim to launch and the syntax tree's first highlight
	// pass to deliver a SetLocationList call. There is no portable
	// in-band readiness signal, and the syntax pipeline runs on the
	// host scheduler we drive, so a generous wall-clock wait keeps
	// the assertion below stable across machines.
	require.Eventually(t, func() bool {
		var ok bool
		locked(func() { ok = len(h.LocationLists()) > 0 })
		return ok
	}, 5*time.Second, 50*time.Millisecond,
		"syntax tree must publish a location list to the exo handler")

	// At rest the rendered frame must contain at least one
	// '#'-marked cell — i.e. the overlay actually drew. We don't
	// pin a full golden frame because vim's chrome (ruler, mode
	// line, ~ tildes) varies across versions; the structural claim
	// is that the overlay is on top of vim's content rows.
	require.Eventually(t, func() bool {
		var n int
		locked(func() { n = countHash(drawHandler(h, width, height)) })
		return n > 0
	}, 5*time.Second, 100*time.Millisecond,
		"first draw must overlay at least one syntax highlight cell")

	var beforeHashes int
	locked(func() {
		beforeHashes = countHash(drawHandler(h, width, height))
	})
	require.Positive(t, beforeHashes)

	send := func(keys ...term.KeyComb) {
		locked(func() { dispatchKeys(h, keys...) })
	}
	hashes := func() int {
		var n int
		locked(func() { n = countHash(drawHandler(h, width, height)) })
		return n
	}

	// Drive vim to the end of the buffer (G) then back to the
	// start (gg). Each scroll triggers an EventInterrupt that our
	// eventPublisher wrapper uses to refresh vteprobe, which in
	// turn re-projects the syntax location list. Highlights must
	// continue to render after each scroll.
	send(term.KeyComb{Key: term.KeyEsc})
	send(term.KeyComb{Ch: 'G'})
	require.Eventually(t, func() bool { return hashes() > 0 },
		5*time.Second, 100*time.Millisecond,
		"scroll-to-end must keep the overlay alive")

	send(term.KeyComb{Ch: 'g'}, term.KeyComb{Ch: 'g'})
	require.Eventually(t, func() bool { return hashes() > 0 },
		5*time.Second, 100*time.Millisecond,
		"scroll-to-start must keep the overlay alive")

	// j scrolls down one line; repeated j calls walk the cursor
	// through every line in the sample. After each move the probe
	// is refreshed via EventInterrupt and the overlay re-projects.
	for i := 0; i < strings.Count(sample, "\n"); i++ {
		send(term.KeyComb{Ch: 'j'})
		require.Eventually(t, func() bool { return hashes() > 0 },
			2*time.Second, 50*time.Millisecond,
			"j #%d must keep the overlay alive", i)
	}

	// k scrolls back up; the overlay must remain in sync the whole
	// way back to the top of the buffer.
	for i := 0; i < strings.Count(sample, "\n"); i++ {
		send(term.KeyComb{Ch: 'k'})
		require.Eventually(t, func() bool { return hashes() > 0 },
			2*time.Second, 50*time.Millisecond,
			"k #%d must keep the overlay alive", i)
	}
}

func dispatchKeys(h text.Handler, keys ...term.KeyComb) {
	for _, k := range keys {
		_, _ = h.Handle(exoKeyEvent(k))
	}
}

// exoKeyEvent renders a KeyComb into a term.Event with the Raw bytes
// vte expects. The vte input path falls back to ev.Raw for ordinary
// keys, so Ch alone is insufficient to drive the embedded editor.
func exoKeyEvent(k term.KeyComb) term.Event {
	ev := term.Event{
		Type: term.EventKey, Mod: k.Mod, Key: k.Key, Ch: k.Ch,
	}
	switch {
	case k.Key == term.KeyEsc:
		ev.Raw = []byte{0x1b}
	case k.Key == term.KeyEnter:
		ev.Raw = []byte{0x0d}
	case k.Ch != 0:
		ev.Raw = []byte(string(k.Ch))
	}
	return ev
}

func drawHandler(h text.Handler, width, height int) string {
	w := term.NewStringWriter(width, height)
	w.ForegroundCh = '#'
	_ = w.Clear(term.Attributes{})
	h.Draw(w)
	_ = w.Flush()
	return w.String()
}

func countHash(s string) int { return strings.Count(s, "#") }

// newExoGoTestComponent builds a text.Component whose Editor is a
// real exoeditor.Editor running vim, plugged into a real file scheme so
// the embedded vim can exec and the syntax tree can stat/read the
// underlying Go file. The returned text.Handler is the
// SubscribeLocationCommands-wrapped exo handler exposed to the rest
// of the IDE — calling Draw / Handle on it exercises the same
// surface workspace_handler does in production.
func newExoGoTestComponent(
	t *testing.T, dir, file string, width, height int,
) (*text.Component, text.Handler, *sync.Mutex) {
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	fileURI, err := workspaceapi.CurrentUserHostURI(file)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	mu := new(sync.Mutex)
	schedule := func(fn func()) bool {
		mu.Lock()
		defer mu.Unlock()
		fn()
		return true
	}

	ws := workspace.NewSchemeWorkspace(uri, scheme, schedule)

	vteCfg := vte.DefaultConfig()
	vteCfg.RingBell = func() {}

	pkgs := newInstalledPkgManager(t)
	syntaxCfg := defaultSyntaxConfig(schedule)

	tcfg := text.DefaultConfig()
	tcfg.ScheduleNextTick = schedule
	tcfg.Syntax = syntaxCfg
	tcfg.PkgManager = pkgs
	tcfg.EventPublisher = func(term.Event) bool { return true }

	ed := exoeditor.New(
		// -Nu NONE skips user vimrc; -n disables swap so tempdir
		// can be deleted without "swap file exists" prompts.
		`vim -Nu NONE -n {file}`,
		"<esc>:{line}<enter>{col}|",
		"<esc>:qa<enter>",
		schedule,
		ws,
		uri,
		nopBrowserNotifications{},
		exoeditor.PublisherFunc(func(term.Event) bool { return true }),
		scheme,
		scheme,
		stubExoTabManager{},
		vteCfg,
		stubExoReloader{},
		nil,
		true,
		nil, nil, nil,
	)

	c, err := text.NewComponent(ed, ws, tcfg)
	require.NoError(t, err)

	c.Browser().Resize(width, height)

	// Hold the schedule mutex across OpenFileTab so the syntax
	// tree's first SetLocationList callback (dispatched on a
	// separate goroutine via the same schedule) cannot race the
	// `handler` assignment inside text.Component.newFileBuffer.
	mu.Lock()
	_, err = c.OpenFileTab(fileURI, false)
	require.NoError(t, err)

	h, err := c.Editor(fileURI)
	require.NoError(t, err)
	h.Resize(width, height)
	mu.Unlock()
	return c, h, mu
}

// defaultSyntaxConfig mirrors newTestCase's syntax wiring so the
// tree-sitter pipeline parses and dispatches highlights through the
// caller-supplied scheduler.
func defaultSyntaxConfig(schedule func(func()) bool) syntax.Config {
	cfg := syntax.DefaultConfig()
	cfg.ScheduleNextTick = schedule
	cfg.ReparseOnErrors = false
	cfg.StrictErrors = true
	return cfg
}

// nopBrowserNotifications discards every notification routed through
// the exo handler — the test asserts on rendered frames, not on
// notification side effects.
type nopBrowserNotifications struct{}

func (nopBrowserNotifications) Notify(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return "", nil
}
func (nopBrowserNotifications) NotifyOnce(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return "", nil
}
func (nopBrowserNotifications) UpdateNotificationProgress(
	string, string, int64, int64,
) error {
	return nil
}

type stubExoTabManager struct{}

func (stubExoTabManager) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, nil
}
func (stubExoTabManager) SetTabName(
	workspaceapi.URI, string, term.Attributes,
) error {
	return nil
}

func (stubExoTabManager) OnTabExit(workspaceapi.URI) bool {
	return false
}

type stubExoReloader struct{}

func (stubExoReloader) Reload(workspaceapi.URI) error { return nil }
