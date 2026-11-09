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

package search

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/junegunn/fzf/src/util"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
)

// List is a collection of elements that can be interactively searched.
type List struct {
	mu            sync.Mutex
	quitChan      chan struct{}
	input         [][]byte
	waitSearchCtx context.Context
	cancelSearch  func()
	waitPushCtx   context.Context
	cancelPush    func()
	height        int
	width         int
	// generation counts the times the data was discarded. A push
	// consumer carries the value it started under, so elements it
	// buffered before a discard are never appended after it.
	generation int

	cfg listConfig

	searchBar struct {
		component.Responsive
		component.Virtual[component.Responsive]
		minInputHeight int

		syncBuffer *cell.Buffer
		// this cannot be modified from this component
		// or else syncBuffer becomes out of sync.
		// All updates to the search buffer must
		// be performed via Buffer()
		internalRead *cell.Buffer
		dirty        bool
	}

	matchCountBar matchCounter
	list          struct {
		component.Virtual[*component.FocusList]
		component.FocusList
	}

	// userMovedFocus is set once the user manually moves the focus
	// (FocusUp/FocusDown). While set, the async streaming path appends
	// arriving results without re-sorting, so the user's selection is not
	// displaced. It is cleared whenever the result set is rebuilt.
	userMovedFocus bool
}

// NewList allocates storage for a new List and initializes it.
// See Init for more details.
func NewList(cfg ListConfig) *List {
	l := new(List)
	l.Init(cfg)
	return l
}

// Init initializes this config with cfg. Close must be called
// when this List is no longer to be used, or before Init
// is to be called again to reset the list.
func (l *List) Init(cfg ListConfig) {
	l.cfg = cfg.toInternal()

	l.searchBar.internalRead = cell.NewBuffer()
	l.searchBar.Responsive = tcomponent.Buffer(
		l.searchBar.internalRead, component.StringResponsiveConfig{
			StringConfig: component.StringConfig{
				Attributes:           l.cfg.textAttr,
				BackgroundAttributes: l.cfg.textAttr,
			},
		})
	l.searchBar.C = l.searchBar.Responsive
	l.searchBar.dirty = true
	l.searchBar.syncBuffer = cell.NewBuffer()
	l.searchBar.minInputHeight = 1
	l.searchBar.syncBuffer.Subscribe(syncBuffer{
		parent: l,
		buf:    l.searchBar.internalRead,
	})

	l.matchCountBar.init()

	l.list.FocusList.InitWithAttr(l.cfg.textAttr, l.cfg.focusAttr)
	l.list.FocusList.Inverted = l.cfg.bottomSearchBar
	l.list.C = &l.list.FocusList

	l.setFilesCount()
	l.quitChan = make(chan struct{})
	var ctx context.Context
	ctx, l.cancelSearch = context.WithCancel(context.Background())
	l.waitSearchCtx = ctx
	ctx, l.cancelPush = context.WithCancel(context.Background())
	l.waitPushCtx = ctx
	l.cancelSearch()
	l.cancelPush()
}

// ToggleCaseSensitivity toggles whether the search should be case sensitive or not.
// It returns the previous setting and launches a new search asynchronously.
func (l *List) ToggleCaseSensitivity() bool {
	l.cancelSearch()
	<-l.waitSearchCtx.Done()

	l.mu.Lock()
	defer l.mu.Unlock()
	ret := l.cfg.caseSensitive
	l.cfg.caseSensitive = !l.cfg.caseSensitive
	l.startSearchLocked(context.Background())
	return ret
}

// MatchCount returns how many elements match the search query so far.
func (l *List) MatchCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.list.Len()
}

// TotalCount returns the total number of elements in the list, whether
// they are matches or not.
func (l *List) TotalCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.input)
}

