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

package idecursor

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

type stubWorkspaceManager struct{}

func (stubWorkspaceManager) AddWorkspace(context.Context, workspaceapi.URI) (workspace.Workspace, error) {
	return nil, nil
}
func (stubWorkspaceManager) Workspace(workspaceapi.URI) (workspace.Workspace, bool, error) {
	return nil, true, nil
}
func (stubWorkspaceManager) RegisterScheme(string, schemeapi.SchemeFunc) error {
	return nil
}
func (stubWorkspaceManager) UnregisterScheme(string) error       { return nil }
func (stubWorkspaceManager) IncrementReference(workspaceapi.URI) {}
func (stubWorkspaceManager) DecrementReference(workspaceapi.URI) error {
	return nil
}
func (stubWorkspaceManager) RemoveWorkspace(workspaceapi.URI) (workspace.Workspace, bool) {
	return nil, false
}

type stubOpener struct{ opened []workspaceapi.URI }

func (s *stubOpener) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	s.opened = append(s.opened, uri)
	return texttest.NewTestHandler(), nil
}

type stubWM struct{ set bool }

func (stubWM) Focus() (browserapi.Window, error) { return stubWindow(1), nil }
func (stubWM) Split(browserapi.Orientation, browserapi.Window, browserapi.Handler) (browserapi.Window, error) {
	return nil, nil
}
func (stubWM) Floating(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error) {
	return stubWindow(2), nil
}
func (stubWM) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (stubWM) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return nil, nil
}
func (s *stubWM) SetWindowContent(browserapi.Window, browserapi.Handler) error {
	s.set = true
	return nil
}
func (stubWM) CloseWindow(browserapi.Window) error { return nil }

func (stubWM) SetTabActivity(workspaceapi.URI, bool) error { return nil }

type stubWindow uint64

func (s stubWindow) WindowID() uint64 { return uint64(s) }

type stubFS struct{}

func (stubFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + path)
}
func (stubFS) OpenFile(string, int, os.FileMode) (workspaceapi.File, error) {
	return nil, assert.AnError
}
func (stubFS) Remove(string) error                   { return nil }
func (stubFS) Stat(string) (os.FileInfo, error)      { return nil, nil }
func (stubFS) ReadDir(string) ([]os.DirEntry, error) { return nil, nil }
func (stubFS) MkdirAll(string, os.FileMode) error    { return nil }

func TestWithHistorySubscribes(t *testing.T) {
	var subscribed bool
	ed := texttest.NopEditorWithCallback(func() { subscribed = true })
	ws, err := workspaceapi.ParseURI("file:///workspace")
	require.NoError(t, err)
	closer, err := WithHistory(ed, storagestub.NewInMemoryService(), &stubOpener{}, &stubWM{}, stubFS{}, nil, stubWorkspaceManager{}, ws, func(fn func()) bool { fn(); return true })
	require.NoError(t, err)
	require.NoError(t, closer.Close())
	assert.True(t, subscribed)
}

func TestHandlerCompleteOrdersEntries(t *testing.T) {
	ws, err := workspaceapi.ParseURI("file:///workspace")
	require.NoError(t, err)
	h := &handler{workspaceURI: ws}
	h.doc = historyDocument{WorkspaceURI: ws.String(), Entries: []location{
		{URI: "file:///workspace/a.go", Cursor: term.Coordinates{Y: 1, X: 1}},
		{URI: "file:///workspace/b.go", Cursor: term.Coordinates{Y: 2, X: 2}},
		{URI: "file:///workspace/c.go", Cursor: term.Coordinates{Y: 3, X: 3}},
	}, Index: 1}

	// Top-level complete returns empty (framework auto-appends subcommands).
	iter, _, err := h.Complete(context.Background(), textapi.Command{Name: commandName, Args: nil})
	require.NoError(t, err)
	var topLevel []string
	for v, ok := iter.Next(context.Background()); ok; v, ok = iter.Next(context.Background()) {
		topLevel = append(topLevel, v)
	}
	assert.Empty(t, topLevel, "top-level complete should be empty")

	// Subcommand "prev" returns locations in prev-first order.
	iter, _, err = h.Complete(context.Background(), textapi.Command{Name: commandName, Args: []string{"prev"}})
	require.NoError(t, err)
	vals := []string{}
	for v, ok := iter.Next(context.Background()); ok; v, ok = iter.Next(context.Background()) {
		vals = append(vals, v)
	}
	assert.Equal(t, []string{"a.go:2:2", "c.go:4:4"}, vals)
}

