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

package vte

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/searchbox"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/workspace"
)

var (
	searchMatchAttr   = term.Attributes{Bg: term.ColorYellow}
	searchCurrentAttr = term.Attributes{Attrs: term.AttrReverse}
)

func testSearchConfig() SearchConfig {
	return SearchConfig{
		Config: searchbox.Config{
			Editor:  standard.Editor(),
			FindKey: term.KeyComb{Mod: term.ModMeta, Ch: 'f'},
		},
		MatchAttr:        searchMatchAttr,
		CurrentMatchAttr: searchCurrentAttr,
	}
}

// searchHarness drives the scrollback search over an injected
// primary buffer, without a pty.
type searchHarness struct {
	vi   viHandler
	ctrl *searchController
	box  *searchbox.Box
}

func newSearchHarness(t *testing.T, content string, cursor term.Coordinates) *searchHarness {
	t.Helper()
	h := new(searchHarness)
	comp := newTestParentComponent(content, cursor)
	h.vi.doInit(comp, DefaultConfig())
	h.vi.Resize(20, 4)

	cfg := testSearchConfig()
	h.ctrl = &searchController{vi: &h.vi, cfg: cfg}
	h.box = searchbox.New(h.ctrl, cfg.Config)
	return h
}

func (h *searchHarness) open(t *testing.T) {
	t.Helper()
	require.True(t, h.box.HandleKey(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'}))
	require.NotNil(t, h.box.Content())
	require.True(t, h.ctrl.viewing)
}

func (h *searchHarness) type_(query string) {
	for _, ch := range query {
		h.box.Content().Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
}

func (h *searchHarness) locations() []textapi.Location {
	return h.vi.searchLocations()
}

func (h *searchHarness) cursor() term.Coordinates {
	return h.vi.cursorAtScroll()
}

func TestVTESearchHighlightsAndNavigatesScrollback(t *testing.T) {
	t.Parallel()
	// three matches, one of them above the live cursor
	const content = "one two\nthree one\nfour\none"

	tests := []struct {
		name       string
		query      string
		origin     term.Coordinates
		advance    int
		wantCursor term.Coordinates
		wantCount  int
	}{
		{name: "lands on first match at or after origin", query: "one",
			origin: term.Coordinates{}, wantCursor: term.Coordinates{}, wantCount: 3},
		{name: "lands past the origin", query: "one",
			origin: term.Coordinates{Y: 1}, wantCursor: term.Coordinates{Y: 1, X: 6},
			wantCount: 3},
		{name: "advances to the next match", query: "one",
			origin: term.Coordinates{}, advance: 1,
			wantCursor: term.Coordinates{Y: 1, X: 6}, wantCount: 3},
		{name: "wraps around at the last match", query: "one",
			origin: term.Coordinates{}, advance: 3,
			wantCursor: term.Coordinates{}, wantCount: 3},
		{name: "wraps to the first match when the origin is past every match",
			query: "two", origin: term.Coordinates{Y: 3},
			wantCursor: term.Coordinates{X: 4}, wantCount: 1},
		{name: "keeps the origin when nothing matches", query: "zzz",
			origin: term.Coordinates{Y: 2}, wantCursor: term.Coordinates{Y: 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newSearchHarness(t, content, tt.origin)
			h.open(t)
			h.type_(tt.query)
			for range tt.advance {
				h.box.Content().Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			}

			locs := h.locations()
			assert.Len(t, locs, tt.wantCount)
			assert.Equal(t, tt.wantCursor, h.cursor())
			for i, loc := range locs {
				want := searchMatchAttr
				if loc.From == tt.wantCursor {
					want = searchCurrentAttr
				}
				assert.Equal(t, want, loc.Attr, "location %d attributes", i)
			}
		})
	}
}

func TestVTESearchParksOnCurrentMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		close func(*testing.T, *searchHarness)
	}{
		{name: "esc", close: func(t *testing.T, h *searchHarness) {
			exit, handled := h.box.Content().Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			assert.True(t, exit)
			assert.True(t, handled)
			require.NoError(t, h.box.Content().Close())
		}},
		{name: "window closed", close: func(t *testing.T, h *searchHarness) {
			require.NoError(t, h.box.Close())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newSearchHarness(t, "one two\nthree one", term.Coordinates{})
			h.open(t)
			h.type_("one")
			require.NotEmpty(t, h.locations())
			match := h.cursor()

			tt.close(t, h)
			assert.False(t, h.box.Active())
			assert.True(t, h.ctrl.viewing, "the results view stays up")
			assert.NotEmpty(t, h.locations(), "matches stay highlighted")
			assert.Equal(t, match, h.cursor(), "the view stays on the current match")

			h.ctrl.exitView()
			assert.False(t, h.ctrl.viewing)
			assert.Empty(t, h.locations())

			h.open(t)
			assert.Equal(t, "one", h.ctrl.SearchLast(), "the query is offered again")
		})
	}
}

func TestVTESearchWithoutMatchRestoresLiveView(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
	}{
		{name: "empty query"},
		{name: "no matches", query: "zzz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newSearchHarness(t, "one two\nthree one", term.Coordinates{Y: 1})
			h.open(t)
			h.type_(tt.query)

			require.NoError(t, h.box.Close())
			assert.False(t, h.ctrl.viewing)
			assert.Empty(t, h.locations())
		})
	}
}