// Push returns a channel that can be used to push data to this list asynchronously.
// Clients should call close on the channel if no more data is expected.
// See PushSync for more details.
//
// No more than one goroutine should be calling Push concurrently with the rest
// of methods of this List.
func (l *List) Push(ctx context.Context) chan<- []byte {
	l.cancelPush()
	<-l.waitPushCtx.Done()

	l.mu.Lock()
	defer l.mu.Unlock()

	// waitPushCtx is a pure completion signal: it must complete only via
	// cancelWait, which the consumer calls after flushing its buffered
	// elements. Deriving it from the caller's ctx would let an outside
	// cancel complete it transitively, unblocking a subsequent Push (the
	// DrainList barrier) before the flush has landed.
	var cancelWait func()
	l.waitPushCtx, cancelWait = context.WithCancel(context.Background())

	// cancel from outside should cancel the consumer's ctx
	ctx, l.cancelPush = context.WithCancel(ctx)

	datachan := make(chan []byte)
	quitChan := l.quitChan
	generation := l.generation
	go debug.CapturePanicReport(func() {
		l.consumeAsyncElements(ctx, cancelWait, generation, datachan, quitChan)
	})
	return datachan
}

// Pause hints to this list that no more data is expected, for now.
// This should be called when pushing an initial large amount of data
// from an unbound source.
//
// Note that this should NOT be called when there's no more data
// remaining, in which case closing the channel returned by Push is
// more appropiate.
//
// Resuming is as easy as pushing new data to the channel returned by Push.
func (l *List) Pause() {
	// this API is preferable over an automated timer in consumeAsyncElements
	// to avoid adding extra logic to an already contentious and hot path
	l.mu.Lock()
	l.sortMatchesList()
	l.setFilesCount()
	interrupter := l.cfg.interrupter
	l.mu.Unlock()
	_ = interrupter.Interrupt(context.Background())
}

// PushSync pushes one element to this list and searches for a match on it.
// If data matches the search input, this element is appended to the list and
// this method returns true.
func (l *List) PushSync(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()

	data := [1][]byte{nil}
	data[0] = b
	l.pushData(data[:], nil, true)
}

// FocusUp moves the focus of the match list up.
func (l *List) FocusUp() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	moved := l.list.FocusUp()
	if moved {
		l.userMovedFocus = true
	}
	return moved
}

// FocusDown moves the focus of the match list down.
func (l *List) FocusDown() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	moved := l.list.FocusDown()
	if moved {
		l.userMovedFocus = true
	}
	return moved
}

// FocusStart moves the focus of the match list to the start.
func (l *List) FocusStart() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list.FocusStart()
}

// FocusEnd moves the focus of the match list to the end.
func (l *List) FocusEnd() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list.FocusEnd()
}

// Focus returns the match in the list currently in focus.
func (l *List) Focus() (Match, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	node, ok := l.list.Focus()
	if !ok {
		return Match{}, false
	}
	return node.Value().(searchResultComponent).Match, true
}

// SetFocus sets the focus of this List to node.
func (l *List) SetFocus(node component.ListNode) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Note: due to performance reasons, Match has to be
	// stack allocated and therefore it canot contain
	// a pointer to its component.ListNode, thus forcing
	// this List's API to take both ListNode and Match
	// rendering it somewhat inconsistent.
	// TODO: benchcmp with heap allocated Match exclusively
	// vs stack allocated but also perform holistic benchmark
	// with GC on a real live session.
	l.list.SetFocus(node)
}

// IterateVisible iterates only the visible elements in l.
func (l *List) IterateVisible(fn func(Match)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list.IterateVisible(func(c tui.Component) {
		fn(c.(searchResultComponent).Match)
	})
}

// DataReset resets the current data list. Elements that a consumer
// started by an earlier Push has received but not yet appended are
// dropped with it, so Push must come after DataReset, never before.
func (l *List) DataReset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cancelSearch()
	l.generation++
	l.input = nil
	l.list.Reset()
	l.userMovedFocus = false
	l.setFilesCount()
}

