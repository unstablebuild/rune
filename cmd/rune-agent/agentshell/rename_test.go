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

package agentshell

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
)

// renameTestStore is a map-backed dialoguemanager.Store for rename tests.
type renameTestStore struct {
	mu        sync.Mutex
	dialogues map[string]dialoguemanager.Dialogue
}

func newRenameTestStore(ds ...dialoguemanager.Dialogue) *renameTestStore {
	s := &renameTestStore{dialogues: make(map[string]dialoguemanager.Dialogue)}
	for _, d := range ds {
		s.dialogues[d.ID] = d
	}
	return s
}

func (s *renameTestStore) Health(context.Context) error { return nil }
func (s *renameTestStore) Create(_ context.Context, d dialoguemanager.Dialogue) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dialogues[d.ID] = d
	return nil
}
func (s *renameTestStore) Get(_ context.Context, id string) (dialoguemanager.Dialogue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.dialogues[id]
	if !ok {
		return dialoguemanager.Dialogue{}, storageapi.ErrNotFound
	}
	return d, nil
}
func (s *renameTestStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.dialogues, id)
	return nil
}
func (s *renameTestStore) AppendMessages(
	context.Context, dialoguemanager.Dialogue, []llmapi.Message, llmapi.DialogueUsage,
) error {
	return nil
}
func (s *renameTestStore) ArchiveAndReplace(
	context.Context, dialoguemanager.ArchiveAndReplaceParams,
) error {
	return nil
}
func (s *renameTestStore) List(
	context.Context,
) (iterator.Iterator[dialoguemanager.DialogueHeader], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	headers := make([]dialoguemanager.DialogueHeader, 0, len(s.dialogues))
	for _, d := range s.dialogues {
		headers = append(headers, d.Header())
	}
	return iterator.FromSlice(headers), nil
}
func (s *renameTestStore) SetTitle(_ context.Context, id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.dialogues[id]
	if !ok {
		return storageapi.ErrNotFound
	}
	d.Title = title
	s.dialogues[id] = d
	return nil
}

// captureWindowManager records floating windows so tests can drive the
// popup's handler and Close callback directly.
type captureWindowManager struct {
	stubWindowManager
	floating  browserapi.Floating
	cfg       browserapi.FloatingConfig
	closedWin []browserapi.Window
}

func (w *captureWindowManager) Floating(
	f browserapi.Floating, cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	w.floating = f
	w.cfg = cfg
	return fakeWindow(1), nil
}
func (w *captureWindowManager) CloseWindow(win browserapi.Window) error {
	w.closedWin = append(w.closedWin, win)
	return nil
}

type fakeWindow uint64

func (w fakeWindow) WindowID() uint64 { return uint64(w) }

var _ browserapi.WindowManager = (*captureWindowManager)(nil)

func keyEvent(key term.Key) term.Event {
	return term.Event{Type: term.EventKey, Key: key}
}

func TestRenameConversationSetsTitle(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore(dialoguemanager.Dialogue{
		ID: "rolling-fox", Model: "test-model",
	})
	var retitled []string
	s := &shell{
		store: store,
		retitleTab: func(_ context.Context, id string) {
			retitled = append(retitled, id)
		},
	}

	it, err := s.renameConversation(ctx, []string{"rolling-fox", "fix", "the", "flaky", "test"})
	require.NoError(t, err)
	_ = it.Close()

	d, err := store.Get(ctx, "rolling-fox")
	require.NoError(t, err)
	assert.Equal(t, "fix the flaky test", d.Title)
	assert.Equal(t, []string{"rolling-fox"}, retitled,
		"an open chat tab is relabeled live")
}

func TestRenameConversationClearTitleRetitlesTab(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore(dialoguemanager.Dialogue{
		ID: "rolling-fox", Model: "test-model", Title: "old name",
	})
	wm := &captureWindowManager{}
	var retitled []string
	s := &shell{
		store: store,
		wm:    wm,
		retitleTab: func(_ context.Context, id string) {
			retitled = append(retitled, id)
		},
	}

	it, err := s.renameConversation(ctx, []string{"rolling-fox"})
	require.NoError(t, err)
	_ = it.Close()
	require.NotNil(t, wm.floating)

	// An empty commit falls the label back to the dialogue ID.
	for range "old name" {
		wm.floating.Handle(keyEvent(term.KeyBackspace))
	}
	exit, handled := wm.floating.Handle(keyEvent(term.KeyEnter))
	assert.True(t, exit && handled)
	require.NoError(t, wm.floating.Close())

	assert.Equal(t, []string{"rolling-fox"}, retitled)
}

func TestRenameConversationUnknownID(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore()
	s := &shell{store: store, wm: stubWindowManager{}}

	_, err := s.renameConversation(ctx, []string{"ghost", "name"})
	assert.ErrorContains(t, err, "no saved messages yet")
}

func TestRenameConversationPopupUnsavedChat(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore()
	wm := &captureWindowManager{}
	s := &shell{store: store, wm: wm}

	// A chat opened but never messaged has no document.
	_, err := s.renameConversation(ctx, []string{"unsaved-chat"})
	assert.ErrorContains(t, err, "no saved messages yet")
	assert.Nil(t, wm.floating, "no popup for an unsaved conversation")
}

