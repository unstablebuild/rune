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

package standard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/searchbox"
)

type searchTestWindow uint64

func (w searchTestWindow) WindowID() uint64 { return uint64(w) }

type searchTestWindowManager struct {
	floating browserapi.Floating
	config   browserapi.FloatingConfig
	closed   int
	err      error
	closeErr error
}

type searchSequenceHarness struct {
	owner         *searchHandler
	floating      browserapi.Floating
	width, height int
	closeCalls    int
	closeErr      error
}

func newSearchSequenceHarness(
	t *testing.T, content string, mode searchbox.Mode,
) *searchSequenceHarness {
	t.Helper()
	buf := cell.NewBuffer()
	buf.WriteString(content)
	root := NewHandler(buf, workspaceapi.URI{}, '\t', 0).(*standardHandler)
	h := &searchSequenceHarness{}
	cfg := defaultConfig().search
	cfg.WindowManager = h
	h.owner = newSearchHandler(root, root, cfg)
	if mode == searchbox.ModeReplace {
		h.open(t, mode)
	}
	return h
}

func (h *searchSequenceHarness) open(t *testing.T, mode searchbox.Mode) {
	t.Helper()
	ev := term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'}
	if mode == searchbox.ModeReplace {
		ev.Ch = 'r'
	}
	_, handled := h.owner.Handle(ev)
	require.True(t, handled)
	require.NotNil(t, h.floating)
}

func (h *searchSequenceHarness) Floating(
	f browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	h.floating = f
	h.floating.Resize(h.width, h.height)
	return searchTestWindow(1), nil
}

func (h *searchSequenceHarness) CloseWindow(browserapi.Window) error {
	h.closeCalls++
	if f := h.floating; f != nil {
		h.floating = nil
		return f.Close()
	}
	return nil
}

func (h *searchSequenceHarness) SetTabActivity(workspaceapi.URI, bool) error { return nil }

func (h *searchSequenceHarness) Resize(width, height int) {
	h.width, h.height = width, height
	h.owner.Handler.Resize(width, height)
	if h.floating != nil {
		h.floating.Resize(width, height)
	}
}

func (h *searchSequenceHarness) Draw(w term.Writer) {
	if h.floating != nil {
		h.floating.Draw(w)
		return
	}
	h.owner.Handler.Draw(w)
}

func (h *searchSequenceHarness) Handle(ev term.Event) (bool, bool) {
	if h.floating == nil {
		return h.owner.Handle(ev)
	}
	f := h.floating
	exit, handled := f.Handle(ev)
	if exit {
		h.closeErr = errors.Join(h.closeErr, f.Close())
	}
	return false, handled
}

func (h *searchSequenceHarness) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	if h.floating != nil {
		return h.floating.Cursor()
	}
	return h.owner.Handler.Cursor()
}

func (h *searchSequenceHarness) Selection() (string, bool) {
	if h.floating != nil {
		return h.floating.Selection()
	}
	return h.owner.Handler.Selection()
}

func (h *searchSequenceHarness) Close() error { return h.owner.Close() }

func golden(width int, lines ...string) string {
	for i, line := range lines {
		lines[i] = line + strings.Repeat(" ", max(0, width-utf8.RuneCountInString(line)))
	}
	return strings.Join(lines, "\n")
}