func TestVTESearchReopensFromParkedMatch(t *testing.T) {
	t.Parallel()
	h := newSearchHarness(t, "one\nfiller\none\nfiller\none", term.Coordinates{Y: 4})
	h.open(t)
	h.type_("one")
	require.Equal(t, term.Coordinates{Y: 4}, h.cursor())

	// wrap onto the first match and park there
	h.box.Content().Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.Equal(t, term.Coordinates{}, h.cursor())
	require.NoError(t, h.box.Close())

	h.open(t)
	h.type_("one")
	assert.Equal(t, term.Coordinates{}, h.cursor())
}

func TestVTESearchMatchIsTheSelection(t *testing.T) {
	t.Parallel()
	h := newSearchHarness(t, "one two\nthree once", term.Coordinates{})
	h.open(t)

	selected, ok := h.ctrl.Selection()
	assert.False(t, ok, "an empty query selects nothing")
	assert.Empty(t, selected)

	h.type_("on")
	selected, ok = h.ctrl.Selection()
	require.True(t, ok)
	assert.Equal(t, "on", selected)

	// the match stays selected once the box is out of the way
	require.NoError(t, h.box.Close())
	require.True(t, h.ctrl.viewing)
	selected, ok = h.ctrl.Selection()
	require.True(t, ok)
	assert.Equal(t, "on", selected)

	h.ctrl.exitView()
	selected, ok = h.ctrl.Selection()
	assert.False(t, ok, "dismissing the results drops the selection")
	assert.Empty(t, selected)
}

func TestVTESearchOriginFollowsViCursorInModal(t *testing.T) {
	t.Parallel()
	liveCursor := term.Coordinates{Y: 5, X: 2}
	h := newSearchHarness(t, "one\ntwo\nthree", liveCursor)

	assert.Equal(t, liveCursor, h.ctrl.SearchOrigin(),
		"outside of modal mode, the live cursor is the origin")

	h.vi.enterViMode(term.Coordinates{Y: 1, X: 2})
	// enterViMode nudges the column back by one to land on the last
	// character instead of past it, matching a live pty->vi handoff
	viCursor := term.Coordinates{Y: 1, X: 1}
	h.ctrl.viModeActive = func() bool { return true }

	assert.Equal(t, viCursor, h.ctrl.SearchOrigin(),
		"once in modal mode, the vi cursor is the origin, not the live one")
}

func TestVTESearchOverlayScrolls(t *testing.T) {
	t.Parallel()
	h := newSearchHarness(t, "one\n1\n2\n3\n4\n5\none", term.Coordinates{Y: 6})
	h.open(t)
	h.type_("one")
	require.Len(t, h.locations(), 2)

	// the match above the viewport pulls the overlay view up with it
	h.box.Content().Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.Equal(t, term.Coordinates{}, h.cursor())

	w := term.NewStringWriter(20, 4)
	h.vi.Draw(w)
	require.NoError(t, w.Flush())
	assert.Equal(t, searchCurrentAttr, w.Cells()[0].Attributes(),
		"the overlay view scrolled to the current match")
}

