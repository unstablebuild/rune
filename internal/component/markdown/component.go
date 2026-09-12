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
	"strings"
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// Component renders markdown content in a terminal.
// It implements component.ScrollableFloating.
type Component struct {
	cfg          *Config
	blocks       []block
	blockHeights []int          // cached heights for each block at current width
	anchors      map[string]int // anchor slug -> block index

	width, height int
	offset        int
	totalHeight   int

	// heightCache memoizes Height for heightCacheWidth. Blocks are
	// immutable once parsed, so the cache only has to be dropped when
	// Init replaces them.
	heightCache      int
	heightCacheWidth int

	searchQuery   string
	searchResults []textapi.Location
	searchList    textapi.LocationList
	prompt        searchPrompt
}

var (
	_ component.ScrollableFloating = (*Component)(nil)
	_ component.Responsive         = (*Component)(nil)
)

// New creates a new markdown component with the given content
// using default styling. Returns an error if parsing fails.
func New(content string) (*Component, error) {
	return NewWithConfig(content, DefaultConfig())
}

// NewWithConfig creates a new markdown component with the given content
// and custom configuration. Returns an error if parsing fails.
func NewWithConfig(content string, cfg Config) (*Component, error) {
	c := &Component{
		cfg:     &cfg,
		anchors: make(map[string]int),
	}
	if err := c.Init(content); err != nil {
		return nil, err
	}
	return c, nil
}

// Init replaces the component's content by re-parsing the given markdown
// string using the existing configuration. It resets the scroll offset and
// recalculates block heights when the component has already been sized.
func (c *Component) Init(content string) error {
	blocks, err := parse(content, c.cfg)
	if err != nil {
		return err
	}
	c.closeBlocks()
	c.blocks = blocks
	c.heightCacheWidth = 0
	c.offset = 0
	if c.anchors == nil {
		c.anchors = make(map[string]int)
	} else {
		clear(c.anchors)
	}
	c.buildAnchors()
	if c.width > 0 {
		c.recalculateHeights(c.width)
	} else {
		c.blockHeights = c.blockHeights[:0]
		c.totalHeight = 0
	}
	if c.searchQuery != "" {
		c.runSearch()
	}
	return nil
}

// Anchor converts header text to a URL anchor slug.
// It lowercases text, replaces spaces with hyphens,
// and removes non-alphanumeric chars.
func Anchor(text string) string {
	var b strings.Builder
	prev := '-'
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prev = r
		} else if prev != '-' {
			b.WriteRune('-')
			prev = '-'
		}
	}
	result := b.String()
	return strings.Trim(result, "-")
}

// Resize updates the viewport dimensions and recalculates
// block heights if the width has changed.
func (c *Component) Resize(width, height int) {
	widthChanged := width != c.width
	c.width, c.height = width, height
	if widthChanged {
		c.recalculateHeights(width)
		if c.searchQuery != "" {
			c.runSearch()
		}
	}
	if max := c.MaxSeekOffset(); c.offset > max {
		c.offset = max
	}
}

// Draw renders the visible portion of the markdown content.
func (c *Component) Draw(w term.Writer) {
	if c.width <= 0 || c.height <= 0 {
		return
	}
	w = term.BoundsCheckWriter(c.width, c.height, w)
	y := -c.offset
	for i, blk := range c.blocks {
		blockHeight := c.blockHeights[i]
		if y+blockHeight <= 0 {
			y += blockHeight
			continue
		}
		if y >= c.height {
			break
		}
		vw := &component.VirtualWriter{
			Writer: w,
			Offset: term.Coordinates{X: 0, Y: y},
			Width:  c.width,
			Height: blockHeight,
		}
		blk.Draw(vw)
		y += blockHeight
	}

	if len(c.searchResults) == 0 {
		return
	}
	var currentLoc textapi.Location
	var hasCurrent bool
	if c.searchList != nil {
		currentLoc, hasCurrent = c.searchList.Current()
	}
	for _, loc := range c.searchResults {
		screenY := loc.From.Y - c.offset
		if screenY < 0 || screenY >= c.height {
			continue
		}
		attr := c.cfg.SearchMatch
		if hasCurrent && loc.From == currentLoc.From && loc.To == currentLoc.To {
			attr = c.cfg.SearchCurrent
		}
		for x := loc.From.X; x < loc.To.X && x < c.width; x++ {
			w.UnionAttributes(term.Coordinates{X: x, Y: screenY}, attr)
		}
	}
}

