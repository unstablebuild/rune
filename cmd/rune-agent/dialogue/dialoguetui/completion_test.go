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
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/glslshader"
)

type fakeCompleter struct {
	mu            sync.Mutex
	candidates    []string
	candidatesFor func(string) []string
	queries       []string
}

type blockingCompleter struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingCompleter) Candidates(
	context.Context, string,
) (iterator.Iterator[string], error) {
	first := true
	return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
		if !first {
			return "", false, nil
		}
		first = false
		close(b.started)
		select {
		case <-b.release:
			return fileCandidate("alpha.go"), true, nil
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}, func() error { return nil }), nil
}

func (b *blockingCompleter) Resolve(candidate string) (Attachment, bool) {
	return (&fakeCompleter{}).Resolve(candidate)
}

type replacementCompleter struct {
	mu      sync.Mutex
	started []chan struct{}
	release []chan struct{}
	call    int
}

func (c *replacementCompleter) Candidates(
	context.Context, string,
) (iterator.Iterator[string], error) {
	c.mu.Lock()
	call := c.call
	c.call++
	c.mu.Unlock()
	first := true
	return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
		if !first {
			return "", false, nil
		}
		first = false
		close(c.started[call])
		select {
		case <-c.release[call]:
			return fileCandidate("alpha.go"), true, nil
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}, func() error { return nil }), nil
}

func (c *replacementCompleter) Resolve(candidate string) (Attachment, bool) {
	return (&fakeCompleter{}).Resolve(candidate)
}

func (f *fakeCompleter) Candidates(
	_ context.Context, query string,
) (iterator.Iterator[string], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, query)
	candidates := f.candidates
	if f.candidatesFor != nil {
		candidates = f.candidatesFor(query)
	}
	candidates = append([]string(nil), candidates...)
	i := 0
	return iterator.FromFunc(func(context.Context) (string, bool, error) {
		if i >= len(candidates) {
			return "", false, nil
		}
		i++
		return candidates[i-1], true, nil
	}, func() error { return nil }), nil
}

func (f *fakeCompleter) Queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

func (f *fakeCompleter) Resolve(candidate string) (Attachment, bool) {
	r := []rune(candidate)
	if len(r) < 3 || r[1] != ' ' {
		return Attachment{}, false
	}
	name := string(r[2:])
	switch r[0] {
	case WorkspaceFileIcon:
		return NewWorkspaceFileAttachment(name), true
	case SymbolIcon:
		return NewSymbolAttachment(name), true
	}
	return Attachment{}, false
}

func fileCandidate(name string) string {
	return string(WorkspaceFileIcon) + " " + name
}

func symbolCandidate(name string) string {
	return string(SymbolIcon) + " " + name
}

func newCompletionHandler(t *testing.T) (*dialogueHandler, *Component) {
	t.Helper()
	return newCompletionHandlerWithCompleter(t, &fakeCompleter{candidates: []string{
		fileCandidate("alpha.go"),
		fileCandidate("bravo.go"),
		symbolCandidate("charlie.Delta"),
	}})
}

func newCompletionHandlerWithCompleter(
	t *testing.T, completer ContextCompleter,
) (*dialogueHandler, *Component) {
	t.Helper()
	return newCompletionHandlerWithConfig(t, ComponentConfig{}, completer)
}

func newCompletionHandlerWithConfig(
	t *testing.T, cfg ComponentConfig, completer ContextCompleter,
) (*dialogueHandler, *Component) {
	t.Helper()
	comp := NewComponent(cfg)
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.NopInterrupter(), WithContextCompleter(completer))
	t.Cleanup(func() { close(tx) })
	dh := h.(*dialogueHandler)
	dh.compSyncSearch = true
	h.Resize(40, 14)
	return dh, comp
}

// typeCompletion drives text through the handler, waiting for each
// candidate refresh to settle so filtering is deterministic.
func typeCompletion(t *testing.T, h *dialogueHandler, text string) {
	t.Helper()
	for _, ch := range text {
		_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: ch})
		require.True(t, handled, "typing %q", ch)
		if h.compList != nil && h.compDone != nil {
			<-h.compDone
			h.compList.Wait()
		}
	}
}

