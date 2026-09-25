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

package extension

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
)

// reviewWindowManager records the floating windows opened by /diff.
type reviewWindowManager struct {
	recordingWindowManager
	floatings []browserapi.Floating
	configs   []browserapi.FloatingConfig
	closes    int
	failWith  error
}

func (m *reviewWindowManager) Floating(
	f browserapi.Floating, cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	if m.failWith != nil {
		return nil, m.failWith
	}
	m.floatings = append(m.floatings, f)
	m.configs = append(m.configs, cfg)
	return nil, nil
}

func (m *reviewWindowManager) CloseWindow(browserapi.Window) error {
	m.closes++
	return nil
}

func (m *reviewWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

// countingEditor wraps a real editor so tests can count handler closes
// and simulate an editor that refuses to open.
type countingEditor struct {
	ed       text.Editor
	closed   int
	failEdit error
	lastBuf  *cell.Buffer
}

func (e *countingEditor) SubscribeEvents(
	ts []textapi.EventType, h text.EventHandler,
) error {
	return e.ed.SubscribeEvents(ts, h)
}

func (e *countingEditor) UnsubscribeEvents(h text.EventHandler) (bool, error) {
	return e.ed.UnsubscribeEvents(h)
}

func (e *countingEditor) Edit(
	ctx context.Context, file workspaceapi.URI,
	buf *cell.Buffer, readOnly, recovered bool,
) (text.Handler, error) {
	if e.failEdit != nil {
		return nil, e.failEdit
	}
	h, err := e.ed.Edit(ctx, file, buf, readOnly, recovered)
	if err != nil {
		return nil, err
	}
	e.lastBuf = buf
	return &countingHandler{Handler: h, editor: e}, nil
}

func (e *countingEditor) Editor(uri workspaceapi.URI) (text.Handler, error) {
	return e.ed.Editor(uri)
}

func (e *countingEditor) SubscribeCommand(
	m textapi.CommandManual, h text.CommandHandler,
) error {
	return e.ed.SubscribeCommand(m, h)
}

func (e *countingEditor) RegisterREPLCommand(
	m textapi.CommandManual, h textapi.REPLHandler,
) error {
	return e.ed.RegisterREPLCommand(m, h)
}

func (e *countingEditor) UnsubscribeCommand(name string) error {
	return e.ed.UnsubscribeCommand(name)
}

func (e *countingEditor) UnregisterREPLCommand(name string) error {
	return e.ed.UnregisterREPLCommand(name)
}

func (e *countingEditor) IsExternal() bool { return e.ed.IsExternal() }

type countingHandler struct {
	text.Handler
	editor *countingEditor
}

func (h *countingHandler) Close() error {
	h.editor.closed++
	return h.Handler.Close()
}

func newReviewAdapter(
	t *testing.T, msgs []llmapi.Message,
) (*commandAdapter, *reviewWindowManager, *countingEditor, *[]dialoguetui.Attachment) {
	t.Helper()
	store := newMemDialogueStore()
	require.NoError(t, store.Create(context.Background(), dialoguemanager.Dialogue{
		ID: "rolling-fox", Messages: msgs,
	}))
	// The review drops changes the workspace has no trace of, so the
	// fixture holds what each patch these tests replay produced.
	dir := t.TempDir()
	for name, content := range map[string]string{
		"foo.go":    "ctx\nnew\n",
		"server.go": "func serve() {\n\tconst new = \"b\"\n}\n",
	} {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, name), []byte(content), 0o600))
	}
	wm := &reviewWindowManager{}
	ed := &countingEditor{
		ed: standard.Editor(standard.WithClipboard(clipboard.NewInMemory())),
	}
	var attached []dialoguetui.Attachment
	a := &commandAdapter{
		dialogueID:    "rolling-fox",
		fs:            testLocalFS{root: dir},
		cwd:           dirURI(dir),
		skillRegistry: skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		store:         store,
		wm:            wm,
		editor:        ed,
		attachFn: func(at dialoguetui.Attachment) {
			attached = append(attached, at)
		},
	}
	return a, wm, ed, &attached
}

func patchedConversation(t *testing.T) []llmapi.Message {
	t.Helper()
	return []llmapi.Message{
		assistant(patchCall(t, "c1", updatePatch)),
		toolResult("c1", okResult),
	}
}

