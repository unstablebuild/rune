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

package syntaxtest

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"
)

func newInstalledYAMLPkgManager(t *testing.T) *mockPkgManager {
	return newInstalledPkgManagerWithFiles(t,
		"yaml/tree-sitter.so",
		"yaml/highlights.scm",
		"yaml/indents.scm",
		"yaml/folds.scm",
		"yaml/locals.scm",
	)
}

func newYAMLTestCase(
	t *testing.T,
	pkgs syntax.PkgManager,
	width, height int,
	interrupt func(context.Context) error,
) (*sync.Mutex, *text.Component, func()) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)

	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	mu := new(sync.Mutex)
	scfg := syntax.DefaultConfig()
	scfg.ScheduleNextTick = func(fn func()) bool {
		mu.Lock()
		defer mu.Unlock()
		fn()
		return true
	}
	scfg.ReparseOnErrors = false
	scfg.StrictErrors = true

	ed := vi.Editor(
		vi.WithIndents(text.IndentConfig{"yaml": text.IndentRuneSpace}),
		vi.WithStatusBarConfig(true, text.StatusBarConfig{
			Publisher:        texttest.NopEditor(),
			ScheduleNextTick: scfg.ScheduleNextTick,
		}),
	)
	w := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
	tcfg := text.DefaultConfig()
	// Share scfg's mutex-serializing scheduler; see newTestCase in
	// go_test.go for why an independent inline scheduler races with
	// syntax.Tree's async parser init on the shared cell.Buffer.
	tcfg.ScheduleNextTick = scfg.ScheduleNextTick
	tcfg.Syntax = scfg
	tcfg.PkgManager = pkgs
	tcfg.EventPublisher = func(ev term.Event) bool {
		interrupt(context.Background())
		return true
	}
	comp, err := text.NewComponent(ed, w, tcfg)
	require.NoError(t, err)

	comp.Browser().Resize(width, height)

	return mu, comp, func() {
		_ = comp.Close()
		_ = scheme.Close()
	}
}

func TestYAMLEnterUsesSpaceIndentIntegration(t *testing.T) {
	pkgs := newInstalledYAMLPkgManager(t)

	var wg sync.WaitGroup
	ready := func(context.Context) error {
		wg.Done()
		return nil
	}

	const width, height = 40, 12
	mu, comp, cleanup := newYAMLTestCase(t, pkgs, width, height, ready)
	defer cleanup()

	const yamlContent = "root:\n  child:"

	wg.Add(1)
	mu.Lock()
	_, h := newEditFileName(t, mu, comp, yamlContent, "config.yaml")
	mu.Unlock()
	wg.Wait()

	keys, err := term.ParseKeys("GA<enter>x<esc>")
	require.NoError(t, err)
	for _, key := range keys {
		ev := term.Event{Type: term.EventKey, Ch: key.Ch, Key: key.Key, Mod: key.Mod}
		_, handled := h.Handle(ev)
		require.True(t, handled, "%s", ev.KeyComb().String())
	}

	actual := h.CellView().String()
	assert.Equal(t, "root:\n  child:\n    x", actual)
	assert.NotContains(t, actual, "\t")
}

func TestYAMLShiftRightUsesSpaceIndentIntegration(t *testing.T) {
	pkgs := newInstalledYAMLPkgManager(t)

	var wg sync.WaitGroup
	ready := func(context.Context) error {
		wg.Done()
		return nil
	}

	const width, height = 40, 12
	mu, comp, cleanup := newYAMLTestCase(t, pkgs, width, height, ready)
	defer cleanup()

	const yamlContent = "root:\nchild:"

	wg.Add(1)
	mu.Lock()
	_, h := newEditFileName(t, mu, comp, yamlContent, "config.yaml")
	mu.Unlock()
	wg.Wait()

	// Move to last line and run `>>` to indent it one level.
	keys, err := term.ParseKeys(`G\>\>`)
	require.NoError(t, err)
	for _, key := range keys {
		ev := term.Event{Type: term.EventKey, Ch: key.Ch, Key: key.Key, Mod: key.Mod}
		_, handled := h.Handle(ev)
		require.True(t, handled, "%s", ev.KeyComb().String())
	}

	actual := h.CellView().String()
	assert.NotContains(t, actual, "\t", "YAML shift-right must not introduce tabs")
	assert.Contains(t, actual, "    child:")
}

func TestYAMLVisualShiftRightUsesSpaceIndentIntegration(t *testing.T) {
	pkgs := newInstalledYAMLPkgManager(t)

	var wg sync.WaitGroup
	ready := func(context.Context) error {
		wg.Done()
		return nil
	}

	const width, height = 40, 12
	mu, comp, cleanup := newYAMLTestCase(t, pkgs, width, height, ready)
	defer cleanup()

	const yamlContent = "root:\nchild:\nother:"

	wg.Add(1)
	mu.Lock()
	_, h := newEditFileName(t, mu, comp, yamlContent, "config.yaml")
	mu.Unlock()
	wg.Wait()

	// Visual-line select last two lines and indent once.
	keys, err := term.ParseKeys(`jVj\>`)
	require.NoError(t, err)
	for _, key := range keys {
		ev := term.Event{Type: term.EventKey, Ch: key.Ch, Key: key.Key, Mod: key.Mod}
		_, handled := h.Handle(ev)
		require.True(t, handled, "%s", ev.KeyComb().String())
	}

	actual := h.CellView().String()
	assert.NotContains(t, actual, "\t", "YAML visual shift-right must not introduce tabs")
	assert.Contains(t, actual, "    child:")
	assert.Contains(t, actual, "    other:")
}
