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

package html

import (
	"net/http"
	"net/url"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	htmlcomp "unstable.build/rune/internal/component/html"
	"unstable.build/rune/internal/component/markdown"
)

// Handler fetches and renders HTML pages as markdown with keyboard
// navigation, mouse selection, and link handling.
//
// While loading, it handles Ctrl+C to cancel the fetch and q/Esc to
// exit. Once the content is loaded, it provides less-like keyboard
// navigation (j/k, g/G, d/u, f/b, etc.), mouse text selection, and
// intercepts http/https link clicks to navigate to the new URL.
//
// Previously visited pages are cached so that navigating back does
// not require a new fetch.
//
// Keyboard controls:
//   - Up/k:          Scroll up one line
//   - Down/j:        Scroll down one line
//   - Page Up/b:     Scroll up one page
//   - Page Down/f:   Scroll down one page
//   - Space:         Scroll down one page
//   - Home/g:        Go to top
//   - End/G:         Go to bottom
//   - d:             Scroll down half page
//   - u:             Scroll up half page
//   - /:             Open search prompt
//   - n:             Jump to next search result
//   - N:             Jump to previous search result
//   - H/Alt+Left:    Go back in history
//   - L/Alt+Right:   Go forward in history
//   - r/F5:          Refresh current page
//   - q/Esc:         Exit (returns exit=true)
//   - Ctrl+C:        Cancel fetch (loading only)
//
// It implements [handler.ScrollableFloating] and [mouse.Delegate].
type Handler struct {
	interrupter term.Interrupter
	current     *htmlcomp.Component
	m           *mouse.Mouse
	cache       map[string]*htmlcomp.Component

	// Navigation history: ordered list of visited URLs and the
	// current position within it. historyIdx points at the entry
	// for h.current.
	history    []*url.URL
	historyIdx int

	selStart, selEnd term.Coordinates // document coords
	hasSelection     bool

	width, height int

	// Navigation bar state.
	bar         *navigationBar
	barPos      BarPosition
	contentDrag bool // true during a mouse drag started in the content area
	barDrag     bool // true during a mouse drag started in the bar input area

	// Component options (immutable after construction).
	httpClient *http.Client
	mdCfg      markdown.Config
	compOpts   []htmlcomp.Option

	// Selection highlight style.
	selectionAttrs term.Attributes
}

var _ handler.ScrollableFloating = (*Handler)(nil)
var _ mouse.Delegate = (*Handler)(nil)

// New creates a new HTML handler that fetches content from the given
// URL and renders it as markdown.
func New(interrupter term.Interrupter, u *url.URL, opts ...Option) *Handler {
	h := &Handler{
		interrupter:    interrupter,
		cache:          make(map[string]*htmlcomp.Component),
		httpClient:     http.DefaultClient,
		mdCfg:          markdown.DefaultConfig(),
		selectionAttrs: term.Attributes{Attrs: term.AttrReverse},
	}
	for _, opt := range opts {
		opt(h)
	}
	h.m = mouse.New(h)
	h.navigateTo(u)
	return h
}

// Close releases all resources associated with the handler by closing
// every cached component.
func (h *Handler) Close() error {
	for _, comp := range h.cache {
		_ = comp.Close()
	}
	return nil
}

// Resize updates the viewport dimensions.
func (h *Handler) Resize(width, height int) {
	h.width = width
	h.height = height
	h.current.Resize(width, h.contentHeight())
	if h.bar != nil {
		h.bar.Resize(width)
	}
}

// Draw renders the HTML content with selection highlighting.
func (h *Handler) Draw(w term.Writer) {
	if h.bar == nil {
		h.current.Draw(w)
		h.drawSelection(w, h.height)
		if r := h.current.Resolved(); r != nil {
			r.DrawSearchPrompt(w, h.height-1, h.width)
		}
		return
	}

	barY, contentY := h.barContentOffsets()
	ch := h.contentHeight()

	// Draw bar.
	barW := &component.VirtualWriter{
		Writer: w,
		Offset: term.Coordinates{X: 0, Y: barY},
		Width:  h.width,
		Height: barHeight,
	}
	h.bar.Draw(barW)

	// Draw content.
	contentW := &component.VirtualWriter{
		Writer: w,
		Offset: term.Coordinates{X: 0, Y: contentY},
		Width:  h.width,
		Height: ch,
	}
	h.current.Draw(contentW)
	h.drawSelection(contentW, ch)

	if r := h.current.Resolved(); r != nil {
		r.DrawSearchPrompt(w, h.height-1, h.width)
	}
}

