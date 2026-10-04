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

package idelsp

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
)

func TestRootContains(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		root    string
		file    string
		contain bool
	}{
		{"identical", "file:///ws", "file:///ws", true},
		{"direct child", "file:///ws", "file:///ws/a.py", true},
		{"nested child", "file:///ws", "file:///ws/sub/a.py", true},
		{"trailing slash root", "file:///ws/", "file:///ws/a.py", true},
		{"sibling prefix not contained", "file:///ws", "file:///wsx/a.py", false},
		{"outside", "file:///ws/sub", "file:///ws/a.py", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.contain, rootContains(tt.root, tt.file))
		})
	}
}

func TestServerForURILongestRoot(t *testing.T) {
	t.Parallel()
	uri := makeURI(t, "file:///workspace")
	m := New(uri, nil, nil, nil, nil, nil, Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	rootSrv := &fakeChild{childName: "python"}
	nestedSrv := &fakeChild{childName: "python"}
	m.mu.Lock()
	m.servers[serverKey{languageID: "python", rootURI: "file:///workspace"}] = rootSrv
	m.servers[serverKey{languageID: "python", rootURI: "file:///workspace/sub"}] = nestedSrv
	m.mu.Unlock()

	nested, err := m.serverForURI("file:///workspace/sub/app.py")
	require.NoError(t, err)
	assert.Same(t, nestedSrv, nested, "nested file must route to nested root server")

	root, err := m.serverForURI("file:///workspace/top.py")
	require.NoError(t, err)
	assert.Same(t, rootSrv, root, "top-level file must route to workspace-root server")

	outside, err := m.serverForURI("file:///elsewhere/x.py")
	require.NoError(t, err)
	assert.Same(t, rootSrv, outside,
		"file outside any initialized root must fall back to the "+
			"broadest same-language server")

	_, err = m.serverForURI("file:///elsewhere/x.go")
	require.ErrorIs(t, err, ErrNoServer,
		"a language with no running server has no fallback")
}

func TestServerForURIBroadestFallback(t *testing.T) {
	t.Parallel()
	uri := makeURI(t, "file:///workspace")
	m := New(uri, nil, nil, nil, nil, nil, Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	aaSrv := &fakeChild{childName: "python"}
	abSrv := &fakeChild{childName: "python"}
	m.mu.Lock()
	m.servers[serverKey{languageID: "python", rootURI: "file:///workspace/ab"}] = abSrv
	m.servers[serverKey{languageID: "python", rootURI: "file:///workspace/aa"}] = aaSrv
	m.mu.Unlock()

	srv, err := m.serverForURI("file:///home/u/go/pkg/mod/dep/x.py")
	require.NoError(t, err)
	assert.Same(t, aaSrv, srv,
		"equal-length roots must tie-break lexicographically")

	rootSrv := &fakeChild{childName: "python"}
	m.mu.Lock()
	m.servers[serverKey{languageID: "python", rootURI: "file:///workspace"}] = rootSrv
	m.mu.Unlock()

	srv, err = m.serverForURI("file:///home/u/go/pkg/mod/dep/x.py")
	require.NoError(t, err)
	assert.Same(t, rootSrv, srv,
		"the shortest root must win once available")
}

func TestSendPendingOpensOutOfWorkspaceFallback(t *testing.T) {
	t.Parallel()
	uri := makeURI(t, "file:///workspace")
	m := New(uri, nil, nil, nil, nil, nil, Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	outsideURI := makeURI(t, "file:///goroot/lib/dep.py")
	m.mu.Lock()
	m.pendingOpens[outsideURI.String()] = textapi.Event{
		Type: textapi.EventTypeOpen, URI: outsideURI, Content: "x = 1\n"}
	m.mu.Unlock()

	srv := &langServer{
		cfg:     langConfig{id: "python"},
		rootURI: "file:///workspace",
		log:     slog.Default(),
	}
	m.sendPendingOpens(
		serverKey{languageID: "python", rootURI: "file:///workspace"}, srv)

	m.mu.Lock()
	_, stillPending := m.pendingOpens[outsideURI.String()]
	m.mu.Unlock()
	assert.False(t, stillPending,
		"out-of-workspace pending open must flush to the first "+
			"same-language server")
}

func TestInitializeRejectsRootOutsideWorkspace(t *testing.T) {
	t.Parallel()
	uri := makeURI(t, "file:///workspace")
	m := New(uri, nil, nil, nil, nil, nil, Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	params := semanticapi.InitializeParams{RootURI: "file:///other"}
	_, err := m.Initialize(t.Context(), params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the workspace")
}

func TestSendPendingOpensRootScoped(t *testing.T) {
	t.Parallel()
	uri := makeURI(t, "file:///workspace")
	m := New(uri, nil, nil, nil, nil, nil, Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	topURI := makeURI(t, "file:///workspace/top.py")
	nestedURI := makeURI(t, "file:///workspace/sub/app.py")
	m.mu.Lock()
	m.pendingOpens[topURI.String()] = textapi.Event{
		Type: textapi.EventTypeOpen, URI: topURI, Content: "x = 1\n"}
	m.pendingOpens[nestedURI.String()] = textapi.Event{
		Type: textapi.EventTypeOpen, URI: nestedURI, Content: "y = 2\n"}
	m.mu.Unlock()

	// A nested-root server must claim only the nested file and leave
	// the top-level file pending for the workspace-root server. The
	// server is unstarted (alive=false), so notify is a harmless
	// no-op after the pending entry is consumed.
	srv := &langServer{
		cfg:     langConfig{id: "python"},
		rootURI: "file:///workspace/sub",
		log:     slog.Default(),
	}
	m.sendPendingOpens(
		serverKey{languageID: "python", rootURI: "file:///workspace/sub"}, srv)

	m.mu.Lock()
	_, topStillPending := m.pendingOpens[topURI.String()]
	_, nestedConsumed := m.pendingOpens[nestedURI.String()]
	m.mu.Unlock()

	assert.True(t, topStillPending,
		"workspace-root file must remain pending for its own root server")
	assert.False(t, nestedConsumed,
		"nested file must be consumed by the nested-root server")
}

func TestInstallRestartedIsRootScoped(t *testing.T) {
	t.Parallel()
	uri := makeURI(t, "file:///workspace")
	m := New(uri, nil, nil, nil, nil, nil, Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	rootChild := &langServer{cfg: langConfig{id: "python"}, rootURI: "file:///workspace"}
	nestedChild := &langServer{
		cfg: langConfig{id: "python"}, rootURI: "file:///workspace/sub"}
	rootMLS := &multiLangServer{
		cfg:      langConfig{id: "python"},
		children: []server{rootChild},
	}
	nestedMLS := &multiLangServer{
		cfg:      langConfig{id: "python"},
		children: []server{nestedChild},
	}
	rootKey := serverKey{languageID: "python", rootURI: "file:///workspace"}
	nestedKey := serverKey{languageID: "python", rootURI: "file:///workspace/sub"}
	m.mu.Lock()
	m.servers[rootKey] = rootMLS
	m.servers[nestedKey] = nestedMLS
	m.mu.Unlock()

	replacement := &langServer{cfg: langConfig{id: "python"}, rootURI: "file:///workspace"}
	m.installRestarted(rootKey, rootChild, replacement)

	assert.Same(t, replacement, rootMLS.children[0].(*langServer),
		"restart must replace the crashed child in its own root")
	assert.Same(t, nestedChild, nestedMLS.children[0].(*langServer),
		"restarting the workspace-root child must not replace nested-root children")
}