// SeekUp scrolls the content up by one row.
func (c *Component) SeekUp() bool {
	if c.offset > 0 {
		c.offset--
		return true
	}
	return false
}

// SeekDown scrolls the content down by one row.
func (c *Component) SeekDown() bool {
	if c.offset < c.MaxSeekOffset() {
		c.offset++
		return true
	}
	return false
}

// SeekOffset returns the current scroll offset.
func (c *Component) SeekOffset() int {
	return c.offset
}

// SeekTo scrolls to offset, clamped to the available content.
func (c *Component) SeekTo(offset int) {
	if offset < 0 {
		offset = 0
	}
	if maxOffset := c.MaxSeekOffset(); offset > maxOffset {
		offset = maxOffset
	}
	c.offset = offset
}

// MaxSeekOffset returns the maximum scroll offset.
func (c *Component) MaxSeekOffset() int {
	if c.totalHeight <= c.height {
		return 0
	}
	return c.totalHeight - c.height
}

// Dimensions returns the ideal width and height
// without wrapping or clipping.
func (c *Component) Dimensions() (width, height int) {
	maxWidth := 0
	totalHeight := 0
	for _, blk := range c.blocks {
		w, h := blk.Dimensions()
		if w > maxWidth {
			maxWidth = w
		}
		totalHeight += h
	}
	return maxWidth, totalHeight
}

// BlockHeights returns the cached block heights for debugging.
func (c *Component) BlockHeights() []int {
	return c.blockHeights
}

// LinkAt returns link information at the given screen
// coordinates relative to the component's viewport.
// Returns nil if no link is found at that position.
func (c *Component) LinkAt(x, y int) *LinkInfo {
	docY := y + c.offset
	blockY := 0
	for i, blk := range c.blocks {
		blockHeight := c.blockHeights[i]
		if docY >= blockY && docY < blockY+blockHeight {
			relY := docY - blockY
			if info := c.linkInBlock(blk, x, relY); info != nil {
				return info
			}
			// Each block's height includes a trailing spacing row.
			// If we landed on that spacing and the next block's first
			// row has a link, return it so clicks just above a link
			// still work.
			if relY == blockHeight-1 && i+1 < len(c.blocks) {
				if info := c.linkInBlock(c.blocks[i+1], x, 0); info != nil {
					return info
				}
			}
			return nil
		}
		blockY += blockHeight
	}
	return nil
}

func (c *Component) linkInBlock(blk block, x, relY int) *LinkInfo {
	_, url, ok := blk.SpanAt(x, relY)
	if ok && url != "" {
		text, _, _ := blk.SpanAt(x, relY)
		return &LinkInfo{URL: url, Text: text}
	}
	return nil
}

// SpanAt returns the text and URL at the given screen
// coordinates relative to the component's viewport.
func (c *Component) SpanAt(x, y int) (text, url string, ok bool) {
	docY := y + c.offset
	blockY := 0
	for i, blk := range c.blocks {
		blockHeight := c.blockHeights[i]
		if docY >= blockY && docY < blockY+blockHeight {
			relY := docY - blockY
			return blk.SpanAt(x, relY)
		}
		blockY += blockHeight
	}
	return
}

// SeekToAnchor scrolls to the header with the given
// anchor slug. Returns true if the anchor was found
// and scrolling occurred.
func (c *Component) SeekToAnchor(anchor string) bool {
	blockIdx, ok := c.anchors[anchor]
	if !ok {
		return false
	}

	y := 0
	for i := 0; i < blockIdx && i < len(c.blockHeights); i++ {
		y += c.blockHeights[i]
	}

	if y > c.MaxSeekOffset() {
		y = c.MaxSeekOffset()
	}
	if y == c.offset {
		return false
	}
	c.offset = y
	return true
}