// drawSelection renders the selection highlight overlay.
func (h *Handler) drawSelection(w term.Writer, viewHeight int) {
	if !h.hasSelection || h.current.State() != htmlcomp.StateLoaded {
		return
	}

	start, end := term.CoordinatesSort(h.selStart, h.selEnd)
	offset := h.current.Resolved().SeekOffset()

	for y := start.Y; y <= end.Y; y++ {
		screenY := y - offset
		if screenY < 0 || screenY >= viewHeight {
			continue
		}

		lineStart := 0
		lineEnd := h.width
		if y == start.Y {
			lineStart = start.X
		}
		if y == end.Y {
			lineEnd = end.X
		}

		for x := lineStart; x < lineEnd && x < h.width; x++ {
			w.UnionAttributes(term.Coordinates{X: x, Y: screenY}, h.selectionAttrs)
		}
	}
}

// Handle processes keyboard and mouse events.
func (h *Handler) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type == term.EventMouse {
		if h.bar != nil {
			return h.handleMouseWithBar(ev)
		}
		return h.handleContentMouse(ev)
	}

	if ev.Type != term.EventKey {
		return false, false
	}

	if h.bar != nil && h.bar.focused {
		return h.handleKeyBarFocused(ev)
	}

	if h.current.State() != htmlcomp.StateLoaded {
		return h.handleKeyLoading(ev)
	}

	return h.handleKeyLoaded(ev, h.current.Resolved())
}

// Cursor returns the cursor position, style, and visibility.
func (h *Handler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	if r := h.current.Resolved(); r != nil {
		if pos, style, show := r.SearchPromptCursor(h.height - 1); show {
			return pos, style, true
		}
	}
	if h.bar != nil && h.bar.focused {
		pos, style, show := h.bar.Cursor()
		barY, _ := h.barContentOffsets()
		pos.Y += barY
		return pos, style, show
	}
	return term.Coordinates{}, term.CursorStyleDefault, false
}

// Selection returns the selected text if any.
func (h *Handler) Selection() (string, bool) {
	if h.bar != nil && h.bar.focused {
		return h.bar.Selection()
	}
	if !h.hasSelection || h.current.State() != htmlcomp.StateLoaded {
		return "", false
	}
	r := h.current.Resolved()
	start, end := term.CoordinatesSort(h.selStart, h.selEnd)
	if start == end {
		return "", false
	}
	text := r.TextRange(start, end)
	return text, text != ""
}

// SeekUp scrolls the content up by one row.
func (h *Handler) SeekUp() bool {
	if h.current.State() == htmlcomp.StateLoaded {
		return h.current.Resolved().SeekUp()
	}
	return false
}

// SeekDown scrolls the content down by one row.
func (h *Handler) SeekDown() bool {
	if h.current.State() == htmlcomp.StateLoaded {
		return h.current.Resolved().SeekDown()
	}
	return false
}

// SeekOffset returns the current scroll offset.
func (h *Handler) SeekOffset() int {
	if h.current.State() == htmlcomp.StateLoaded {
		return h.current.Resolved().SeekOffset()
	}
	return 0
}

// MaxSeekOffset returns the maximum scroll offset.
func (h *Handler) MaxSeekOffset() int {
	if h.current.State() == htmlcomp.StateLoaded {
		return h.current.Resolved().MaxSeekOffset()
	}
	return 0
}

// Dimensions returns the ideal content dimensions.
// When the current page is still loading, it returns sensible defaults
// capped to the current viewport so the floating window keeps a stable
// size while content is fetched.
func (h *Handler) Dimensions() (width, height int) {
	if h.current.State() == htmlcomp.StateLoaded {
		return h.current.Dimensions()
	}
	w, ht := 100, 50
	if h.width > 0 {
		w = min(w, h.width)
	}
	if h.height > 0 {
		ht = min(ht, h.height)
	}
	return w, ht
}

// ScrollUp scrolls the content up by n lines.
func (h *Handler) ScrollUp(n int) bool {
	if h.current.State() != htmlcomp.StateLoaded {
		return false
	}
	r := h.current.Resolved()
	scrolled := false
	for range n {
		if r.SeekUp() {
			scrolled = true
		} else {
			break
		}
	}
	return scrolled
}

// ScrollDown scrolls the content down by n lines.
func (h *Handler) ScrollDown(n int) bool {
	if h.current.State() != htmlcomp.StateLoaded {
		return false
	}
	r := h.current.Resolved()
	scrolled := false
	for range n {
		if r.SeekDown() {
			scrolled = true
		} else {
			break
		}
	}
	return scrolled
}