func TestVTESearchIsFindOnly(t *testing.T) {
	t.Parallel()
	h := newSearchHarness(t, "one", term.Coordinates{})
	h.open(t)

	_, handled := h.box.Content().Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'r'})
	assert.False(t, handled, "terminals cannot replace")
}

func newSearchHandler(t *testing.T, cfg Config) *Handler {
	t.Helper()
	if os.Getenv("CI") == "true" {
		t.SkipNow()
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	uri, err := workspaceapi.CurrentUserHostURI(os.TempDir())
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { scheme.Close() })

	cfg.WidthHint, cfg.HeightHint = 20, 10
	cfg.CommandAndArgs = []string{"sh"}
	handler, err := NewHandler(nopEventPublisher{}, nopNotifications{},
		scheme, scheme, nopTabManager{}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { handler.Close() })
	handler.Resize(20, 10)
	return handler
}

func TestHandlerSearchKeyRouting(t *testing.T) {
	tests := []struct {
		name        string
		modal       bool
		search      SearchConfig
		ev          term.Event
		wantOpen    bool
		wantHandled bool
	}{
		{name: "meta-f opens the search box", search: testSearchConfig(),
			ev:       term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'},
			wantOpen: true, wantHandled: true},
		{name: "meta-f opens the search box in modal terminals", modal: true,
			search:   testSearchConfig(),
			ev:       term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'},
			wantOpen: true, wantHandled: true},
		{name: "ctrl-f still reaches the shell", search: testSearchConfig(),
			ev:          term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'f', Raw: []byte{6}},
			wantHandled: true},
		{name: "meta-f is dropped when search is disabled",
			ev: term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Modal = tt.modal
			cfg.Search = tt.search
			handler := newSearchHandler(t, cfg)

			exit, handled := handler.Handle(tt.ev)
			assert.False(t, exit)
			assert.Equal(t, tt.wantHandled, handled)
			assert.Equal(t, tt.wantOpen, handler.searchOpen())
			assert.Equal(t, tt.wantOpen, handler.searchViewing())
		})
	}
}

func TestHandlerSearchIgnoredInAltBuffer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Search = testSearchConfig()
	handler := newSearchHandler(t, cfg)

	_, err := handler.comp.pty.Master.Write([]byte("printf '\\033[?1049h'\n"))
	require.NoError(t, err)
	require.Eventually(t, handler.comp.IsAltBuffer, 5*time.Second, 10*time.Millisecond)

	_, handled := handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	assert.False(t, handled)
	assert.False(t, handler.searchOpen())
	assert.False(t, handler.searchViewing())
}

func TestHandlerSearchOpensWhileInViMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Modal = true
	cfg.Search = testSearchConfig()
	handler := newSearchHandler(t, cfg)

	// drives the same transition <esc> triggers, without the real bell
	// round-trip: that entry path is exercised elsewhere, and reading
	// its unsynchronized completion here would race with this goroutine
	handler.enterViMode(term.Coordinates{})
	require.True(t, handler.viMode)

	_, handled := handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	assert.True(t, handled)
	assert.True(t, handler.searchOpen(), "meta-f must open the search box while in vi mode")
	assert.True(t, handler.viMode, "vi mode stays active behind the box")
}

func TestHandlerSearchQueriedBeforeFirstDraw(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Search = testSearchConfig()
	handler := newSearchHandler(t, cfg)

	_, handled := handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	require.True(t, handled)

	pos, _, show := handler.Cursor()
	assert.True(t, show, "the query input owns the cursor")
	assert.True(t, handler.searchOverlayContains(pos.X, pos.Y),
		"the cursor sits inside the box")
	selected, ok := handler.Selection()
	assert.False(t, ok)
	assert.Empty(t, selected)
}

func TestHandlerSearchLetsUnboundShortcutsThroughWhileOpen(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Search = testSearchConfig()
	handler := newSearchHandler(t, cfg)

	_, handled := handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	require.True(t, handled)
	require.True(t, handler.searchOpen())

	_, handled = handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'c'})
	assert.False(t, handled, "an unbound meta shortcut must bubble up to the IDE's key bindings")
	assert.True(t, handler.searchOpen(), "the box itself stays open")
}