// TextRange extracts text from the given coordinate range.
// Coordinates are in document space (not screen space).
// The range is inclusive of start and exclusive of end.
func (c *Component) TextRange(
	start, end term.Coordinates,
) string {
	if c.width <= 0 {
		return ""
	}

	start, end = term.CoordinatesSort(start, end)
	if start == end {
		return ""
	}

	var result strings.Builder
	docY := 0

	for i, blk := range c.blocks {
		blockHeight := c.blockHeights[i]
		blockEnd := docY + blockHeight

		if blockEnd <= start.Y {
			docY = blockEnd
			continue
		}
		if docY > end.Y || (docY == end.Y && end.X == 0) {
			break
		}

		for lineY := docY; lineY < blockEnd; lineY++ {
			if lineY < start.Y {
				continue
			}
			if lineY > end.Y || (lineY == end.Y && end.X == 0) {
				break
			}

			relY := lineY - docY
			startX := 0
			endX := c.width

			if lineY == start.Y {
				startX = start.X
			}
			if lineY == end.Y {
				endX = end.X
			}

			for x := startX; x < endX; x++ {
				ch, ok := blk.CharAt(x, relY)
				if ok && ch != 0 {
					result.WriteRune(ch)
				}
			}

			if lineY < end.Y-1 ||
				(lineY == end.Y-1 && end.X > 0) {
				result.WriteByte('\n')
			}
		}
		docY = blockEnd
	}

	return result.String()
}

// WordBoundsAt returns the start and end coordinates of
// the word at the given screen position. Returns zero
// coordinates if no word is found.
func (c *Component) WordBoundsAt(
	x, y int,
) (start, end term.Coordinates) {
	text, _, ok := c.SpanAt(x, y)
	if !ok || text == "" {
		return
	}

	startX := x
	for sx := x - 1; sx >= 0; sx-- {
		t, _, ok := c.SpanAt(sx, y)
		if !ok || t != text {
			break
		}
		startX = sx
	}

	relX := x - startX
	if relX < 0 || relX >= len(text) {
		return
	}

	wordStart := relX
	for wordStart > 0 && isWordCharMd(rune(text[wordStart-1])) {
		wordStart--
	}

	wordEnd := relX
	for wordEnd < len(text) && isWordCharMd(rune(text[wordEnd])) {
		wordEnd++
	}

	if !isWordCharMd(rune(text[relX])) {
		for i := relX + 1; i < len(text); i++ {
			if isWordCharMd(rune(text[i])) {
				wordStart = i
				wordEnd = i + 1
				for wordEnd < len(text) &&
					isWordCharMd(rune(text[wordEnd])) {
					wordEnd++
				}
				break
			}
		}
	}

	start = term.Coordinates{X: startX + wordStart, Y: y}
	end = term.Coordinates{X: startX + wordEnd, Y: y}
	return
}

// Search finds all case-insensitive occurrences of query in the rendered
// text and highlights them using Config.SearchMatch and Config.SearchCurrent.
// Pass an empty query to clear the search.
func (c *Component) Search(query string) {
	c.searchQuery = query
	c.runSearch()
}

// SearchQuery returns the active search query.
func (c *Component) SearchQuery() string {
	return c.searchQuery
}

// SeekToNextSearchResult advances the internal LocationList cursor and
// scrolls the viewport so the next match is visible. Returns false when
// there are no results or the end of the list is reached.
func (c *Component) SeekToNextSearchResult() bool {
	if c.searchList == nil {
		return false
	}
	loc, ok := c.searchList.Next()
	if !ok {
		return false
	}
	return c.seekToSearchResult(loc)
}

// SeekToPrevSearchResult moves the internal LocationList cursor backward
// and scrolls the viewport so the previous match is visible. Returns false
// when there are no results or the beginning of the list is reached.
func (c *Component) SeekToPrevSearchResult() bool {
	if c.searchList == nil {
		return false
	}
	loc, ok := c.searchList.Prev()
	if !ok {
		return false
	}
	return c.seekToSearchResult(loc)
}

// OpenSearchPrompt opens the less-style "/" search input bar.
func (c *Component) OpenSearchPrompt() {
	c.prompt.open()
}

// IsSearchPromptActive returns true if the search prompt is visible.
func (c *Component) IsSearchPromptActive() bool {
	return c.prompt.isActive()
}