const wantConversationDiff = "apply_patch #1 [applied]\n" +
	"*** Update File: foo.go\n" +
	"@@ func main\n" +
	" ctx\n" +
	"-old\n" +
	"+new\n"

// openReview runs /reviewchanges and returns the floating handler the
// adapter opened.
func openReview(t *testing.T, a *commandAdapter, wm *reviewWindowManager) browserapi.Floating {
	t.Helper()
	before := len(wm.floatings)
	_, err := a.HandleCommand(context.Background(), "reviewchanges", nil)
	require.NoError(t, err)
	require.Len(t, wm.floatings, before+1)
	f := wm.floatings[before]
	f.Resize(80, 20)
	return f
}

func typeRunes(h tui.Handler, s string) {
	for _, r := range s {
		h.Handle(term.Event{Type: term.EventKey, Ch: r})
	}
}

func TestCommandAdapterReviewChangesOpensCenteredFloating(t *testing.T) {
	a, wm, _, _ := newReviewAdapter(t, patchedConversation(t))

	f := openReview(t, a, wm)

	assert.Equal(t, component.AlignmentCentered, wm.configs[0].Alignment)
	w, h := f.Dimensions()
	assert.Positive(t, w)
	assert.Positive(t, h)

	buf := term.NewStringWriter(80, 20)
	f.Draw(buf)
	require.NoError(t, buf.Flush())
	assert.Contains(t, buf.String(), "*** Update File: foo.go")
	assert.Contains(t, buf.String(), "-old")
	assert.Contains(t, buf.String(), "+new")
}

func TestCommandAdapterReviewChangesBufferKeepsDiffStyling(t *testing.T) {
	a, wm, _, _ := newReviewAdapter(t, patchedConversation(t))

	f := openReview(t, a, wm)

	w := term.NewStringWriter(80, 20)
	f.Draw(w)
	require.NoError(t, w.Flush())

	var sawAdd, sawDel bool
	for _, c := range w.Cells() {
		switch c.Ch {
		case '+':
			sawAdd = sawAdd || c.Bg == term.GetColor("darkgreen")
		case '-':
			sawDel = sawDel || c.Bg == term.GetColor("darkred")
		}
	}
	assert.True(t, sawAdd, "insertions must carry the dark green tint")
	assert.True(t, sawDel, "deletions must carry the dark red tint")
}

func TestCommandAdapterReviewChangesCloseWithoutEditsAddsNoAttachment(t *testing.T) {
	a, wm, ed, attached := newReviewAdapter(t, patchedConversation(t))

	f := openReview(t, a, wm)
	require.NoError(t, f.Close())

	assert.Empty(t, *attached)
	assert.Equal(t, 1, ed.closed)
	assert.Equal(t, 1, wm.closes)
}

func TestCommandAdapterReviewChangesEditUndoneAddsNoAttachment(t *testing.T) {
	a, wm, ed, attached := newReviewAdapter(t, patchedConversation(t))

	f := openReview(t, a, wm)
	typeRunes(f, "note")
	require.NotEqual(t, wantConversationDiff, ed.lastBuf.String())
	for range 4 {
		f.Handle(term.Event{Type: term.EventKey, Key: term.KeyBackspace})
	}
	require.Equal(t, wantConversationDiff, ed.lastBuf.String())
	require.NoError(t, f.Close())

	assert.Empty(t, *attached)
}

func TestCommandAdapterReviewChangesEditAddsCommentsAttachment(t *testing.T) {
	a, wm, ed, attached := newReviewAdapter(t, patchedConversation(t))

	f := openReview(t, a, wm)
	typeRunes(f, "why? ")
	edited := ed.lastBuf.String()
	require.NoError(t, f.Close())

	require.Len(t, *attached, 1)
	got := (*attached)[0]
	assert.Equal(t, chatReviewAttachmentID, got.ID)
	assert.Equal(t, chatReviewAttachmentName, got.Name)
	assert.Equal(t, '\uf4d2', got.Icon)
	assert.Empty(t, got.Path)
	assert.Equal(t, edited, got.Content)
	assert.Contains(t, got.Content, "why?")
	assert.Contains(t, got.Content, "*** Update File: foo.go")
}