func TestHandlerSearchCtrlCActsLikeEscapeWhileOpen(t *testing.T) {
	h := newSearchHarness(t, "one two\nthree one", term.Coordinates{})
	h.open(t)
	h.type_("one")
	require.NotEmpty(t, h.locations())

	exit, handled := h.box.Content().Handle(
		term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
	assert.True(t, exit)
	assert.True(t, handled)
	require.NoError(t, h.box.Close())

	assert.True(t, h.ctrl.viewing, "the match stays parked, just like on <esc>")
	assert.NotEmpty(t, h.locations(), "matches stay highlighted")
}

type nopEventPublisher struct{}

func (nopEventPublisher) PublishEvent(term.Event) error { return nil }

func TestHandlerSearchOverlayPosition(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Search = testSearchConfig()
	handler := newSearchHandler(t, cfg)
	handler.Resize(80, 24)

	_, handled := handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	require.True(t, handled)

	w := term.NewStringWriter(80, 24)
	handler.Draw(w)
	require.NoError(t, w.Flush())

	lines := strings.Split(w.String(), "\n")
	top := []rune(lines[0])
	require.Contains(t, string(top), "┌", "the box frame is flush with the top edge")
	assert.Equal(t, "┐  ", string(top[len(top)-3:]),
		"the frame ends two columns clear of the right edge")
}

func TestHandlerSearchOverlayCroppedTopStaysUsable(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Search = testSearchConfig()
	handler := newSearchHandler(t, cfg)
	handler.Resize(80, 24)

	_, handled := handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	require.True(t, handled)
	for _, ch := range "needle" {
		_, handled = handler.Handle(term.Event{Type: term.EventKey, Ch: ch})
		require.True(t, handled)
	}

	w := term.NewStringWriter(80, 24)
	handler.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "needle")

	pos, _, show := handler.Cursor()
	require.True(t, show)
	assert.True(t, handler.searchOverlayContains(pos.X, pos.Y))
	assert.GreaterOrEqual(t, pos.Y, 0)

	// click inside the query input's content row and release, which
	// must reach the box's own hit-testing rather than being dropped or
	// misrouted by the crop; clicking the top row itself would land on
	// the frame's border, which the box does not treat as a hit target
	frame := handler.searchOverlay.Position()
	x, y := frame.X+5, frame.Y+1
	_, handled = handler.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft,
		MouseX: x, MouseY: y})
	assert.True(t, handled)
	_, handled = handler.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease,
		MouseX: x, MouseY: y})
	assert.False(t, handled, "an un-dragged release is a no-op for the input")
}

func waitForScrollback(t *testing.T, h *Handler, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		scroll, mu := h.comp.PrimaryScroll()
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(scroll.Buffer().String(), want)
	}, 5*time.Second, 10*time.Millisecond)
}

func TestHandlerSearchResultsStayUntilDismissed(t *testing.T) {
	tests := []struct {
		name        string
		ev          term.Event
		wantHandled bool
		wantViewing bool
	}{
		{name: "esc dismisses the results",
			ev:          term.Event{Type: term.EventKey, Key: term.KeyEsc},
			wantHandled: true},
		{name: "typing goes back to the shell",
			ev:          term.Event{Type: term.EventKey, Ch: 'x', Raw: []byte("x")},
			wantHandled: true},
		{name: "shortcuts the shell never sees keep the results up",
			ev:          term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'c'},
			wantViewing: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Search = testSearchConfig()
			handler := newSearchHandler(t, cfg)

			_, err := handler.comp.pty.Master.Write([]byte("printf 'needle\\n'\n"))
			require.NoError(t, err)
			waitForScrollback(t, handler, "needle")

			_, handled := handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
			require.True(t, handled)
			for _, ch := range "needle" {
				handler.Handle(term.Event{Type: term.EventKey, Ch: ch})
			}
			require.NotEmpty(t, handler.vi.searchLocations())

			_, handled = handler.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
			require.True(t, handled)
			require.False(t, handler.searchBox.Active())
			assert.True(t, handler.searchViewing(), "the match stays on screen")
			assert.NotEmpty(t, handler.vi.searchLocations(), "matches stay highlighted")

			_, handled = handler.Handle(tt.ev)
			assert.Equal(t, tt.wantHandled, handled)
			assert.Equal(t, tt.wantViewing, handler.searchViewing())
			assert.Equal(t, tt.wantViewing, len(handler.vi.searchLocations()) > 0)
		})
	}
}

