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

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"unstable.build/rune/internal/extension/langext/langexttest"
)

// captureLSP records the InitializeParams it received and otherwise
// no-ops every request, so the discovery tests assert against the
// captured params without running a real gopls. The inits channel lets a
// test synchronize on the asynchronous, event-driven bring-up.
type captureLSP struct {
	noopLSP
	mu         sync.Mutex
	initParams *semanticapi.InitializeParams
	initCount  int
	inits      chan struct{}
}

func (l *captureLSP) Initialize(
	_ context.Context, p semanticapi.InitializeParams,
) (semanticapi.InitializeResult, error) {
	l.mu.Lock()
	cp := p
	l.initParams = &cp
	l.initCount++
	signal := l.inits
	l.mu.Unlock()
	if signal != nil {
		signal <- struct{}{}
	}
	return semanticapi.InitializeResult{}, nil
}

func (l *captureLSP) captured() (semanticapi.InitializeParams, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.initParams == nil {
		return semanticapi.InitializeParams{}, l.initCount
	}
	return *l.initParams, l.initCount
}

// waitForInit blocks until the next Initialize call completes or the
// timeout elapses, so tests can synchronize on the event-driven bring-up.
func (l *captureLSP) waitForInit(t *testing.T, timeout time.Duration) {
	t.Helper()
	l.mu.Lock()
	if l.inits == nil {
		l.inits = make(chan struct{}, 1)
	}
	signal := l.inits
	l.mu.Unlock()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for lsp.Initialize")
	}
}

func TestExtendWorkspaceNestedDiscovery(t *testing.T) {
	root := t.TempDir()
	mod := filepath.Join(root, "services", "api")
	require.NoError(t, os.MkdirAll(mod, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(mod, "go.mod"), []byte("module example.com/api\n\ngo 1.22\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(mod, "main.go"), []byte("package main\n"), 0o644))

	stray := filepath.Join(root, "stray")
	require.NoError(t, os.MkdirAll(stray, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(stray, "loose.go"), []byte("package stray\n"), 0o644))

	scheme := newTestSchemeRooted(root)
	lsp := &captureLSP{}
	editor := newMockEditor()
	ext := &goExtension{}

	registered := false
	err := ext.extendWorkspaceWith(context.Background(),
		scheme, scheme, &mockNotifications{}, lsp, editor,
		nil, nil, nil, nil, &langexttest.Installer{}, nil,
		func(textapi.CommandManual, textapi.CommandHandler) error {
			registered = true
			return nil
		})
	require.NoError(t, err)
	assert.True(t, registered, "the go command must register independent of any module")

	// No root go.mod, so nothing is initialized on startup.
	_, count := lsp.captured()
	require.Zero(t, count)

	// A .go with no enclosing go.mod must not spawn a server.
	editor.open(t, filepath.Join(stray, "loose.go"))

	// Opening the nested module's source initializes gopls rooted there.
	editor.open(t, filepath.Join(mod, "main.go"))
	lsp.waitForInit(t, 5*time.Second)

	params, count := lsp.captured()
	require.Equal(t, 1, count, "only the marked nested module must initialize")
	assert.Equal(t, "file://"+mod, params.RootURI)
}