func key(t *testing.T, h *dialogueHandler, k term.Key) {
	t.Helper()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: k})
	require.True(t, handled)
}

func TestCompletionAttachesFile(t *testing.T) {
	h, comp := newCompletionHandler(t)

	typeCompletion(t, h, "#alpha")
	require.NotNil(t, h.compList)
	key(t, h, term.KeyEnter)

	require.Nil(t, h.compList)
	assert.Equal(t, "alpha.go", comp.Input().Text(),
		"the accepted candidate replaces '#query' with its readable name")
	require.Len(t, comp.Attachments(), 1)
	a := comp.Attachments()[0]
	assert.Equal(t, "alpha.go", a.Path)
	assert.Equal(t, AttachmentFile, a.Kind)
	assert.Equal(t, WorkspaceFileIcon, a.Icon)
	assert.NotZero(t, a.Key, "the strip chip must be keyed for inline links")
	assert.Equal(t, []InlineAttachmentLink{{Key: a.Key, Start: 0, End: 8}},
		comp.Input().Links())
}

func TestCompletionKeepsPrecedingText(t *testing.T) {
	h, comp := newCompletionHandler(t)

	typeCompletion(t, h, "abc #bravo")
	key(t, h, term.KeyEnter)

	assert.Equal(t, "abc bravo.go", comp.Input().Text())
	require.Len(t, comp.Attachments(), 1)
	assert.Equal(t, "bravo.go", comp.Attachments()[0].Path)
	assert.Equal(t, []InlineAttachmentLink{
		{Key: comp.Attachments()[0].Key, Start: 4, End: 12},
	}, comp.Input().Links())
}

func TestCompletionOpensAndReplacesAtCursorMidSentence(t *testing.T) {
	h, comp := newCompletionHandler(t)
	comp.Input().SetText("abc  tail")
	in := comp.Input().(*textHandlerInput)
	require.True(t, in.Handler.SetCursorAtScroll(term.Coordinates{X: 4}))

	typeCompletion(t, h, "#alpha")
	require.NotNil(t, h.compList)
	key(t, h, term.KeyEnter)

	assert.Equal(t, "abc alpha.go tail", comp.Input().Text())
	require.Len(t, comp.Attachments(), 1)
	a := comp.Attachments()[0]
	assert.Equal(t, []InlineAttachmentLink{{Key: a.Key, Start: 4, End: 12}},
		comp.Input().Links())
}

func TestCompletionInsertsUntruncatedLabel(t *testing.T) {
	long := "a-very-long-workspace-file-name.go"
	h, comp := newCompletionHandlerWithCompleter(t, &fakeCompleter{
		candidates: []string{fileCandidate("pkg/" + long)},
	})

	typeCompletion(t, h, "#very")
	key(t, h, term.KeyEnter)

	require.Len(t, comp.Attachments(), 1)
	a := comp.Attachments()[0]
	assert.Equal(t, long, comp.Input().Text(),
		"the inline label is the full name, not the truncated chip label")
	assert.NotEqual(t, long, a.Name, "the chip label stays truncated")
	assert.Equal(t, []InlineAttachmentLink{{Key: a.Key, Start: 0, End: len(long)}},
		comp.Input().Links())
}

func TestCompletionAttachesSymbol(t *testing.T) {
	h, comp := newCompletionHandler(t)

	typeCompletion(t, h, "#charlie.Delta")
	key(t, h, term.KeyEnter)

	require.Len(t, comp.Attachments(), 1)
	a := comp.Attachments()[0]
	assert.Equal(t, AttachmentSymbol, a.Kind)
	assert.Equal(t, "charlie.Delta", a.Symbol)
	assert.Equal(t, SymbolIcon, a.Icon)
	assert.Equal(t, "", a.Path)
	assert.Equal(t, "charlie.Delta", comp.Input().Text())
	assert.Equal(t, []InlineAttachmentLink{{Key: a.Key, Start: 0, End: 13}},
		comp.Input().Links())
}

