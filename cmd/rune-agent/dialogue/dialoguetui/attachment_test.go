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

package dialoguetui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

func pasteEvents(text string) []term.Event {
	evs := []term.Event{{Type: term.EventPasteStart}}
	for _, ch := range text {
		evs = append(evs, term.Event{Type: term.EventKey, Ch: ch})
	}
	return append(evs, term.Event{Type: term.EventPasteEnd})
}

func paste(t *testing.T, h tui.Handler, text string) {
	t.Helper()
	for _, ev := range pasteEvents(text) {
		_, handled := h.Handle(ev)
		assert.True(t, handled, "paste event %v should be handled", ev)
	}
}

func newAttachmentHandler(t *testing.T) (tui.Handler, *Component, <-chan SubmitMessage) {
	t.Helper()
	comp := NewComponent(ComponentConfig{})
	h, tx, rx := Handler(context.Background(), new(sync.Mutex), comp,
		term.NopInterrupter())
	t.Cleanup(func() { close(tx) })
	h.Resize(40, 12)
	return h, comp, rx
}

func writeTempFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o600))
	return path
}

func TestPastedFilePaths(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	b := filepath.Join(dir, "b c.txt")
	require.NoError(t, os.WriteFile(a, []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(b, []byte("x"), 0o600))

	tests := []struct {
		name  string
		text  string
		want  []string
		wantK bool
	}{
		{name: "empty", text: "  \n "},
		{name: "plain text", text: "hello world"},
		{name: "missing file", text: filepath.Join(dir, "nope.txt")},
		{name: "directory", text: dir},
		{name: "single file", text: a, want: []string{a}, wantK: true},
		{
			name: "trailing newline",
			text: a + "\n", want: []string{a}, wantK: true,
		},
		{
			name: "newline separated",
			text: a + "\n" + b, want: []string{a, b}, wantK: true,
		},
		{
			name: "escaped spaces",
			text: a + " " + escapeSpaces(b), want: []string{a, b}, wantK: true,
		},
		{
			name: "quoted paths",
			text: `"` + a + `" "` + b + `"`, want: []string{a, b}, wantK: true,
		},
		{
			name: "one missing among many",
			text: a + "\n" + filepath.Join(dir, "nope.txt"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pastedFilePaths(tt.text)
			assert.Equal(t, tt.wantK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func escapeSpaces(path string) string {
	var out []rune
	for _, r := range path {
		if r == ' ' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}

func TestHandlerPasteCreatesAttachment(t *testing.T) {
	h, comp, _ := newAttachmentHandler(t)
	path := writeTempFile(t, "shot.png")

	paste(t, h, path)

	require.Len(t, comp.Attachments(), 1)
	assert.Equal(t, path, comp.Attachments()[0].Path)
	assert.Equal(t, "shot.png", comp.Attachments()[0].Name)
	assert.True(t, comp.Attachments()[0].IsImage)
	assert.Empty(t, comp.Input().Text())

	w := term.NewStringWriter(40, 12)
	h.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "shot.png")
}

func TestHandlerPasteMultipleFiles(t *testing.T) {
	h, comp, _ := newAttachmentHandler(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	b := filepath.Join(dir, "b.go")
	require.NoError(t, os.WriteFile(a, []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(b, []byte("x"), 0o600))

	paste(t, h, a+"\n"+b)

	require.Len(t, comp.Attachments(), 2)
	assert.True(t, comp.Attachments()[0].IsImage)
	assert.False(t, comp.Attachments()[1].IsImage)
	assert.Empty(t, comp.Input().Text())
}

func TestHandlerPasteTextGoesToInput(t *testing.T) {
	h, comp, _ := newAttachmentHandler(t)

	paste(t, h, "just some text")

	assert.Empty(t, comp.Attachments())
	assert.Equal(t, "just some text", comp.Input().Text())
}

func TestHandlerClickRemovesAttachment(t *testing.T) {
	h, comp, _ := newAttachmentHandler(t)
	path := writeTempFile(t, "shot.png")
	paste(t, h, path)

	w := term.NewStringWriter(40, 12)
	h.Draw(w)
	require.NoError(t, w.Flush())

	pos, ok := comp.AttachmentsPosition()
	require.True(t, ok)
	x, ok := removeIconColumn(w.String(), pos.Y)
	require.True(t, ok, "remove affordance should be drawn")
	_, handled := h.Handle(term.Event{
		Type:   term.EventMouse,
		Key:    term.MouseLeft,
		MouseX: x,
		MouseY: pos.Y,
	})
	assert.True(t, handled)
	assert.Empty(t, comp.Attachments())
	_, ok = comp.AttachmentsPosition()
	assert.False(t, ok)
}

func TestHandlerClickOnAttachmentNameKeepsIt(t *testing.T) {
	h, comp, _ := newAttachmentHandler(t)
	paste(t, h, writeTempFile(t, "shot.png"))

	w := term.NewStringWriter(40, 12)
	h.Draw(w)
	require.NoError(t, w.Flush())

	pos, ok := comp.AttachmentsPosition()
	require.True(t, ok)
	_, handled := h.Handle(term.Event{
		Type:   term.EventMouse,
		Key:    term.MouseLeft,
		MouseX: pos.X + 3,
		MouseY: pos.Y,
	})
	assert.True(t, handled)
	assert.Len(t, comp.Attachments(), 1)
}

// removeIconColumn locates the remove affordance on the given row of a
// flushed StringWriter, where one cell renders as exactly one rune.
func removeIconColumn(out string, row int) (int, bool) {
	lines := strings.Split(out, "\n")
	if row < 0 || row >= len(lines) {
		return 0, false
	}
	for x, r := range []rune(lines[row]) {
		if r == removeAttachmentIcon {
			return x, true
		}
	}
	return 0, false
}

func TestHandlerSubmitCarriesAttachments(t *testing.T) {
	h, comp, rx := newAttachmentHandler(t)
	path := writeTempFile(t, "shot.png")
	paste(t, h, path)

	typeText(h, "look at this")
	done := make(chan SubmitMessage, 1)
	go func() { done <- <-rx }()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.True(t, handled)

	msg := <-done
	assert.Equal(t, "look at this", msg.Text)
	require.Len(t, msg.Attachments, 1)
	assert.Equal(t, path, msg.Attachments[0].Path)
	assert.Empty(t, comp.Attachments())
}

func TestUpsertVirtualAttachmentRenders(t *testing.T) {
	h, comp, _ := newAttachmentHandler(t)

	comp.UpsertAttachment(Attachment{
		ID: "chatreviewchanges", Name: " changes review", Icon: '\uf4d2',
		Content: "+added\n",
	})

	require.Len(t, comp.Attachments(), 1)
	assert.Empty(t, comp.Attachments()[0].Path)
	assert.Equal(t, "+added\n", comp.Attachments()[0].Content)

	w := term.NewStringWriter(40, 12)
	h.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "\uf4d2  changes review")
}

func TestUpsertVirtualAttachmentReplacesSameID(t *testing.T) {
	h, comp, _ := newAttachmentHandler(t)

	comp.UpsertAttachment(Attachment{
		ID: "chatreviewchanges", Name: " changes review", Icon: '\uf4d2', Content: "first",
	})
	comp.UpsertAttachment(Attachment{
		ID: "chatreviewchanges", Name: " changes review", Icon: '\uf4d2', Content: "second",
	})

	require.Len(t, comp.Attachments(), 1)
	assert.Equal(t, "second", comp.Attachments()[0].Content)

	w := term.NewStringWriter(40, 12)
	h.Draw(w)
	require.NoError(t, w.Flush())
	assert.Equal(t, 1, strings.Count(w.String(), "changes review"))
}

func TestUpsertAttachmentKeepsDistinctEntries(t *testing.T) {
	_, comp, _ := newAttachmentHandler(t)
	path := writeTempFile(t, "shot.png")

	comp.UpsertAttachment(NewAttachment(path))
	comp.UpsertAttachment(NewAttachment(path))
	comp.UpsertAttachment(Attachment{ID: "a", Name: "a", Icon: ' '})
	comp.UpsertAttachment(Attachment{ID: "b", Name: "b", Icon: ' '})
	comp.UpsertAttachment(Attachment{ID: "a", Name: "a", Icon: ' ', Content: "x"})

	require.Len(t, comp.Attachments(), 4)
	assert.Equal(t, "x", comp.Attachments()[2].Content)
}

func TestVirtualAttachmentClearedOnSubmit(t *testing.T) {
	h, comp, rx := newAttachmentHandler(t)
	comp.UpsertAttachment(Attachment{
		ID: "chatreviewchanges", Name: " changes review", Icon: '\uf4d2', Content: "+added\n",
	})

	typeText(h, "look")
	done := make(chan SubmitMessage, 1)
	go func() { done <- <-rx }()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.True(t, handled)

	msg := <-done
	require.Len(t, msg.Attachments, 1)
	assert.Equal(t, "chatreviewchanges", msg.Attachments[0].ID)
	assert.Equal(t, "+added\n", msg.Attachments[0].Content)
	assert.Empty(t, comp.Attachments())
}

func TestClickRemovesVirtualAttachment(t *testing.T) {
	h, comp, _ := newAttachmentHandler(t)
	comp.UpsertAttachment(Attachment{
		ID: "chatreviewchanges", Name: " changes review", Icon: '\uf4d2',
	})

	w := term.NewStringWriter(40, 12)
	h.Draw(w)
	require.NoError(t, w.Flush())

	pos, ok := comp.AttachmentsPosition()
	require.True(t, ok)
	x, ok := removeIconColumn(w.String(), pos.Y)
	require.True(t, ok, "remove affordance should be drawn")
	_, handled := h.Handle(term.Event{
		Type:   term.EventMouse,
		Key:    term.MouseLeft,
		MouseX: x,
		MouseY: pos.Y,
	})
	assert.True(t, handled)
	assert.Empty(t, comp.Attachments())
}