func TestSearchFloatingKeyboardStateDiagram(t *testing.T) {
	tests := []struct {
		name    string
		content string
		cases   []handlertest.SequenceTestCase
	}{
		{
			name:    "find refinement navigation recovery and reopen",
			content: "one two one",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<meta-f>", Expected: golden(48,
					"", " ┌──────────────────────────────┐", " │▐ind                          │  Replace ",
					" └──────────────────────────────┘", "")},
				{InputSequence: "one", Expected: golden(48,
					"", " ┌──────────────────────────────┐", " │one▐                          │  Replace ",
					" └──────────────────────────────┘", "")},
				{InputSequence: "<enter>", Expected: golden(48,
					"", " ┌──────────────────────────────┐", " │one▐                          │  Replace ",
					" └──────────────────────────────┘", "")},
				{InputSequence: "z", Expected: golden(48,
					"", " ┌──────────────────────────────┐", " │onez▐                         │  Replace ",
					" └──────────────────────────────┘", "")},
				{InputSequence: "<backspace>", Expected: golden(48,
					"", " ┌──────────────────────────────┐", " │one▐                          │  Replace ",
					" └──────────────────────────────┘", "")},
				{InputSequence: "<meta-f>", Expected: golden(48,
					"", " ┌──────────────────────────────┐", " │one▐                          │  Replace ",
					" └──────────────────────────────┘", "")},
				{InputSequence: "<esc>", Expected: golden(48,
					"one two one▐", "", "", "", "")},
				{InputSequence: "<ctrl-f>", Expected: golden(48,
					"", " ┌──────────────────────────────┐", " │one▐                          │  Replace ",
					" └──────────────────────────────┘", "")},
			},
		},
		{
			name:    "replace upgrade field cycles spaces and wide input",
			content: "界 one 界 one",
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "<meta-f>界<space>one<meta-r>", Expected: golden(48,
					"", " ┌────────────────────────────┐", " │界  one▐                     │",
					" └────────────────────────────┘", "", " ┌────────────────────────────┐",
					" │Replace with                │  Replace   All ",
					" └────────────────────────────┘", "")},
				{InputSequence: "<tab>x界", Expected: golden(48,
					"", " ┌────────────────────────────┐", " │界  one                      │",
					" └────────────────────────────┘", "", " ┌────────────────────────────┐",
					" │x界 ▐                        │  Replace   All ",
					" └────────────────────────────┘", "")},
				{InputSequence: "<shift-tab>q", Expected: golden(48,
					"", " ┌────────────────────────────┐", " │界  oneq▐                    │",
					" └────────────────────────────┘", "", " ┌────────────────────────────┐",
					" │x界                          │  Replace   All ",
					" └────────────────────────────┘", "")},
				{InputSequence: "<backspace><enter><tab><enter>", Expected: golden(48,
					"", " ┌────────────────────────────┐", " │界  one                      │",
					" └────────────────────────────┘", "", " ┌────────────────────────────┐",
					" │x界 ▐                        │  Replace   All ",
					" └────────────────────────────┘", "")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSearchSequenceHarness(t, tt.content, searchbox.ModeFind)
			handlertest.RunHandlerSequence(t, h, 48, len(strings.Split(tt.cases[0].Expected, "\n")), tt.cases)
			require.NoError(t, h.closeErr)
		})
	}
}

func TestSearchFloatingDirectReplaceSequence(t *testing.T) {
	h := newSearchSequenceHarness(t, "alpha alpha", searchbox.ModeFind)
	handlertest.RunHandlerSequence(t, h, 48, 9, []handlertest.SequenceTestCase{
		{InputSequence: "<meta-r>alpha<tab>beta", Expected: golden(48,
			"", " ┌────────────────────────────┐", " │alpha                       │",
			" └────────────────────────────┘", "", " ┌────────────────────────────┐",
			" │beta▐                       │  Replace   All ",
			" └────────────────────────────┘", "")},
		{InputSequence: "<meta-r>", Expected: golden(48,
			"", " ┌────────────────────────────┐", " │alpha                       │",
			" └────────────────────────────┘", "", " ┌────────────────────────────┐",
			" │beta▐                       │  Replace   All ",
			" └────────────────────────────┘", "")},
	})
}

func TestSearchFloatingUsesStandardInputEditing(t *testing.T) {
	h := newSearchSequenceHarness(t, "one Xone", searchbox.ModeFind)
	handlertest.RunHandlerSequence(t, h, 48, 5, []handlertest.SequenceTestCase{
		{InputSequence: "<meta-f>one<meta-left>X", Expected: golden(48,
			"", " ┌──────────────────────────────┐", " │X▐ne                          │  Replace ",
			" └──────────────────────────────┘", "")},
	})
}