// RemoveFocus removes the item that is currently on focus.
func (l *List) RemoveFocus() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.cancelSearch()

	listNode, ok := l.list.Focus()

	if !ok {
		return false
	}

	match := listNode.Value().(searchResultComponent)
	matchIdx := match.idx

	// Remove the node on focus and shift focus if possible.
	l.list.Remove(&listNode)

	// Matches are sorted by score, so those built from later inputs can
	// sit anywhere in the list.
	for nn, ok := l.list.Front(); ok; nn, ok = nn.Next() {
		match := nn.Value().(searchResultComponent)
		if match.idx > matchIdx {
			match.idx--
			nn.SetValue(match)
		}
	}

	// Align the `l.list` with the `l.input`.
	if len(l.input) == 1 {
		l.input = [][]byte{}
	} else {
		l.input = append(
			l.input[:matchIdx],
			l.input[matchIdx+1:]...,
		)
	}

	l.setFilesCount()
	return true
}

// Wait waits for the current search to finish if any and returns.
func (l *List) Wait() {
	l.mu.Lock()
	searchCtx := l.waitSearchCtx
	pushCtx := l.waitPushCtx
	l.mu.Unlock()

	<-searchCtx.Done()
	<-pushCtx.Done()
}

// Cancel cancels the current search if there's any.
//
// Cancel can be used before Wait to ensure that
// we don't wait indefinitely.
func (l *List) Cancel() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cancelSearch()
	l.cancelPush()
}

// Draw satisfies tui.Component
func (l *List) Draw(w term.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.drawList(w)
	l.drawSearchBar(w)
	l.drawMatchCounts(w)
}

// Resize satisfies tui.Component
func (l *List) Resize(width, height int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.resize(width, height)
}

// InputHeight returns the component.Responsive Height of the input search bar.
func (l *List) InputHeight() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inputHeight()
}

// SetMinInputHeight sets the minimum search input field height.
func (l *List) SetMinInputHeight(height int) {
	if height < 1 {
		panic(fmt.Sprintf("invalid height: %d < 1", height))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.searchBar.minInputHeight = height
	l.resize(l.width, l.height)
}

// Buffer returns the search input buffer.
func (l *List) Buffer() *cell.Buffer {
	return l.searchBar.syncBuffer
}

// Offset returns this list's current seek offset.
func (l *List) Offset() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list.Offset()
}

// FocusOffset returns this list's focus index in the underlying list.
func (l *List) FocusOffset() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list.FocusOffset()
}

// ElementHeight returns the height for each element of this list.
func (l *List) ElementHeight() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list.ElementHeight()
}

// ElementAt returns the Match at the given position and true, if there's any
// or a zero-valued Match and false if there's none.
func (l *List) ElementAt(pos term.Coordinates) (Match, component.ListNode, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// NOTE returning a ListNode solely exist to enable usage of SetFocus. If SetFocus
	// ever uses a Match, this method should be removed.
	node, ok := l.list.ElementAt(pos)
	if !ok {
		return Match{}, component.ListNode{}, false
	}
	return node.Value().(searchResultComponent).Match, node, true
}

// SetFocusAttr sets the attributes of the focus nodes.
// The current focus node is changed and any future focused
// nodes will inherit the given attr.
func (l *List) SetFocusAttr(attr term.Attributes) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list.SetFocusAttr(attr)
}

// Close closes all the resources associated with this List.
func (l *List) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.cancelSearch()
	l.cancelPush()
	l.generation++
	l.input = nil
	l.list.Reset()
	if l.quitChan == nil {
		return nil
	}
	close(l.quitChan)
	l.quitChan = nil
	return nil
}

func (l *List) inputHeight() int {
	// search bar is used as prompt so always return a min of 1,
	// even if buffer is empty
	return int(math.Max(float64(l.searchBar.minInputHeight),
		float64(l.searchBar.Responsive.Height(l.width))))
}

func (l *List) setFilesCount() {
	doSetFilesCount(&l.matchCountBar, l.list.Len(), len(l.input), l.cfg.matchCountAttr)
}

func (l *List) sortMatchesList() {
	sortMatchesList(l.cfg.bottomSearchBar, &l.list.FocusList)
}