// retryCreateOnceStore wraps an in-memory storageapi.Service and
// simulates the firstmover retry-on-transient-failure behaviour that
// can produce ErrAlreadyExists on the second Create attempt: the
// underlying write actually succeeded on the first call, but the
// caller saw a transport error and the retry now races with itself.
// load must treat that ErrAlreadyExists as success rather than
// surfacing it as a fatal init error to the workspace manager.
type retryCreateOnceStore struct {
	storageapi.Service
	createAlreadyExistsOnce bool
}

func (s *retryCreateOnceStore) Create(ctx context.Context, id string, doc any) error {
	if !s.createAlreadyExistsOnce {
		s.createAlreadyExistsOnce = true
		// Simulate that the first Create succeeded on the backend
		// but the retry sees the doc already exists.
		if err := s.Service.Create(ctx, id, doc); err != nil {
			return err
		}
		return storageapi.ErrAlreadyExists
	}
	return s.Service.Create(ctx, id, doc)
}

func TestWithHistoryToleratesAlreadyExistsOnCreate(t *testing.T) {
	store := &retryCreateOnceStore{Service: storagestub.NewInMemoryService()}
	ed := texttest.NopEditorWithCallback(func() {})
	ws, err := workspaceapi.ParseURI("file:///workspace")
	require.NoError(t, err)
	closer, err := WithHistory(ed, store, &stubOpener{}, &stubWM{}, stubFS{},
		nil, stubWorkspaceManager{}, ws,
		func(fn func()) bool { fn(); return true })
	require.NoError(t, err, "ErrAlreadyExists on retried Create must "+
		"not propagate as a load failure")
	require.NotNil(t, closer)
	require.NoError(t, closer.Close())

	// Sanity: the document the second WithHistory would see is the
	// one persisted by the first Create attempt — not a brand-new
	// in-memory shadow that drops history.
	var doc historyDocument
	require.NoError(t, store.Get(context.Background(), documentID(ws), &doc))
	assert.Equal(t, ws.String(), doc.WorkspaceURI)
}

// TestHandleSkipsSkipListedURI reproduces the ctrl-i/o file-explorer
// bug: the file explorer's pseudo-resource (memory:///fexplorer) must
// never enter the cursor history. Recording it would let a later
// prev/next navigation call opener.Open on the pseudo-URI, which
// re-registers the explorer's per-file commands ("command already
// registered") and re-opens the swap-locked buffer ("file is already
// open by another process").
func TestHandleSkipsSkipListedURI(t *testing.T) {
	ws, err := workspaceapi.ParseURI("file:///workspace")
	require.NoError(t, err)
	skipURI, err := workspaceapi.ParseURI("memory:///fexplorer")
	require.NoError(t, err)
	fileURI, err := workspaceapi.ParseURI("file:///workspace/a.go")
	require.NoError(t, err)

	h := &handler{
		store:        storagestub.NewInMemoryService(),
		workspaceURI: ws,
		docID:        documentID(ws),
		doc:          newHistoryDocument(ws),
		skip:         []workspaceapi.URI{skipURI},
	}

	// Seed the history with a real file so a subsequent jump would be
	// recorded if the pseudo-URI were not skipped.
	h.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeOpen, URI: fileURI})
	// A cursor event on the file explorer pseudo-URI must be dropped.
	h.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  skipURI,
		From: term.Coordinates{Y: 40},
	})

	for _, e := range h.doc.Entries {
		assert.NotEqual(t, skipURI.String(), e.URI,
			"file explorer pseudo-URI must not be recorded in cursor history")
	}
}

// TestHandleSkipsURIsUnderSkippedPrefix covers pseudo-buffers that mint a
// resource per invocation, such as the :gitshow diff popup: the skip
// entry names the namespace because the individual URIs cannot be
// enumerated up front.
func TestHandleSkipsURIsUnderSkippedPrefix(t *testing.T) {
	ws, err := workspaceapi.ParseURI("file:///workspace")
	require.NoError(t, err)
	prefix, err := workspaceapi.ParseURI("memory:///gitshow")
	require.NoError(t, err)
	popup, err := workspaceapi.ParseURI("memory:///gitshow/a.go.diff?n=2")
	require.NoError(t, err)
	fileURI, err := workspaceapi.ParseURI("file:///workspace/a.go")
	require.NoError(t, err)

	h := &handler{
		store:        storagestub.NewInMemoryService(),
		workspaceURI: ws,
		docID:        documentID(ws),
		doc:          newHistoryDocument(ws),
		skip:         []workspaceapi.URI{prefix},
	}

	h.Handle(context.Background(), textapi.Event{Type: textapi.EventTypeOpen, URI: fileURI})
	h.Handle(context.Background(), textapi.Event{
		Type: textapi.EventTypeCursor,
		URI:  popup,
		From: term.Coordinates{Y: 40},
	})

	for _, e := range h.doc.Entries {
		assert.NotEqual(t, popup.String(), e.URI,
			"a resource under a skipped namespace must not enter cursor history")
	}
}