// TestSearchEscThenTypeReplacesMatch pins that the match selection left
// behind when the search widget is dismissed behaves like any other
// selection: typing replaces the selected occurrence.
func TestSearchEscThenTypeReplacesMatch(t *testing.T) {
	h := newSearchSequenceHarness(t, "one two one", searchbox.ModeFind)
	h.Resize(48, 5)
	handlertest.RunHandlerSequence(t, h, 48, 5, []handlertest.SequenceTestCase{
		{InputSequence: "<meta-f>one<enter><esc>X", Expected: golden(48,
			"one two X▐", "", "", "", "")},
	})
	require.NoError(t, h.closeErr)
	root := h.owner.Handler.(*standardHandler)
	assert.Equal(t, "one two X", root.buf.String())
}

type searchBrowserWindowManager struct {
	browser *browser.Component
}

func (m searchBrowserWindowManager) Floating(
	h browserapi.Floating, cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	return m.browser.Floating(h.(browser.Floating), cfg), nil
}

func (m searchBrowserWindowManager) CloseWindow(win browserapi.Window) error {
	return win.(browser.Window).Close()
}

func (m searchBrowserWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

func TestSearchFloatingRealBrowserLifecycle(t *testing.T) {
	cfg := browser.DefaultConfig()
	cfg.WindowManagerConfig.NoMaxSize = false
	b := browser.NewComponent(cfg)
	t.Cleanup(func() { require.NoError(t, b.Close()) })

	buf := cell.NewBuffer()
	buf.WriteString("one two one")
	ed := Editor(WithSearchConfig(SearchConfig{WindowManager: searchBrowserWindowManager{browser: b}}))
	h, err := ed.Edit(context.Background(), workspaceapi.URI{}, buf, false, false)
	require.NoError(t, err)
	require.NoError(t, b.Focus().SetContent(h))

	handlertest.RunHandlerSequence(t, b, 56, 14, []handlertest.SequenceTestCase{
		{InputSequence: "<ctrl-f>one", Expected: golden(56,
			"┌──────────────────────────────────────────────────────┐",
			"│                                                      │",
			"├──────────────────────────────────────────────────────┤",
			"│one two one                                           │",
			"│    █●██████████████ Find / Replace ██████████████    │",
			"│    │                                            │    │",
			"│    │ ┌──────────────────────────────┐           │    │",
			"│    │ │one▐                          │  Replace  │    │",
			"│    │ └──────────────────────────────┘           │    │",
			"│    └────────────────────────────────────────────┘    │",
			"│                                                      │",
			"│                                                      │",
			"│                                                      │",
			"└──────────────────────────────────────────────────────┘")},
		{InputSequence: "<esc>", Expected: golden(56,
			"┌──────────────────────────────────────────────────────┐",
			"│                                                      │",
			"├──────────────────────────────────────────────────────┤",
			"│one▐two one                                           │",
			"│                                                      │",
			"│                                                      │",
			"│                                                      │",
			"│                                                      │",
			"│                                                      │",
			"│                                                      │",
			"│                                                      │",
			"│                                                      │",
			"│                                                      │",
			"└──────────────────────────────────────────────────────┘")},
	})
	assert.Zero(t, b.FloatingWindows())
}

func TestSearchFloatingConstrainedKeyboardSequence(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		input         string
		want          string
	}{
		{name: "narrow wide find wraps and scrolls", width: 8, height: 3,
			input: "<meta-f>界界界界", want: golden(8, " │界│", " │界│", " │▐│")},
		{name: "short replace viewport follows replacement cursor", width: 12, height: 4,
			input: "<meta-r>abcdef<tab>123456", want: golden(12, " │4│", " │5│", " │6│", " │▐│")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSearchSequenceHarness(t, "abcdef abcdef", searchbox.ModeFind)
			handlertest.RunHandlerSequence(t, h, tt.width, tt.height, []handlertest.SequenceTestCase{
				{InputSequence: tt.input, Expected: tt.want},
			})
		})
	}
}