// OnAction handles mouse actions. Returns true to suppress default behavior.
func (h *Handler) OnAction(ev term.Event, pos term.Coordinates, action mouse.Action) bool {
	if action != mouse.LeftClick {
		return false
	}

	if h.current.State() != htmlcomp.StateLoaded {
		return false
	}

	r := h.current.Resolved()
	link := r.LinkAt(pos.X, pos.Y)
	if link == nil || link.URL == "" {
		return false
	}

	parsed, err := url.Parse(link.URL)
	if err != nil {
		return true
	}
	if parsed.Scheme == "http" || parsed.Scheme == "https" {
		if parsed.Fragment != "" && h.samePageURL(parsed) {
			r.SeekToAnchor(parsed.Fragment)
			return true
		}
		h.navigateTo(parsed)
		return true
	}
	if parsed.Fragment != "" && parsed.Scheme == "" &&
		parsed.Host == "" && parsed.Path == "" {
		r.SeekToAnchor(parsed.Fragment)
	}
	return true
}

// SetSelectionStart sets the start of the selection.
func (h *Handler) SetSelectionStart(pos term.Coordinates) {
	h.selStart = h.screenToDoc(pos)
	h.selEnd = h.selStart
	h.hasSelection = true
}

// SetSelectionEnd sets the end of the selection.
func (h *Handler) SetSelectionEnd(pos term.Coordinates) {
	h.selEnd = h.screenToDoc(pos)
}

// ClearSelection clears any active selection.
func (h *Handler) ClearSelection() {
	h.hasSelection = false
	h.selStart = term.Coordinates{}
	h.selEnd = term.Coordinates{}
}

// SelectWordAt selects the word at the given position.
func (h *Handler) SelectWordAt(pos term.Coordinates) {
	if h.current.State() != htmlcomp.StateLoaded {
		return
	}
	r := h.current.Resolved()
	start, end := r.WordBoundsAt(pos.X, pos.Y)
	if start == end {
		return
	}
	h.selStart = h.screenToDoc(start)
	h.selEnd = h.screenToDoc(end)
	h.hasSelection = true
}

// SelectLine selects the entire line at the given y position.
func (h *Handler) SelectLine(y int) {
	h.selStart = h.screenToDoc(term.Coordinates{X: 0, Y: y})
	h.selEnd = h.screenToDoc(term.Coordinates{X: h.width, Y: y})
	h.hasSelection = true
}

// Width returns the current width.
func (h *Handler) Width() int { return h.width }

// Height returns the content viewport height (excluding any navigation bar).
func (h *Handler) Height() int { return h.contentHeight() }

// samePageURL reports whether u refers to the same page as the
// currently displayed URL (i.e. everything matches except the fragment).
func (h *Handler) samePageURL(u *url.URL) bool {
	if len(h.history) == 0 {
		return false
	}
	cur := h.history[h.historyIdx]
	return cur.Scheme == u.Scheme &&
		cur.Host == u.Host &&
		cur.Path == u.Path &&
		cur.RawQuery == u.RawQuery
}

// screenToDoc converts screen coordinates to document coordinates.
func (h *Handler) screenToDoc(pos term.Coordinates) term.Coordinates {
	offset := 0
	if h.current.State() == htmlcomp.StateLoaded {
		offset = h.current.Resolved().SeekOffset()
	}
	return term.Coordinates{
		X: pos.X,
		Y: pos.Y + offset,
	}
}

// handleKeyLoading handles keyboard events while loading, canceled, or errored.
func (h *Handler) handleKeyLoading(ev term.Event) (exit, handled bool) {
	// Alt+Arrow: history navigation (must work in all states).
	if ev.Mod&term.ModAlt != 0 {
		switch ev.Key {
		case term.KeyArrowLeft:
			h.goBack()
			return false, true
		case term.KeyArrowRight:
			h.goForward()
			return false, true
		}
	}

	switch ev.Key {
	case term.KeyEsc:
		return true, true
	case term.KeyF5:
		h.refresh()
		return false, true
	}

	// Only consume Ctrl+C when a fetch is actively in progress.
	if ev.Mod&term.ModCtrl != 0 && ev.Ch == 'c' &&
		h.current.State() == htmlcomp.StateLoading {
		h.current.Cancel()
		return false, true
	}

	switch ev.Ch {
	case 'q':
		return true, true
	case 'H':
		h.goBack()
		return false, true
	case 'L':
		h.goForward()
		return false, true
	case 'r':
		h.refresh()
		return false, true
	}

	return false, false
}

