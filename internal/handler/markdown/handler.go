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

package markdown

import (
	"net/url"

	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
)

// Handler wraps a markdown.Component with mouse selection, link handling,
// and less-like keyboard navigation.
//
// Keyboard controls:
//   - Up/k:        Scroll up one line
//   - Down/j:      Scroll down one line
//   - Ctrl-y:      Scroll up one line
//   - Ctrl-e:      Scroll down one line
//   - Page Up/b/Ctrl-b:   Scroll up one page
//   - Page Down/f/Ctrl-f: Scroll down one page
//   - Space:       Scroll down one page
//   - Home/g:      Go to top
//   - End/G:       Go to bottom
//   - d/Ctrl-d:    Scroll down half page
//   - u/Ctrl-u:    Scroll up half page
//   - /:           Open search prompt
//   - n:           Jump to next search result
//   - N:           Jump to previous search result
//   - q/Esc:       Exit (returns exit=true)
//
// It implements handler.ScrollableFloating and handler.Responsive.
type Handler struct {
	comp           *markdown.Component
	mouse          *mouse.Mouse
	selStart       term.Coordinates // document coords
	selEnd         term.Coordinates // document coords
	hasSelection   bool
	width, height  int
	onLinkClick    func(*url.URL) bool
	selectionAttrs term.Attributes
}

var _ handler.ScrollableFloating = (*Handler)(nil)
var _ handler.Responsive = (*Handler)(nil)

// mouseDelegate adapts a Handler to satisfy mouse.Delegate, which requires
// Height() int (viewport height), while the handler's own Height(width int) int
// satisfies handler.Responsive.
type mouseDelegate struct {
	*Handler
}

var _ mouse.Delegate = (*mouseDelegate)(nil)

// Height returns the viewport height for the mouse delegate.
func (d *mouseDelegate) Height() int {
	return d.height
}

// New creates a new markdown handler wrapping the given component.
func New(comp *markdown.Component, opts ...Option) *Handler {
	h := &Handler{
		comp:           comp,
		selectionAttrs: term.Attributes{Attrs: term.AttrReverse},
	}
	for _, opt := range opts {
		opt(h)
	}
	h.mouse = mouse.New(&mouseDelegate{h})
	return h
}

// Component returns the underlying markdown component.
func (h *Handler) Component() *markdown.Component {
	return h.comp
}

// SetComponent swaps the underlying markdown component. Selection
// state is cleared and the component is resized to the handler's
// current dimensions.
func (h *Handler) SetComponent(comp *markdown.Component) {
	h.comp = comp
	h.ClearSelection()
	if h.width > 0 || h.height > 0 {
		h.comp.Resize(h.width, h.height)
	}
}

// Close cancels any in-flight syntax highlighting goroutines
// owned by the underlying component.
func (h *Handler) Close() error {
	return h.comp.Close()
}

// Resize updates the viewport dimensions.
func (h *Handler) Resize(width, height int) {
	h.width = width
	h.height = height
	h.comp.Resize(width, height)
}