func TestSearchReplacementSemantics(t *testing.T) {
	tests := []struct {
		name, content, query, replacement, want string
		all                                     bool
	}{
		{name: "no matches", content: "abc", query: "z", replacement: "x", want: "abc"},
		{name: "empty query", content: "abc", replacement: "x", want: "abc"},
		{name: "shorter next", content: "one one", query: "one", replacement: "x", want: "x one"},
		{name: "longer all", content: "a a", query: "a", replacement: "alpha", all: true, want: "alpha alpha"},
		{name: "empty all", content: "a-a-a", query: "a", all: true, want: "--"},
		{name: "contains query all", content: "a a", query: "a", replacement: "aa", all: true, want: "aa aa"},
		{name: "unicode wide", content: "界x界", query: "界", replacement: "語", all: true, want: "語x語"},
		{name: "multiline", content: "one\none", query: "one", replacement: "x\ny", all: true, want: "x\ny\nx\ny"},
		{name: "nul buffer", content: "a\x00a", query: "a", replacement: "b", all: true, want: "b\x00b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.WriteString(tt.content)
			h := NewHandler(buf, workspaceapi.URI{}, '\t', 0).(*standardHandler)
			h.Resize(40, 10)
			h.BeginSearch([]rune(tt.query), term.Coordinates{})
			if tt.all {
				h.ReplaceAll(tt.replacement)
			} else {
				h.ReplaceNext(tt.replacement)
			}
			assert.Equal(t, tt.want, buf.View().String())
			if tt.want != tt.content {
				undone, _ := buf.Undo()
				require.True(t, undone)
				assert.Equal(t, tt.content, buf.View().String())
			}
		})
	}
}

func mouseEvent(key term.Key, x, y int) term.Event {
	return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
}

func TestSearchCustomTriggersAndLifecycle(t *testing.T) {
	wm := &searchTestWindowManager{closeErr: errors.New("close")}
	cfg := defaultConfig().search
	cfg.WindowManager = wm
	cfg.FindKey = term.KeyComb{Mod: term.ModCtrl, Ch: 's'}
	cfg.ReplaceKey = term.KeyComb{Mod: term.ModCtrl, Ch: 'h'}
	buf := cell.NewBuffer()
	buf.WriteString("one")
	root := NewHandler(buf, workspaceapi.URI{}, '\t', 0).(*standardHandler)
	h := newSearchHandler(root, root, cfg)

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'f'})
	assert.True(t, handled, "ctrl-f stays aliased to the find key")
	assert.True(t, h.box.Active())
	assert.ErrorIs(t, h.box.Finish(true), wm.closeErr)
	wm.closed = 0

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 's'})
	assert.True(t, handled)
	require.True(t, h.box.Active())
	wm.floating.Handle(term.Event{Type: term.EventKey, Ch: 'o'})
	assert.False(t, root.find.legacyPrompt)

	err := h.box.Finish(true)
	assert.ErrorIs(t, err, wm.closeErr)
	assert.Equal(t, 1, wm.closed)
	assert.False(t, h.box.Active())
	assert.False(t, root.find.active)
	assert.NotEmpty(t, root.searchLocations(), "accepted search highlights must remain")

	_, handled = h.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'h'})
	assert.True(t, handled)
	require.True(t, h.box.Active())
	assert.Equal(t, "Find / Replace", wm.config.Title)
}

func (m *searchTestWindowManager) Floating(
	f browserapi.Floating, cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	m.floating, m.config = f, cfg
	if m.err != nil {
		return nil, m.err
	}
	return searchTestWindow(1), nil
}

func (m *searchTestWindowManager) CloseWindow(browserapi.Window) error {
	m.closed++
	return m.closeErr
}