func (l *List) pushData(data [][]byte, slab *util.Slab, sortList bool) {
	idx := len(l.input)
	l.input = append(l.input, data...)

	searchInput := l.getSearchQuery()
	if len(searchInput) == 0 {
		template := component.LazyBytes{
			Attributes:      l.cfg.textAttr,
			TokenAttributes: l.cfg.matchedTextAttr,
		}
		for i, data := range data {
			match := Match{data: data, idx: idx + i}
			template.Data = match.data
			template.Tokens = match.tokens
			l.list.FocusList.PushBack(searchResultComponent{
				LazyBytes: template,
				Match:     match,
			})
		}
		if !sortList {
			return
		}
		if l.cfg.bottomSearchBar {
			l.sortMatchesList()
		}
		l.setFilesCount()
		return
	}

	template := component.LazyBytes{
		Attributes:      l.cfg.textAttr,
		TokenAttributes: l.cfg.matchedTextAttr,
	}
	search(l.cfg.algo, data, searchInput, slab, l.cfg.caseSensitive,
		func(match Match) {
			match.idx += idx
			template.Data = match.data
			template.Tokens = match.tokens
			l.list.FocusList.PushBack(searchResultComponent{
				LazyBytes: template,
				Match:     match,
			})
		})

	if sortList {
		l.sortMatchesList()
		l.setFilesCount()
	}
}

func (l *List) consumeAsyncElements(
	ctx context.Context, cancel func(), generation int,
	datachan chan []byte, quitChan chan struct{},
) {
	defer cancel()

	t1 := time.NewTicker(l.cfg.interruptEvery)
	defer t1.Stop()
	t2 := time.NewTicker(l.cfg.interruptEvery)
	defer t2.Stop()
	tickerCh := make(chan struct{}) // need a way to signal from below
	defer close(tickerCh)

	var dirty bool
	sort := func() {
		l.mu.Lock()
		if !dirty || l.generation != generation {
			l.mu.Unlock()
			return
		}
		interrupter := l.cfg.interrupter
		if (len(l.getSearchQuery()) != 0 || l.cfg.bottomSearchBar) && !l.userMovedFocus {
			l.sortMatchesList()
		}
		l.setFilesCount()
		dirty = false
		l.mu.Unlock()
		_ = interrupter.Interrupt(context.Background())
	}

	buf := make([][]byte, 0, l.cfg.setFileCountEvery)
	slab := makeSlab()
	pushData := func() {
		if len(buf) == 0 {
			return
		}
		l.mu.Lock()
		// A canceled ctx cannot stand in for this check: the Push
		// barrier relies on a canceled consumer still flushing.
		if l.generation != generation {
			l.mu.Unlock()
			buf = buf[:0]
			return
		}
		l.pushData(buf, slab, false)
		l.setFilesCount()
		dirty = true
		l.mu.Unlock()
		buf = buf[:0]
	}

	go debug.CapturePanicReport(func() {
		for {
			select {
			case <-t1.C:
				sort()
			case <-ctx.Done():
				return
			case <-quitChan:
				return
			case <-tickerCh:
				return
			}
		}
	})

	defer sort()
	defer pushData()
	for i := 0; ; i++ {
		select {
		case <-t2.C:
			pushData()
		case <-ctx.Done():
			return
		case data, ok := <-datachan:
			if !ok {
				return
			}
			buf = append(buf, data)
			if i%l.cfg.setFileCountEvery == 0 {
				pushData()
			}
		case <-quitChan:
			l.DataReset()
			buf = buf[:0] // avoid last push
			return
		}
	}
}

func (l *List) getSearchQuery() string {
	return l.searchBar.internalRead.String()
}