func TestCompletionReevaluatesCandidatesForPathQuery(t *testing.T) {
	completer := &fakeCompleter{candidatesFor: func(query string) []string {
		if query == "~/Desktop/" {
			return []string{
				fileCandidate("~/Desktop/notes.txt"),
				symbolCandidate("pkg.DesktopNotes"),
			}
		}
		return []string{symbolCandidate("pkg.DesktopNotes")}
	}}
	h, comp := newCompletionHandlerWithCompleter(t, completer)

	typeCompletion(t, h, "#~/Desktop/")

	assert.Equal(t, []string{
		"", "~", "~/", "~/D", "~/De", "~/Des", "~/Desk",
		"~/Deskt", "~/Deskto", "~/Desktop", "~/Desktop/",
	}, completer.Queries())
	key(t, h, term.KeyEnter)
	require.Len(t, comp.Attachments(), 1)
	assert.Equal(t, "~/Desktop/notes.txt", comp.Attachments()[0].Path)
}

func TestCompletionShowsRadarFrameWhileCandidatesStream(t *testing.T) {
	completer := &blockingCompleter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	h, comp := newCompletionHandlerWithCompleter(t, completer)

	_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: '#'})
	require.True(t, handled)
	<-completer.started

	const width, height = 40, 14
	w := term.NewStringWriter(width, height)
	require.NoError(t, w.Clear(term.Attributes{}))
	h.Draw(w)
	require.NotNil(t, comp.loadingBox.shader)
	box := comp.boxArea.Position()
	boxWidth, boxHeight := comp.boxArea.Width(), comp.boxArea.Height()
	borderShaded := false
	for y := range boxHeight {
		for x := range boxWidth {
			if x != 0 && x != boxWidth-1 && y != 0 && y != boxHeight-1 {
				continue
			}
			if w.Cells()[(box.Y+y)*width+box.X+x].Fg != comp.box.Fg {
				borderShaded = true
			}
		}
	}
	assert.True(t, borderShaded)

	close(completer.release)
	<-h.compDone
	h.compList.Wait()
	require.NoError(t, w.Clear(term.Attributes{}))
	h.Draw(w)
	assert.Nil(t, comp.loadingBox.shader)
	for y := range boxHeight {
		for x := range boxWidth {
			if x != 0 && x != boxWidth-1 && y != 0 && y != boxHeight-1 {
				continue
			}
			assert.Equal(t, comp.box.Fg,
				w.Cells()[(box.Y+y)*width+box.X+x].Fg)
		}
	}
}

func TestCompletionRadarSurvivesQueryReplacement(t *testing.T) {
	completer := &replacementCompleter{
		started: []chan struct{}{make(chan struct{}), make(chan struct{})},
		release: []chan struct{}{make(chan struct{}), make(chan struct{})},
	}
	h, comp := newCompletionHandlerWithCompleter(t, completer)

	_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: '#'})
	require.True(t, handled)
	<-completer.started[0]
	_, handled = h.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
	require.True(t, handled)
	<-completer.started[1]

	require.NotNil(t, comp.loadingBox.shader)
	close(completer.release[1])
	<-h.compDone
	h.compList.Wait()
	assert.Nil(t, comp.loadingBox.shader)
}

func TestCompletionRadarParamsUsesConfiguredColor(t *testing.T) {
	def := completionRadarParams(component.FrameCharSet{}, term.ColorDefault)
	assert.Equal(t, glslshader.DefaultRadarFrameParams(component.FrameCharSet{}).Color,
		def.Color, "ColorDefault keeps the built-in radar color")

	custom := completionRadarParams(component.FrameCharSet{}, term.ColorFuchsia)
	assert.Equal(t, term.ColorFuchsia, custom.Color)

	assert.Positive(t, custom.AngularWidth,
		"a non-positive wedge disables the sweep entirely")
	assert.LessOrEqual(t, custom.AngularWidth, 1.0,
		"the shader clamps the wedge to one revolution, past which the "+
			"frame is lit all at once and stops reading as a sweep")
}