// handleKeyLoaded handles keyboard events after the content has loaded.
func (h *Handler) handleKeyLoaded(ev term.Event, r *markdown.Component) (exit, handled bool) {
	if r.HandleSearchKey(ev) {
		return false, true
	}

	// Alt+Arrow: history navigation.
	if ev.Mod&term.ModAlt != 0 {
		switch ev.Key {
		case term.KeyArrowLeft:
			h.goBack()
			return false, true
		case term.KeyArrowRight:
			h.goForward()
			return false, true
		}
	}

	switch ev.Key {
	case term.KeyEsc:
		return true, true

	case term.KeyArrowUp:
		r.SeekUp()
		return false, true

	case term.KeyArrowDown:
		r.SeekDown()
		return false, true

	case term.KeyPgup:
		h.scrollPage(r, -1)
		return false, true

	case term.KeyPgdn:
		h.scrollPage(r, 1)
		return false, true

	case term.KeyHome:
		h.scrollToTop(r)
		return false, true

	case term.KeyEnd:
		h.scrollToBottom(r)
		return false, true

	case term.KeySpace:
		h.scrollPage(r, 1)
		return false, true

	case term.KeyF5:
		h.refresh()
		return false, true
	}

	switch ev.Ch {
	case 'q':
		return true, true

	case '/':
		r.OpenSearchPrompt()
		return false, true

	case 'n':
		r.SeekToNextSearchResult()
		return false, true

	case 'N':
		r.SeekToPrevSearchResult()
		return false, true

	case 'k':
		r.SeekUp()
		return false, true

	case 'j':
		r.SeekDown()
		return false, true

	case 'g':
		h.scrollToTop(r)
		return false, true

	case 'G':
		h.scrollToBottom(r)
		return false, true

	case 'H':
		h.goBack()
		return false, true

	case 'L':
		h.goForward()
		return false, true

	case 'r':
		h.refresh()
		return false, true

	case 'b':
		h.scrollPage(r, -1)
		return false, true

	case 'f':
		h.scrollPage(r, 1)
		return false, true

	case 'd':
		h.scrollHalfPage(r, 1)
		return false, true

	case 'u':
		h.scrollHalfPage(r, -1)
		return false, true
	}

	return false, false
}

// scrollPage scrolls by one page. direction is -1 for up, 1 for down.
func (h *Handler) scrollPage(r *markdown.Component, direction int) {
	lines := max(1, h.contentHeight())
	if direction > 0 {
		for range lines {
			if !r.SeekDown() {
				break
			}
		}
	} else {
		for range lines {
			if !r.SeekUp() {
				break
			}
		}
	}
}

// scrollHalfPage scrolls by half a page. direction is -1 for up, 1 for down.
func (h *Handler) scrollHalfPage(r *markdown.Component, direction int) {
	lines := max(1, h.contentHeight()/2)
	if direction > 0 {
		for range lines {
			if !r.SeekDown() {
				break
			}
		}
	} else {
		for range lines {
			if !r.SeekUp() {
				break
			}
		}
	}
}

// scrollToTop scrolls to the beginning of the document.
func (h *Handler) scrollToTop(r *markdown.Component) {
	for r.SeekUp() {
	}
}

// scrollToBottom scrolls to the end of the document.
func (h *Handler) scrollToBottom(r *markdown.Component) {
	for r.SeekDown() {
	}
}

// navigateTo switches to the component for the given URL, creating a
// new one if it is not already in the cache. It pushes the URL onto
// the history stack, discarding any forward history.
func (h *Handler) navigateTo(u *url.URL) {
	// Truncate forward history.
	if len(h.history) > 0 {
		h.history = h.history[:h.historyIdx+1]
	}
	h.history = append(h.history, u)
	h.historyIdx = len(h.history) - 1

	h.showURL(u)
}

// goBack navigates to the previous page in history. Returns true if
// navigation occurred.
func (h *Handler) goBack() bool {
	if h.historyIdx <= 0 {
		return false
	}
	h.historyIdx--
	h.showURL(h.history[h.historyIdx])
	return true
}

// goForward navigates to the next page in history. Returns true if
// navigation occurred.
func (h *Handler) goForward() bool {
	if h.historyIdx >= len(h.history)-1 {
		return false
	}
	h.historyIdx++
	h.showURL(h.history[h.historyIdx])
	return true
}

// refresh re-fetches the current page by evicting it from the cache
// and creating a new component for the same URL.
func (h *Handler) refresh() {
	if len(h.history) == 0 {
		return
	}
	u := h.history[h.historyIdx]
	key := u.String()
	if old, ok := h.cache[key]; ok {
		_ = old.Close()
		delete(h.cache, key)
	}
	h.showURL(u)
}