func (l *List) handleSearch(
	ctx context.Context, cancelFn func(), input [][]byte,
	searchInput string, caseSensitive bool,
) {
	// defer cancel search context so Wait can rely on searchCtx
	// to wait for search to be done. Cleanup resources
	// even if parent ctx.Done returns before the child ctx.
	defer cancelFn()

	// add matches to a goroutine-local focus list
	// to reduce lock contention while pushing matches to the list.
	// Also sort this temp list, rather than the final list,
	// which reduces contention.
	tempList := component.NewFocusList()
	slab := makeSlab()
	template := component.LazyBytes{
		Attributes:      l.cfg.textAttr,
		TokenAttributes: l.cfg.matchedTextAttr,
	}
	search(l.cfg.algo, input, searchInput, slab, caseSensitive,
		func(match Match) {
			template.Tokens = match.tokens
			template.Data = match.data
			tempList.PushBack(searchResultComponent{
				LazyBytes: template,
				Match:     match,
			})
		})
	sortMatchesList(l.cfg.bottomSearchBar, tempList)

	l.mu.Lock()
	defer l.cfg.interrupter.Interrupt(context.Background()) //nolint:errcheck

	// NOTE: there's a race condition to start pushing matches if we do not push
	// them in batch. Before we start pushing elements, ensure that the
	// context is still active.
	//
	// When a new search is triggered halfway through processing the input, the lock is
	// acquired, and a search is performed above in parallel with all the previosly consumed
	// items, as new items arrive, individual searches are performed by pushData on the
	// new items.
	select {
	case <-ctx.Done():
		// if search was canceled then we're done
		l.mu.Unlock()
		return
	default:
		// if we are holding the lock then nothing else is updating
		// the list or can cancel the search
	}

	for node, ok := tempList.Front(); ok; node, ok = node.Next() {
		l.list.PushBack(node.Value())
	}
	l.setFilesCount()
	l.mu.Unlock()
}

func (l *List) drawList(w term.Writer) {
	l.list.Virtual.Draw(w)
}

func (l *List) drawSearchBar(w term.Writer) {
	dirty := l.searchBar.dirty
	if dirty {
		l.resize(l.width, l.height)
	}
	l.searchBar.Virtual.Draw(w)
}

func (l *List) drawMatchCounts(w term.Writer) {
	if l.matchCountBar.width != getMatchCountBarWidth(&l.matchCountBar) {
		l.resize(l.width, l.height)
	}
	l.matchCountBar.Virtual.Draw(w)
}

func (l *List) asyncSearchLocked(ctx context.Context) {
	// waitSearchCtx completes only via cancelWait once handleSearch has
	// finished pushing matches; see the equivalent note in Push.
	var cancelWait func()
	l.waitSearchCtx, cancelWait = context.WithCancel(context.Background())

	// cancel from outside should cancel the search's ctx
	ctx, l.cancelSearch = context.WithCancel(ctx)

	input := make([][]byte, len(l.input))
	copy(input, l.input)
	searchInput := l.getSearchQuery()
	// A later toggle may flip this while the search is still scanning.
	caseSensitive := l.cfg.caseSensitive
	// reset data now, so moving forward, all the prior input
	// is managed by the goroutine below, and the new items are
	// managed by the Push goroutine
	l.list.Reset()
	l.userMovedFocus = false
	go debug.CapturePanicReport(func() {
		l.handleSearch(ctx, cancelWait, input, searchInput, caseSensitive)
	})
}

func (l *List) syncSearchLocked(ctx context.Context) {
	var cancelWait func()
	l.waitSearchCtx, cancelWait = context.WithCancel(context.Background())

	ctx, l.cancelSearch = context.WithCancel(ctx)

	input := make([][]byte, len(l.input))
	copy(input, l.input)
	searchInput := l.getSearchQuery()
	l.list.Reset()
	l.userMovedFocus = false
	l.mu.Unlock()
	defer l.mu.Lock()
	l.handleSearch(ctx, cancelWait, input, searchInput, l.cfg.caseSensitive)
}

func (l *List) startSearchLocked(ctx context.Context) {
	if l.cfg.syncSearch {
		l.syncSearchLocked(ctx)
		return
	}
	l.asyncSearchLocked(ctx)
}