func (m *searchTestWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

func TestWithSearchConfigRequiresWindowManager(t *testing.T) {
	assert.PanicsWithValue(t, "standard: SearchConfig.WindowManager must not be nil", func() {
		WithSearchConfig(SearchConfig{})
	})
}

func TestSearchCompatibilityOptions(t *testing.T) {
	match := term.Attributes{Fg: term.ColorYellow}
	status := term.Attributes{Bg: term.ColorPurple}
	cfg := defaultConfig()
	WithResAttr(match)(&cfg)
	WithBarAttr(status)(&cfg)
	assert.Equal(t, match, cfg.search.MatchAttr)
	assert.Equal(t, status, cfg.search.StatusAttr)
}

func TestEditorInstallsConfiguredSearchWrapper(t *testing.T) {
	wm := new(searchTestWindowManager)
	ed := Editor(WithSearchConfig(SearchConfig{WindowManager: wm}))
	buf := cell.NewBuffer()
	buf.WriteString("one two one")
	h, err := ed.Edit(context.Background(), workspaceapi.URI{}, buf, false, false)
	require.NoError(t, err)

	wrapped, ok := h.(*searchHandler)
	require.True(t, ok)
	exit, handled := wrapped.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	assert.False(t, exit)
	assert.True(t, handled)
	require.NotNil(t, wm.floating)
	assert.Equal(t, componentTopCenter(), wm.config.Alignment)
	assert.Equal(t, term.Coordinates{Y: 2}, wm.config.Offset)
	assert.False(t, wm.config.NoWindowBar)
	assert.Equal(t, "Find / Replace", wm.config.Title)
}

func componentTopCenter() component.Alignment {
	return component.AlignmentTop | component.AlignmentHorizontallyCentered
}

func TestEditorWithoutSearchConfigPreservesStandaloneHandler(t *testing.T) {
	ed := Editor()
	h, err := ed.Edit(context.Background(), workspaceapi.URI{}, cell.NewBuffer(), false, false)
	require.NoError(t, err)
	_, wrapped := h.(*searchHandler)
	assert.False(t, wrapped)
}

func TestSearchReplacementContainingQueryTerminates(t *testing.T) {
	tests := []struct {
		name string
		all  bool
		want string
	}{
		{name: "next", want: "aa a"},
		{name: "all", all: true, want: "aa aa"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.WriteString("a a")
			h := NewHandler(buf, workspaceapi.URI{}, '\t', 0).(*standardHandler)
			h.Resize(20, 4)
			h.BeginSearch([]rune("a"), term.Coordinates{})
			if tt.all {
				h.ReplaceAll("aa")
			} else {
				h.ReplaceNext("aa")
			}
			assert.Equal(t, tt.want, buf.View().String())
		})
	}
}

func TestFindOpenFailureFallsBackAndReplaceCleansUp(t *testing.T) {
	tests := []struct {
		name string
		key  term.KeyComb
		find bool
	}{
		{name: "find", key: term.KeyComb{Mod: term.ModMeta, Ch: 'f'}, find: true},
		{name: "replace", key: term.KeyComb{Mod: term.ModMeta, Ch: 'r'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wm := &searchTestWindowManager{err: errors.New("boom")}
			buf := cell.NewBuffer()
			buf.WriteString("text")
			root := NewHandler(buf, workspaceapi.URI{}, '\t', 0).(*standardHandler)
			root.Resize(20, 4)
			cfg := defaultConfig().search
			cfg.WindowManager = wm
			owner := newSearchHandler(root, root, cfg)
			owner.Handle(term.Event{
				Type: term.EventKey, Mod: tt.key.Mod, Key: tt.key.Key, Ch: tt.key.Ch})
			assert.False(t, owner.box.Active())
			assert.Equal(t, tt.find, root.find.active, "find falls back to the legacy prompt")
			assert.Equal(t, tt.find, root.find.legacyPrompt)
		})
	}
}

// TestSearchFloatingReportsEditorSelection pins that the match left
// selected in the document stays copyable while the find window holds the
// focus, since window managers report the focused window's selection.
func TestSearchFloatingReportsEditorSelection(t *testing.T) {
	wm := new(searchTestWindowManager)
	buf := cell.NewBuffer()
	buf.WriteString("one two one")
	root := NewHandler(buf, workspaceapi.URI{}, '\t', 0).(*standardHandler)
	root.Resize(40, 10)
	cfg := defaultConfig().search
	cfg.WindowManager = wm
	h := newSearchHandler(root, root, cfg)

	_, handled := h.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	require.True(t, handled)
	require.NotNil(t, wm.floating)
	for _, ch := range "two" {
		wm.floating.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}

	selected, ok := wm.floating.Selection()
	assert.True(t, ok)
	assert.Equal(t, "two", selected)
}