func TestCompletionListUsesConfiguredFocusColor(t *testing.T) {
	focusFg := term.ColorFuchsia
	cfg := ComponentConfig{
		CompletionFocusElementAttr: term.Attributes{Fg: focusFg},
	}
	h, comp := newCompletionHandlerWithConfig(t, cfg, &fakeCompleter{
		candidates: []string{fileCandidate("alpha.go")},
	})

	typeCompletion(t, h, "#alpha")
	require.NotNil(t, h.compList)

	const width, height = 40, 14
	w := term.NewStringWriter(width, height)
	require.NoError(t, w.Clear(term.Attributes{}))
	h.Draw(w)

	pos := comp.completionPos
	rows := comp.completionRows
	sawFocusColor := false
	for y := range rows {
		for x := range width {
			if w.Cells()[(pos.Y+y)*width+x].Fg == focusFg {
				sawFocusColor = true
			}
		}
	}
	assert.True(t, sawFocusColor,
		"focused completion row should use the configured focus color")
}

func TestCompletionEscapeLeavesLiteralText(t *testing.T) {
	h, comp := newCompletionHandler(t)

	typeCompletion(t, h, "#al")
	key(t, h, term.KeyEsc)

	assert.Nil(t, h.compList)
	assert.Equal(t, "#al", comp.Input().Text())
	assert.Empty(t, comp.Attachments())
}

func TestCompletionSpaceClosesBand(t *testing.T) {
	h, comp := newCompletionHandler(t)

	typeCompletion(t, h, "# heading")

	assert.Nil(t, h.compList)
	assert.Equal(t, "# heading", comp.Input().Text())
	assert.Empty(t, comp.Attachments())
}

func TestCompletionCtrlCClosesBand(t *testing.T) {
	h, comp := newCompletionHandler(t)

	typeCompletion(t, h, "#al")
	_, handled := h.Handle(term.Event{
		Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl})
	require.True(t, handled)

	assert.Nil(t, h.compList)
	assert.False(t, comp.CompletionOpen())
	assert.Equal(t, "#al", comp.Input().Text())
	assert.Empty(t, comp.Attachments())
}

func TestCompletionBackspaceOnHashCloses(t *testing.T) {
	h, comp := newCompletionHandler(t)

	typeCompletion(t, h, "#")
	key(t, h, term.KeyBackspace)

	assert.Nil(t, h.compList)
	assert.Equal(t, "", comp.Input().Text())
}

func TestCompletionNotTriggeredMidWord(t *testing.T) {
	h, comp := newCompletionHandler(t)

	typeCompletion(t, h, "abc#")

	assert.Nil(t, h.compList)
	assert.Equal(t, "abc#", comp.Input().Text())
}

func TestCompletionFocusUpSelectsAnotherCandidate(t *testing.T) {
	first, comp := newCompletionHandler(t)
	typeCompletion(t, first, "#")
	key(t, first, term.KeyEnter)
	require.Len(t, comp.Attachments(), 1)
	best := comp.Attachments()[0].Name

	moved, comp := newCompletionHandler(t)
	typeCompletion(t, moved, "#")
	key(t, moved, term.KeyArrowUp)
	key(t, moved, term.KeyEnter)
	require.Len(t, comp.Attachments(), 1)

	assert.NotEqual(t, best, comp.Attachments()[0].Name)
}

func TestCompletionBandShrinksMessages(t *testing.T) {
	h, comp := newCompletionHandler(t)
	msgH := comp.msgArea.Height()
	attachY := comp.attachArea.Position().Y
	boxY := comp.boxArea.Position().Y

	typeCompletion(t, h, "#")
	require.NotNil(t, h.compList)

	assert.Equal(t, completionMaxRows, comp.completionRows)
	assert.Equal(t, msgH-completionMaxRows, comp.msgArea.Height())
	assert.Equal(t, comp.completionPos.Y+completionMaxRows,
		comp.attachArea.Position().Y)
	assert.Equal(t, boxY, comp.boxArea.Position().Y,
		"compose box stays bottom-anchored")
	assert.Equal(t, attachY-completionMaxRows, comp.completionPos.Y)

	w := term.NewStringWriter(40, 14)
	h.Draw(w)
	require.NoError(t, w.Flush())
	assert.True(t, strings.Contains(w.String(), "alpha.go"),
		"completion band should render candidates:\n%s", w.String())
}