// showURL switches to the component for the given URL, creating a new
// one if it is not already in the cache. Unlike navigateTo, it does
// not modify the history stack.
func (h *Handler) showURL(u *url.URL) {
	key := u.String()
	comp, ok := h.cache[key]
	if !ok {
		opts := make([]htmlcomp.Option, 0, len(h.compOpts)+2)
		opts = append(opts, htmlcomp.WithHTTPClient(h.httpClient))
		opts = append(opts, htmlcomp.WithMarkdownConfig(h.mdCfg))
		opts = append(opts, h.compOpts...)
		comp = htmlcomp.New(h.interrupter, u, opts...)
		h.cache[key] = comp
	}
	h.current = comp
	h.ClearSelection()
	if h.width > 0 || h.height > 0 {
		comp.Resize(h.width, h.contentHeight())
	}
	h.syncBar()
}

// ── Navigation bar helpers ──────────────────────────────────────────

// contentHeight returns the height available for the HTML content.
func (h *Handler) contentHeight() int {
	if h.bar != nil {
		return max(0, h.height-barHeight)
	}
	return h.height
}

// barContentOffsets returns the Y offsets for the bar and content areas.
func (h *Handler) barContentOffsets() (barY, contentY int) {
	if h.barPos == BarTop {
		return 0, barHeight
	}
	return h.height - barHeight, 0
}

// syncBar updates the navigation bar URL and button state from the
// current history entry.
func (h *Handler) syncBar() {
	if h.bar == nil || len(h.history) == 0 {
		return
	}
	h.bar.setURL(h.history[h.historyIdx].String())
	h.bar.buttons.backDim = h.historyIdx <= 0
	h.bar.buttons.fwdDim = h.historyIdx >= len(h.history)-1
}

// handleMouseWithBar routes mouse events between the bar and content.
func (h *Handler) handleMouseWithBar(ev term.Event) (exit, handled bool) {
	barY, contentY := h.barContentOffsets()

	// During a content drag, all events go to mouse.Mouse.
	if h.contentDrag {
		if ev.Key == term.MouseRelease {
			h.contentDrag = false
		}
		ev.MouseY -= contentY
		return h.handleContentMouse(ev)
	}

	// During a bar drag, all events go to the bar's frame so that
	// double-click, triple-click, and drag selection work correctly.
	if h.barDrag {
		if ev.Key == term.MouseRelease {
			h.barDrag = false
		}
		ev.MouseY -= barY
		return h.bar.handleMouse(ev)
	}

	// Left-clicks in the bar area are handled by the bar.
	if ev.Key == term.MouseLeft &&
		ev.MouseY >= barY && ev.MouseY < barY+barHeight {
		result := h.bar.handleClick(ev.MouseX, ev.MouseY-barY)
		switch result {
		case clickBack:
			h.goBack()
			return false, true
		case clickForward:
			h.goForward()
			return false, true
		case clickInput:
			h.barDrag = true
			if !h.bar.focused {
				h.bar.setFocused(true)
			}
			ev.MouseY -= barY
			return h.bar.handleMouse(ev)
		}
		return false, true
	}

	// Left-clicks in the content area start a drag and unfocus the bar.
	if ev.Key == term.MouseLeft {
		h.contentDrag = true
		if h.bar.focused {
			h.bar.setFocused(false)
		}
	}

	// Translate coordinates into content space and delegate.
	ev.MouseY -= contentY
	return h.handleContentMouse(ev)
}

// handleContentMouse handles a mouse event in content coordinates.
func (h *Handler) handleContentMouse(ev term.Event) (exit, handled bool) {
	if h.current.State() == htmlcomp.StateLoaded {
		link := h.current.Resolved().LinkAt(ev.MouseX, ev.MouseY)
		if link != nil && link.URL != "" {
			term.SetPointerShape(ev, term.PointerLink)
		}
	}
	return h.m.Handle(ev)
}

// handleKeyBarFocused handles keyboard events when the bar is focused.
func (h *Handler) handleKeyBarFocused(ev term.Event) (exit, handled bool) {
	switch ev.Key {
	case term.KeyEsc:
		h.bar.setFocused(false)
		return false, true
	case term.KeyEnter:
		urlStr := h.bar.input.Text()
		u, err := url.Parse(urlStr)
		if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			h.navigateTo(u)
		}
		h.bar.setFocused(false)
		return false, true
	}

	if ev.Mod&term.ModCtrl != 0 && ev.Ch == 'c' {
		h.bar.setFocused(false)
		return false, true
	}

	return h.bar.inner.Handle(ev)
}