func (l *List) resize(width, height int) {
	l.height = height
	l.width = width
	l.searchBar.dirty = false

	inputHeight := l.inputHeight()

	if height-inputHeight < 2 {
		l.list.Move(term.Coordinates{Y: 0})
		l.list.Virtual.Resize(width, height)
		l.searchBar.Virtual.Resize(0, 0)
		l.matchCountBar.Virtual.Resize(0, 0)
		return
	}

	lenFilesCounter := getMatchCountBarWidth(&l.matchCountBar)
	listHeight := height - inputHeight
	if !l.cfg.bottomSearchBar {
		l.list.Move(term.Coordinates{Y: inputHeight})
		l.list.Virtual.Resize(width, listHeight)

		l.searchBar.Virtual.Move(term.Coordinates{})
		l.searchBar.Virtual.Resize(width, inputHeight)
		resizeMatchCountBar(&l.matchCountBar, inputHeight-1, lenFilesCounter, width)
	} else {
		l.list.Move(term.Coordinates{Y: 0})
		l.list.Virtual.Resize(width, listHeight)

		l.searchBar.Virtual.Move(term.Coordinates{Y: listHeight})
		l.searchBar.Virtual.Resize(width, inputHeight)
		resizeMatchCountBar(&l.matchCountBar, listHeight+inputHeight-1, lenFilesCounter, width)
	}
}

type searchResultComponent struct {
	component.LazyBytes
	Match
}

// Draw renders one result row. component.LazyBytes maps each byte to a
// single-width cell, which is what makes it allocation free, but it
// mangles anything outside ASCII. Rows are only drawn when visible, so
// the cheap ASCII scan below keeps that fast path intact and pays for
// grapheme-aware rendering only on the rows that actually need it.
func (s searchResultComponent) Draw(w term.Writer) {
	if asciiRunLen(s.LazyBytes.Data) == len(s.LazyBytes.Data) {
		s.LazyBytes.Draw(w)
		return
	}
	s.drawGraphemes(w)
}

// drawGraphemes renders s.Data one grapheme cluster per cell. Clusters
// are stepped in place rather than materialized through
// term.StringToCells, which would allocate a cell matrix per row on
// every frame. fzf reports match positions as rune indices, so columns
// are tracked separately from rune offsets to stay correct across wide
// glyphs and combining marks.
func (s searchResultComponent) drawGraphemes(w term.Writer) {
	var tokens []int
	if s.LazyBytes.Tokens != nil {
		tokens = *s.LazyBytes.Tokens
	}
	data := s.LazyBytes.Data
	str := bytesToString(data)
	col, runeIdx, i := 0, 0, 0
	for i < len(data) {
		// Every combining mark is non-ASCII, so an ASCII byte followed
		// by another ASCII byte always stands alone and can be blitted
		// without consulting the grapheme state machine. Only the byte
		// abutting the next non-ASCII sequence may be a cluster base.
		end := i + asciiRunLen(data[i:])
		if end < len(data) {
			end--
		}
		for ; i < end; i++ {
			w.SetCell(term.Coordinates{X: col},
				term.NewCell(rune(data[i]), 1, s.Attributes))
			if containsTokenInRange(tokens, runeIdx, runeIdx+1) {
				w.UnionAttributes(term.Coordinates{X: col}, s.TokenAttributes)
			}
			col++
			runeIdx++
		}
		if i == len(data) {
			return
		}
		var cluster string
		var width uint8
		cluster, _, width, _ = graphemecluster.StepString(str[i:], -1)
		r, size := utf8.DecodeRuneInString(cluster)
		cell := term.NewCell(r, width, s.Attributes)
		next := runeIdx + 1
		if size < len(cluster) {
			combining := []rune(cluster[size:])
			cell.SetCombining(combining)
			next += len(combining)
		}
		w.SetCell(term.Coordinates{X: col}, cell)
		if containsTokenInRange(tokens, runeIdx, next) {
			w.UnionAttributes(term.Coordinates{X: col}, s.TokenAttributes)
		}
		i += len(cluster)
		runeIdx = next
		col += int(width)
	}
}