func TestHandlerSearchOpenCtrlCClosesInsteadOfInterrupting(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Search = testSearchConfig()
	handler := newSearchHandler(t, cfg)

	_, err := handler.comp.pty.Master.Write([]byte("printf 'needle\\n'\n"))
	require.NoError(t, err)
	waitForScrollback(t, handler, "needle")

	_, handled := handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	require.True(t, handled)
	for _, ch := range "needle" {
		handler.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	require.NotEmpty(t, handler.vi.searchLocations())

	before := scrollbackContent(handler)
	_, handled = handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'})
	assert.True(t, handled)
	assert.False(t, handler.searchBox.Active(), "ctrl-c closes the box, like <esc>")
	assert.True(t, handler.searchViewing(), "the match stays parked")
	assert.NotEmpty(t, handler.vi.searchLocations())

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, before, scrollbackContent(handler),
		"the shell must not react: ctrl-c never reached it")
}

func scrollbackContent(h *Handler) string {
	scroll, mu := h.comp.PrimaryScroll()
	mu.Lock()
	defer mu.Unlock()
	return scroll.Buffer().String()
}

func TestHandlerSearchViewSelection(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Search = testSearchConfig()
	handler := newSearchHandler(t, cfg)

	_, err := handler.comp.pty.Master.Write([]byte("printf 'needle\\n'\n"))
	require.NoError(t, err)
	waitForScrollback(t, handler, "needle")

	handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	require.True(t, handler.searchOpen())
	for _, ch := range "needle" {
		handler.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	require.NotEmpty(t, handler.vi.searchLocations())

	match, _ := handler.vi.sync.scroll.ScrollToWindowCoordinates(handler.vi.cursorAtScroll())
	for _, ev := range []term.Event{
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: match.X, MouseY: match.Y},
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: match.X + 5, MouseY: match.Y},
		{Type: term.EventMouse, Key: term.MouseRelease, MouseX: match.X + 5, MouseY: match.Y},
	} {
		handler.Handle(ev)
	}

	// the overlay holds the keyboard, so it is asked to copy
	selected, ok := handler.Selection()
	require.True(t, ok)
	require.Equal(t, "needle", selected)

	// the box is out of the way, but the selection is what gets copied
	_, handled := handler.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	require.True(t, handled)
	handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'c'})

	selected, ok = handler.Selection()
	assert.True(t, ok, "copy shortcuts must not drop the selection")
	assert.Equal(t, "needle", selected)
}

func TestHandlerSearchOverlayHidesContentUnderneath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Search = testSearchConfig()
	handler := newSearchHandler(t, cfg)
	handler.Resize(80, 24)

	_, err := handler.comp.pty.Master.Write(
		[]byte("yes " + strings.Repeat("x", 70) + " | head -40\n"))
	require.NoError(t, err)
	waitForScrollback(t, handler, strings.Repeat("x", 70))

	_, handled := handler.Handle(term.Event{Type: term.EventKey, Mod: term.ModMeta, Ch: 'f'})
	require.True(t, handled)

	w := term.NewStringWriter(80, 24)
	handler.Draw(w)
	require.NoError(t, w.Flush())

	pos := handler.searchOverlay.Position()
	lines := strings.Split(w.String(), "\n")
	require.Contains(t, lines[0], "x", "the live view is underneath")
	for y := pos.Y; y < pos.Y+handler.searchOverlay.Height(); y++ {
		row := []rune(lines[y])
		box := string(row[pos.X : pos.X+handler.searchOverlay.Width()])
		assert.NotContains(t, box, "x", "row %d shows the terminal through the box", y)
	}
}