// Draw renders the markdown content with selection highlighting.
func (h *Handler) Draw(w term.Writer) {
	h.comp.Draw(w)

	if h.hasSelection {
		start, end := term.CoordinatesSort(h.selStart, h.selEnd)
		offset := h.comp.SeekOffset()

		for y := start.Y; y <= end.Y; y++ {
			screenY := y - offset
			if screenY < 0 || screenY >= h.height {
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

	h.comp.DrawSearchPrompt(w, h.height-1, h.width)
}

// Handle processes keyboard and mouse events.
// Mouse events are delegated to the mouse handler.
// Keyboard events provide less-like navigation.
func (h *Handler) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type == term.EventMouse {
		return h.mouse.Handle(ev)
	}

	if ev.Type != term.EventKey {
		return false, false
	}

	if h.comp.HandleSearchKey(ev) {
		return false, true
	}

	switch ev.Key {
	case term.KeyEsc:
		return true, true

	case term.KeyArrowUp:
		h.comp.SeekUp()
		return false, true

	case term.KeyArrowDown:
		h.comp.SeekDown()
		return false, true

	case term.KeyPgup:
		h.scrollPage(-1)
		return false, true

	case term.KeyPgdn:
		h.scrollPage(1)
		return false, true

	case term.KeyHome:
		h.scrollToTop()
		return false, true

	case term.KeyEnd:
		h.scrollToBottom()
		return false, true

	case term.KeySpace:
		h.scrollPage(1)
		return false, true
	}

	if ev.Mod == term.ModCtrl {
		switch ev.Ch {
		case 'p':
			h.comp.SeekUp()
			return false, true
		case 'n':
			h.comp.SeekDown()
			return false, true
		case 'f': // page down
			h.scrollPage(1)
			return false, true
		case 'b': // page up
			h.scrollPage(-1)
			return false, true
		case 'd': // half page down
			h.scrollHalfPage(1)
			return false, true
		case 'u': // half page up
			h.scrollHalfPage(-1)
			return false, true
		case 'e': // one line down
			h.comp.SeekDown()
			return false, true
		case 'y': // one line up
			h.comp.SeekUp()
			return false, true
		}
	}

	// A remaining Ctrl/Alt/Meta modifier means the user is invoking a
	// host keybinding (e.g. <meta-n>); let it fall through unhandled so
	// the IDE can dispatch the bound command instead of scrolling.
	if ev.Mod&(term.ModCtrl|term.ModAlt|term.ModMeta) != 0 {
		return false, false
	}

	switch ev.Ch {
	case 'q':
		return true, true

	case '/':
		h.comp.OpenSearchPrompt()
		return false, true

	case 'n':
		h.comp.SeekToNextSearchResult()
		return false, true

	case 'N':
		h.comp.SeekToPrevSearchResult()
		return false, true

	case 'k':
		h.comp.SeekUp()
		return false, true

	case 'j':
		h.comp.SeekDown()
		return false, true

	case 'g':
		h.scrollToTop()
		return false, true

	case 'G':
		h.scrollToBottom()
		return false, true

	case 'b': // page up (like less)
		h.scrollPage(-1)
		return false, true

	case 'f': // page down (like less)
		h.scrollPage(1)
		return false, true

	case 'd': // half page down
		h.scrollHalfPage(1)
		return false, true

	case 'u': // half page up
		h.scrollHalfPage(-1)
		return false, true
	}

	return false, false
}

// Cursor returns the cursor position, style, and visibility.
// Shows a cursor when the search prompt is active.
func (h *Handler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	if pos, style, show := h.comp.SearchPromptCursor(h.height - 1); show {
		return pos, style, true
	}
	return term.Coordinates{}, term.CursorStyleDefault, false
}

// Selection returns the selected text if any.
func (h *Handler) Selection() (string, bool) {
	if !h.hasSelection {
		return "", false
	}

	start, end := term.CoordinatesSort(h.selStart, h.selEnd)
	if start == end {
		return "", false
	}

	text := h.comp.TextRange(start, end)
	return text, text != ""
}

// SeekUp scrolls the content up.
func (h *Handler) SeekUp() bool {
	return h.comp.SeekUp()
}

// SeekDown scrolls the content down.
func (h *Handler) SeekDown() bool {
	return h.comp.SeekDown()
}

// SeekOffset returns the current scroll offset.
func (h *Handler) SeekOffset() int {
	return h.comp.SeekOffset()
}

// MaxSeekOffset returns the maximum scroll offset.
func (h *Handler) MaxSeekOffset() int {
	return h.comp.MaxSeekOffset()
}

// Dimensions returns the ideal content dimensions.
func (h *Handler) Dimensions() (width, height int) {
	return h.comp.Dimensions()
}

// OnAction handles mouse actions. Returns true to suppress default behavior.
func (h *Handler) OnAction(ev term.Event, pos term.Coordinates, action mouse.Action) bool {
	if action != mouse.LeftClick {
		return false
	}

	link := h.comp.LinkAt(pos.X, pos.Y)
	if link != nil && link.URL != "" {
		parsed, err := url.Parse(link.URL)
		if err != nil {
			return true
		}
		if h.onLinkClick != nil && h.onLinkClick(parsed) {
			return true
		}
		// fallback to local anchors
		if parsed.Fragment != "" && parsed.Scheme == "" && parsed.Host == "" && parsed.Path == "" {
			h.comp.SeekToAnchor(parsed.Fragment)
		}
		return true
	}

	return false
}

// Search finds all case-insensitive occurrences of query in the rendered
// text and highlights them. Delegates to the underlying component.
func (h *Handler) Search(query string) {
	h.comp.Search(query)
}

// SeekToNextSearchResult advances to the next search match and scrolls
// the viewport to make it visible. Delegates to the underlying component.
func (h *Handler) SeekToNextSearchResult() bool {
	return h.comp.SeekToNextSearchResult()
}

// SeekToPrevSearchResult moves to the previous search match and scrolls
// the viewport to make it visible. Delegates to the underlying component.
func (h *Handler) SeekToPrevSearchResult() bool {
	return h.comp.SeekToPrevSearchResult()
}

// ScrollUp scrolls the content up by n lines.
func (h *Handler) ScrollUp(n int) bool {
	scrolled := false
	for range n {
		if h.comp.SeekUp() {
			scrolled = true
		} else {
			break
		}
	}
	return scrolled
}

// ScrollDown scrolls the content down by n lines.
func (h *Handler) ScrollDown(n int) bool {
	scrolled := false
	for range n {
		if h.comp.SeekDown() {
			scrolled = true
		} else {
			break
		}
	}
	return scrolled
}

// SetSelectionStart sets the start of the selection.
func (h *Handler) SetSelectionStart(pos term.Coordinates) {
	// Convert screen coordinates to document coordinates
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
	start, end := h.comp.WordBoundsAt(pos.X, pos.Y)
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
func (h *Handler) Width() int {
	return h.width
}

// Height returns the total height needed to render the markdown content at the
// given width. It delegates to the underlying component's Height method. This
// satisfies the component.Responsive interface.
func (h *Handler) Height(width int) int {
	return h.comp.Height(width)
}

// screenToDoc converts screen coordinates to document coordinates.
func (h *Handler) screenToDoc(pos term.Coordinates) term.Coordinates {
	return term.Coordinates{
		X: pos.X,
		Y: pos.Y + h.comp.SeekOffset(),
	}
}

// scrollPage scrolls by one page. direction is -1 for up, 1 for down.
func (h *Handler) scrollPage(direction int) {
	lines := max(1, h.height)
	if direction > 0 {
		for range lines {
			if !h.comp.SeekDown() {
				break
			}
		}
	} else {
		for range lines {
			if !h.comp.SeekUp() {
				break
			}
		}
	}
}

// scrollHalfPage scrolls by half a page. direction is -1 for up, 1 for down.
func (h *Handler) scrollHalfPage(direction int) {
	lines := max(1, h.height/2)
	if direction > 0 {
		for range lines {
			if !h.comp.SeekDown() {
				break
			}
		}
	} else {
		for range lines {
			if !h.comp.SeekUp() {
				break
			}
		}
	}
}

// scrollToTop scrolls to the beginning of the document.
func (h *Handler) scrollToTop() {
	for h.comp.SeekUp() {
	}
}

// scrollToBottom scrolls to the end of the document.
func (h *Handler) scrollToBottom() {
	for h.comp.SeekDown() {
	}
}