func TestResolveChatsID(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore(
		dialoguemanager.Dialogue{ID: "rolling-fox"},
		dialoguemanager.Dialogue{ID: "quiet-owl"},
	)
	s := &shell{store: store}

	for _, tc := range []struct {
		name     string
		args     []string
		wantID   string
		wantRest string
	}{
		{"bare id", []string{"rolling-fox"}, "rolling-fox", ""},
		{"bare id with args", []string{"rolling-fox", "x"}, "rolling-fox", "x"},
		{"one-token named", []string{"my title (quiet-owl)"}, "quiet-owl", ""},
		{"one-token named with args", []string{"my title (quiet-owl)", "m"}, "quiet-owl", "m"},
		{"split named", []string{"my", "title", "(quiet-owl)"}, "quiet-owl", ""},
		{"unknown bare id", []string{"ghost", "x"}, "ghost", "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, rest := s.resolveChatsID(ctx, tc.args)
			assert.Equal(t, tc.wantID, id)
			assert.Equal(t, tc.wantRest, strings.Join(rest, " "))
		})
	}
}

func TestRenameConversationPopupCommit(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore(dialoguemanager.Dialogue{
		ID: "rolling-fox", Model: "test-model", Title: "old name",
	})
	wm := &captureWindowManager{}
	var retitled []string
	s := &shell{
		store: store,
		wm:    wm,
		retitleTab: func(_ context.Context, id string) {
			retitled = append(retitled, id)
		},
	}

	it, err := s.renameConversation(ctx, []string{"rolling-fox"})
	require.NoError(t, err)
	_ = it.Close()

	require.NotNil(t, wm.floating, "rename must open a floating window")
	assert.Equal(t, "Rename conversation", wm.cfg.Title)

	// Prefilled with the current title.
	for range "old name" {
		wm.floating.Handle(keyEvent(term.KeyBackspace))
	}
	for _, r := range "new name" {
		wm.floating.Handle(term.Event{Type: term.EventKey, Ch: r})
	}
	exit, handled := wm.floating.Handle(keyEvent(term.KeyEnter))
	assert.True(t, exit && handled)
	require.NoError(t, wm.floating.Close())

	d, err := store.Get(ctx, "rolling-fox")
	require.NoError(t, err)
	assert.Equal(t, "new name", d.Title)
	assert.Len(t, wm.closedWin, 1, "the popup closes itself on commit")
	assert.Equal(t, []string{"rolling-fox"}, retitled)
}

func TestRenameConversationPopupCancel(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore(dialoguemanager.Dialogue{
		ID: "rolling-fox", Title: "old name",
	})
	wm := &captureWindowManager{}
	s := &shell{store: store, wm: wm}

	it, err := s.renameConversation(ctx, []string{"rolling-fox"})
	require.NoError(t, err)
	_ = it.Close()
	require.NotNil(t, wm.floating)

	exit, handled := wm.floating.Handle(keyEvent(term.KeyEsc))
	assert.True(t, exit && handled)
	require.NoError(t, wm.floating.Close())

	d, err := store.Get(ctx, "rolling-fox")
	require.NoError(t, err)
	assert.Equal(t, "old name", d.Title, "Esc must not commit the rename")
	assert.Len(t, wm.closedWin, 1)
}

func TestRenameInputBoxCtrlCAborts(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore(dialoguemanager.Dialogue{ID: "rolling-fox"})
	wm := &captureWindowManager{}
	s := &shell{store: store, wm: wm}

	it, err := s.renameConversation(ctx, []string{"rolling-fox"})
	require.NoError(t, err)
	_ = it.Close()
	require.NotNil(t, wm.floating)

	exit, handled := wm.floating.Handle(
		term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl})
	assert.True(t, exit && handled)
	require.NoError(t, wm.floating.Close())

	d, err := store.Get(ctx, "rolling-fox")
	require.NoError(t, err)
	assert.Empty(t, d.Title, "Ctrl-C must abort without renaming")
}

func TestListConversationsShowsNamedID(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore(
		dialoguemanager.Dialogue{ID: "rolling-fox", Title: "fix the flaky test"},
		dialoguemanager.Dialogue{ID: "quiet-owl"},
	)
	s := &shell{store: store}

	it, err := s.listConversations(ctx)
	require.NoError(t, err)
	items, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, items, 1, "listConversations returns one markdown block")

	w := term.NewStringWriter(80, 20)
	items[0].Resize(80, 20)
	items[0].Draw(w)
	require.NoError(t, w.Flush())
	got := w.String()
	assert.Contains(t, got, "fix the flaky test (rolling-fox)")
	assert.Contains(t, got, "quiet-owl")
}

func TestCompleteDialogueIDsShowsNamedID(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore(
		dialoguemanager.Dialogue{ID: "rolling-fox", Title: "fix the flaky test"},
		dialoguemanager.Dialogue{ID: "quiet-owl"},
	)
	s := &shell{store: store}

	it, err := s.completeDialogueIDs(ctx)
	require.NoError(t, err)
	got, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{"fix the flaky test (rolling-fox)", "quiet-owl"}, got)
}

func TestChatsRenameSubcommand(t *testing.T) {
	ctx := context.Background()
	store := newRenameTestStore(dialoguemanager.Dialogue{ID: "rolling-fox"})
	var retitled []string
	s := &shell{
		store: store,
		retitleTab: func(_ context.Context, id string) {
			retitled = append(retitled, id)
		},
	}

	it, err := s.handleChats(ctx, []string{"rename", "rolling-fox", "fix", "flaky"})
	require.NoError(t, err)
	_ = it.Close()

	d, err := store.Get(ctx, "rolling-fox")
	require.NoError(t, err)
	assert.Equal(t, "fix flaky", d.Title)
	assert.Equal(t, []string{"rolling-fox"}, retitled)
}

func TestChatsRenameUsageError(t *testing.T) {
	s := &shell{store: newRenameTestStore()}
	_, err := s.handleChats(context.Background(), []string{"rename"})
	assert.ErrorContains(t, err, "usage: chats rename")
}