// HandleSearchKey processes a key event for the search prompt.
// When the prompt is not active it returns false and the caller
// should handle the event. When active it consumes the event and
// returns true. On confirm it automatically runs the search.
func (c *Component) HandleSearchKey(ev term.Event) bool {
	switch c.prompt.handleKey(ev) {
	case searchIgnored:
		return false
	case searchConsumed:
		return true
	case searchConfirm:
		if q := c.prompt.query(); q != "" {
			c.Search(q)
			c.seekToCurrentSearchResult()
		}
		return true
	case searchCancel:
		return true
	}
	return false
}

// DrawSearchPrompt renders the search prompt at the given y position.
func (c *Component) DrawSearchPrompt(w term.Writer, y, width int) {
	c.prompt.draw(w, y, width)
}

// SearchPromptCursor returns the cursor position for the search prompt.
func (c *Component) SearchPromptCursor(y int) (term.Coordinates, term.CursorStyle, bool) {
	return c.prompt.cursor(y)
}

func (c *Component) seekToCurrentSearchResult() {
	if c.searchList == nil {
		return
	}
	if loc, ok := c.searchList.Current(); ok {
		c.seekToSearchResult(loc)
	}
}

func (c *Component) seekToSearchResult(loc textapi.Location) bool {
	screenY := loc.From.Y - c.offset
	if screenY >= 0 && screenY < c.height {
		return true
	}
	newOffset := loc.From.Y - c.height/2
	if newOffset < 0 {
		newOffset = 0
	}
	if m := c.MaxSeekOffset(); newOffset > m {
		newOffset = m
	}
	c.offset = newOffset
	return true
}

func (c *Component) runSearch() {
	// Clear element slots: textapi.Location holds Message/Icon strings.
	clear(c.searchResults)
	c.searchResults = c.searchResults[:0]
	c.searchList = nil

	if c.searchQuery == "" || c.width <= 0 {
		return
	}

	queryRunes := []rune(c.searchQuery)
	queryLen := len(queryRunes)
	if queryLen == 0 {
		return
	}

	docY := 0
	for i, blk := range c.blocks {
		blockHeight := c.blockHeights[i]
		for relY := range blockHeight {
			line := make([]rune, c.width)
			for x := range c.width {
				ch, ok := blk.CharAt(x, relY)
				if !ok || ch == 0 {
					line[x] = ' '
				} else {
					line[x] = ch
				}
			}
			absY := docY + relY
			for x := 0; x <= c.width-queryLen; x++ {
				match := true
				for qi := range queryLen {
					if line[x+qi] != queryRunes[qi] {
						match = false
						break
					}
				}
				if match {
					c.searchResults = append(c.searchResults, textapi.Location{
						From: term.Coordinates{X: x, Y: absY},
						To:   term.Coordinates{X: x + queryLen, Y: absY},
						Attr: c.cfg.SearchMatch,
					})
					x += queryLen - 1
				}
			}
		}
		docY += blockHeight
	}

	if len(c.searchResults) > 0 {
		c.searchList = textapi.LocationSlice(c.searchResults)
	}
}

// Close cancels any in-flight syntax highlighting goroutines.
func (c *Component) Close() error {
	c.closeBlocks()
	return nil
}

func (c *Component) closeBlocks() {
	for _, blk := range c.blocks {
		if cb, ok := blk.(*codeBlock); ok {
			cb.close()
		}
	}
}

func (c *Component) buildAnchors() {
	for i, blk := range c.blocks {
		if h, ok := blk.(*headerBlock); ok {
			anchor := Anchor(h.content.String())
			if anchor != "" {
				c.anchors[anchor] = i
			}
		}
	}
}

// Height returns the total height needed to render all blocks at the
// given width. This is a pure calculation with no side effects.
func (c *Component) Height(width int) int {
	if width > 0 && c.heightCacheWidth == width {
		return c.heightCache
	}
	total := 0
	for _, blk := range c.blocks {
		total += blk.Height(width)
	}
	c.heightCache, c.heightCacheWidth = total, width
	return total
}

func (c *Component) recalculateHeights(width int) {
	c.blockHeights = make([]int, len(c.blocks))
	c.totalHeight = 0
	for i, blk := range c.blocks {
		h := blk.Height(width)
		blk.Resize(width, h)
		c.blockHeights[i] = h
		c.totalHeight += h
	}
}

func isWordCharMd(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}