// bytesToString aliases data as a string. The match data pushed into a
// List is owned by the list and never mutated after it is stored, so
// the alias cannot observe a change, and avoiding the copy keeps
// redraws of non-ASCII rows allocation free.
func bytesToString(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(data), len(data))
}

// asciiRunLen returns the length of data's leading ASCII bytes. It
// mirrors the scan in term/vte: the mask is identical in every byte
// position, so byte order is irrelevant and a native read avoids a
// bswap on big-endian.
func asciiRunLen(data []byte) int {
	const highBits = 0x8080808080808080
	i := 0
	for ; i+8 <= len(data); i += 8 {
		if binary.NativeEndian.Uint64(data[i:])&highBits != 0 {
			break
		}
	}
	for ; i < len(data); i++ {
		if data[i] >= utf8.RuneSelf {
			break
		}
	}
	return i
}

// containsTokenInRange reports whether any token falls in [lo, hi).
// tokens is bounded by the query length, so a linear scan beats
// building an index.
func containsTokenInRange(tokens []int, lo, hi int) bool {
	for _, t := range tokens {
		if t >= lo && t < hi {
			return true
		}
	}
	return false
}

type matchCounter struct {
	width int
	cell.Buffer
	tcomponent.Scroll
	component.Virtual[*tcomponent.Scroll]
}

func (b *matchCounter) init() {
	b.Buffer.Init()
	b.Scroll.Init(&b.Buffer)
	b.C = &b.Scroll
}

type syncBuffer struct {
	parent *List
	buf    *cell.Buffer
}

func (s syncBuffer) OnWillEdit(
	ctx context.Context, start, end term.Coordinates, str string,
) {
	s.parent.cancelSearch()

	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()
	s.buf.Edit(ctx, start, end, str)
	s.parent.searchBar.dirty = true
	s.parent.startSearchLocked(ctx)
}

func (s syncBuffer) OnDidEdit(
	ctx context.Context, from, to term.Coordinates, old string,
) {
}

func sortMatchesList(bottomSearchBar bool, list *component.FocusList) {
	if bottomSearchBar {
		list.Sort(sortByResultScoreInverted)
	} else {
		list.Sort(sortByResultScore)
	}
}

func doSetFilesCount(matchCountBar *matchCounter, matches, total int, attr term.Attributes) {
	matchCountBar.Reset()
	matchCountBar.WriteStringWithAttr(strconv.Itoa(matches), attr)
	matchCountBar.WriteStringWithAttr("/", attr)
	matchCountBar.WriteStringWithAttr(strconv.Itoa(total), attr)
}

func getMatchCountBarWidth(matchCountBar *matchCounter) int {
	return matchCountBar.Buffer.Columns(0)
}

func resizeMatchCountBar(matchCountBar *matchCounter, y, lenFilesCounter, width int) {
	if width-lenFilesCounter <= 0 {
		matchCountBar.Virtual.Resize(0, 0)
		return
	}

	matchCountBar.width = lenFilesCounter
	matchCountBar.Move(term.Coordinates{
		Y: y,
		X: width - lenFilesCounter,
	})
	matchCountBar.Virtual.Resize(lenFilesCounter, 1)
}

func sortByResultScore(a, b tui.Component) bool {
	ab := a.(searchResultComponent)
	bb := b.(searchResultComponent)
	if ab.Match.res.Score == bb.Match.res.Score {
		return ab.Match.idx < bb.Match.idx
	}
	return ab.Match.res.Score > bb.Match.res.Score
}

func sortByResultScoreInverted(a, b tui.Component) bool {
	ab := a.(searchResultComponent)
	bb := b.(searchResultComponent)
	if ab.Match.res.Score == bb.Match.res.Score {
		return ab.Match.idx < bb.Match.idx
	}
	return ab.Match.res.Score < bb.Match.res.Score
}