// Reopening /diff must reuse the same attachment identity so the strip
// never accumulates duplicates.
func TestCommandAdapterReviewChangesReopenKeepsOneAttachmentIdentity(t *testing.T) {
	a, wm, _, attached := newReviewAdapter(t, patchedConversation(t))

	first := openReview(t, a, wm)
	typeRunes(first, "one ")
	require.NoError(t, first.Close())

	second := openReview(t, a, wm)
	typeRunes(second, "two ")
	require.NoError(t, second.Close())

	require.Len(t, *attached, 2)
	assert.Equal(t, chatReviewAttachmentID, (*attached)[0].ID)
	assert.Equal(t, chatReviewAttachmentID, (*attached)[1].ID)
	assert.Contains(t, (*attached)[1].Content, "two")
}

func TestCommandAdapterReviewChangesCloseIsIdempotent(t *testing.T) {
	a, wm, ed, attached := newReviewAdapter(t, patchedConversation(t))

	f := openReview(t, a, wm)
	typeRunes(f, "x")
	require.NoError(t, f.Close())
	require.NoError(t, f.Close())

	assert.Len(t, *attached, 1)
	assert.Equal(t, 1, ed.closed)
	assert.Equal(t, 1, wm.closes)
}

func TestCommandAdapterReviewChangesCloseKeys(t *testing.T) {
	tests := []struct {
		name     string
		modal    bool
		event    term.Event
		wantExit bool
	}{
		{
			name:     "ctrl-w closes a modeless editor",
			event:    term.Event{Type: term.EventKey, Ch: 'w', Mod: term.ModCtrl},
			wantExit: true,
		},
		{
			name:     "ctrl-w closes a modal editor",
			modal:    true,
			event:    term.Event{Type: term.EventKey, Ch: 'w', Mod: term.ModCtrl},
			wantExit: true,
		},
		{
			name:     "esc closes a modeless editor",
			event:    term.Event{Type: term.EventKey, Key: term.KeyEsc},
			wantExit: true,
		},
		{
			name:  "esc is left to a modal editor",
			modal: true,
			event: term.Event{Type: term.EventKey, Key: term.KeyEsc},
		},
		{
			name:  "ordinary keys reach the editor",
			event: term.Event{Type: term.EventKey, Ch: 'a'},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, wm, _, _ := newReviewAdapter(t, patchedConversation(t))
			a.editorModal = tt.modal
			f := openReview(t, a, wm)
			exit, _ := f.Handle(tt.event)
			assert.Equal(t, tt.wantExit, exit)
		})
	}
}

func TestCommandAdapterReviewChangesWithoutAppliedPatches(t *testing.T) {
	a, wm, _, _ := newReviewAdapter(t, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hi"},
	})

	_, err := a.HandleCommand(context.Background(), "reviewchanges", nil)
	assert.ErrorIs(t, err, errNoAppliedChanges)
	assert.Empty(t, wm.floatings)
}

func TestCommandAdapterReviewChangesEditorOpenFailure(t *testing.T) {
	a, wm, ed, _ := newReviewAdapter(t, patchedConversation(t))
	ed.failEdit = errors.New("editor unavailable")

	_, err := a.HandleCommand(context.Background(), "reviewchanges", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open review editor")
	assert.Empty(t, wm.floatings)
}

func TestCommandAdapterReviewChangesFloatingFailureClosesEditor(t *testing.T) {
	a, wm, ed, attached := newReviewAdapter(t, patchedConversation(t))
	wm.failWith = errors.New("no room")

	_, err := a.HandleCommand(context.Background(), "reviewchanges", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open floating window")
	assert.Equal(t, 1, ed.closed, "editor handler must not leak")
	assert.Empty(t, *attached)
}

// The comments attachment reaches the model as inline text under an
// explicit heading; file attachments keep their existing behaviour.
func TestAttachmentPartsInlineVirtualAttachment(t *testing.T) {
	h := &aiEditorHandler{}

	finalized := []finalizedAttachment{{id: "attachment-1", Attachment: dialoguetui.Attachment{
		ID:      chatReviewAttachmentID,
		Name:    chatReviewAttachmentName,
		Icon:    chatReviewAttachmentIcon,
		Content: wantConversationDiff + "why this rename?\n",
	}}}
	parts := h.attachmentContentParts(context.Background(), finalized)

	require.Len(t, parts, 1)
	assert.Equal(t, llmapi.ContentPartTypeText, parts[0].Type)
	assert.True(t, strings.HasPrefix(parts[0].Text, chatReviewHeading+"\n"),
		"got %q", parts[0].Text)
	assert.Contains(t, parts[0].Text, "*** Update File: foo.go")
	assert.Contains(t, parts[0].Text, "why this rename?")
}
