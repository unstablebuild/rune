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

package emacs

import (
	"context"
	"strconv"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerhistory"
)

// prefixState collects a GNU numeric argument for the next command:
// C-u multiplies by four, digits (plain, M- or C-) build a number, and a
// leading '-' (or C-- / M--) negates it.
type prefixState struct {
	active bool
	// raw counts bare C-u presses; the argument is 4^raw until digits or
	// a sign arrive.
	raw       int
	hasDigits bool
	digits    int
	neg       bool
	// terminated is set by C-u after digits or a sign so the following
	// digit self-inserts: C-u 5 C-u 7 inserts 77777.
	terminated bool
}

// value returns the numeric argument the collected state stands for.
func (p prefixState) value() int {
	if p.hasDigits {
		if p.neg {
			return -p.digits
		}
		return p.digits
	}
	if p.neg {
		return -1
	}
	v := 1
	for range p.raw {
		v *= 4
	}
	return v
}

// rawOnly reports a bare C-u argument (no digits, no sign). Some commands
// treat it specially: C-SPC pops the mark ring, C-y leaves point before
// the inserted text.
func (p prefixState) rawOnly() bool { return !p.hasDigits && !p.neg }

func prefixDigit(ev term.Event) (int, bool) {
	if ev.Key != 0 || ev.Ch < '0' || ev.Ch > '9' {
		return 0, false
	}
	switch ev.Mod {
	case 0, term.ModAlt, term.ModCtrl, term.ModCtrlAlt:
		return int(ev.Ch - '0'), true
	}
	return 0, false
}

func prefixMinus(ev term.Event) bool {
	if ev.Ch != '-' {
		return false
	}
	switch ev.Mod {
	case 0, term.ModAlt, term.ModCtrl, term.ModCtrlAlt:
		return true
	}
	return false
}

// startPrefixArg begins collecting when ev is a prefix entry key: C-u,
// a digit-argument chord (M-5, C-5, C-M-5) or negative-argument (C--,
// M--, C-M--).
func (h *emacsHandler) startPrefixArg(ev term.Event) bool {
	if ev.Type != term.EventKey {
		return false
	}
	switch {
	case ev.Mod == term.ModCtrl && ev.Ch == 'u':
		h.prefix = prefixState{active: true, raw: 1}
	case ev.Mod != 0 && prefixMinus(ev):
		h.prefix = prefixState{active: true, neg: true}
	case ev.Mod != 0:
		d, ok := prefixDigit(ev)
		if !ok {
			return false
		}
		h.prefix = prefixState{active: true, hasDigits: true, digits: d}
	default:
		return false
	}
	h.renderPrefixArg()
	h.setTransientMode("ARG")
	return true
}

// collectPrefixKey consumes ev into the pending argument when it extends
// it (more C-u, digits, a sign) or aborts it (C-g). It returns false when
// ev is the command that consumes the argument.
func (h *emacsHandler) collectPrefixKey(ev term.Event) bool {
	if ev.Type != term.EventKey {
		return false
	}
	switch {
	case ev.Mod == term.ModCtrl && ev.Ch == 'g':
		h.prefix = prefixState{}
		h.less.SetMessage("Quit")
		h.setTransientMode("")
		return true
	case ev.Mod == term.ModCtrl && ev.Ch == 'u':
		if h.prefix.hasDigits || h.prefix.neg {
			// C-u ends digit collection so the next digit self-inserts.
			h.prefix.terminated = true
		} else {
			h.prefix.raw++
		}
	case prefixMinus(ev) && !h.prefix.hasDigits && !h.prefix.neg && !h.prefix.terminated:
		h.prefix.neg = true
	default:
		d, ok := prefixDigit(ev)
		if !ok || h.prefix.terminated {
			return false
		}
		h.prefix.hasDigits = true
		// Saturate instead of overflowing on absurdly long numbers.
		if h.prefix.digits < 100_000_000 {
			h.prefix.digits = h.prefix.digits*10 + d
		}
	}
	h.renderPrefixArg()
	return true
}

// renderPrefixArg echoes the argument collected so far.
func (h *emacsHandler) renderPrefixArg() {
	p := h.prefix
	if p.hasDigits || p.neg {
		sign := ""
		if p.neg {
			sign = "-"
		}
		num := ""
		if p.hasDigits {
			num = strconv.Itoa(p.digits)
		}
		h.less.SetMessage("C-u %s%s", sign, num)
		return
	}
	h.less.SetMessage("%s", strings.TrimSpace(strings.Repeat("C-u ", p.raw)))
}

// dispatchCounted executes ev with the collected numeric argument: GNU
// special cases first, then negative arguments through the mirror table,
// then plain repetition of the dispatch.
func (h *emacsHandler) dispatchCounted(
	ctx context.Context, ev term.Event, count int, raw bool, pasted *bool,
) (handled bool) {
	if ev.Type != term.EventKey {
		return false
	}
	switch ev.Mod {
	case term.ModCtrl:
		switch {
		case ev.Key == term.KeySpace:
			// C-u C-SPC: pop the mark ring and jump to the popped mark.
			return h.popMarkJump()
		case ev.Ch == 'k':
			return h.killLines(count)
		case ev.Ch == 'y':
			return h.yankArg(count, raw, pasted)
		case ev.Ch == 'q':
			// C-u N C-q inserts the quoted character N times.
			return h.startQuotedInsert(count)
		case ev.Ch == 'v':
			// GNU scroll commands take the argument in lines, not pages.
			return h.scrollLines(count)
		case ev.Ch == 't' && count < 0:
			// Transposing with a negative argument is not supported.
			return false
		}
	case term.ModAlt:
		switch {
		case ev.Ch == 'v':
			return h.scrollLines(-count)
		case ev.Ch == 'z':
			if count == 0 {
				return false
			}
			return h.startZap(count)
		case ev.Ch == 'k' && count < 0:
			// kill-sentence with a negative argument kills backward.
			return h.killSentencesBackward(-count)
		case (ev.Ch == 'u' || ev.Ch == 'l' || ev.Ch == 'c') && count < 0:
			// Case commands with a negative argument affect the previous
			// words without moving point.
			return h.caseWordsBackward(ev.Ch, -count)
		case ev.Ch == 't' && count < 0:
			return false
		}
	case term.ModCtrlShift:
		if ev.Key == term.KeyBackspace {
			return h.killWholeLine(count)
		}
	}
	if count < 0 {
		mirror, ok := mirrorEvent(ev)
		if !ok {
			return false
		}
		ev = mirror
		count = -count
	}
	for i := 0; i < count; i++ {
		if !h.dispatchKey(ctx, ev, false, pasted) {
			return i > 0
		}
		// Counted kills accumulate across iterations like consecutive
		// kill commands.
		h.lastKill = h.killedNow
	}
	return true
}

// mirrorEvent returns the opposite-direction command used to execute a
// negative argument.
func mirrorEvent(ev term.Event) (term.Event, bool) {
	flip := func(key term.Key) term.Event {
		return term.Event{Type: term.EventKey, Mod: ev.Mod, Key: key}
	}
	flipCh := func(ch rune) term.Event {
		return term.Event{Type: term.EventKey, Mod: ev.Mod, Ch: ch}
	}
	switch ev.Key {
	case term.KeyArrowRight:
		return flip(term.KeyArrowLeft), true
	case term.KeyArrowLeft:
		return flip(term.KeyArrowRight), true
	case term.KeyArrowDown:
		return flip(term.KeyArrowUp), true
	case term.KeyArrowUp:
		return flip(term.KeyArrowDown), true
	}
	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyDelete:
			return flip(term.KeyBackspace), true
		case term.KeyBackspace:
			return flip(term.KeyDelete), true
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'f':
			return flipCh('b'), true
		case 'b':
			return flipCh('f'), true
		case 'n':
			return flipCh('p'), true
		case 'p':
			return flipCh('n'), true
		case 'd':
			return term.Event{Type: term.EventKey, Key: term.KeyBackspace}, true
		case 'h':
			return term.Event{Type: term.EventKey, Key: term.KeyDelete}, true
		}
	case term.ModAlt:
		switch ev.Key {
		case term.KeyBackspace:
			return flipCh('d'), true
		case term.KeyDelete:
			return flip(term.KeyBackspace), true
		}
		switch ev.Ch {
		case 'f':
			return flipCh('b'), true
		case 'b':
			return flipCh('f'), true
		case 'd':
			return flip(term.KeyBackspace), true
		case '{':
			return flipCh('}'), true
		case '}':
			return flipCh('{'), true
		case 'a':
			return flipCh('e'), true
		case 'e':
			return flipCh('a'), true
		}
	case term.ModCtrlAlt:
		switch ev.Ch {
		case 'f':
			return flipCh('b'), true
		case 'b':
			return flipCh('f'), true
		}
	}
	return ev, false
}

// popMarkJump implements C-u C-SPC: jump to the newest mark and pop it.
func (h *emacsHandler) popMarkJump() bool {
	locs := h.markLocations()
	if len(locs) == 0 {
		return false
	}
	target := locs[len(locs)-1].From
	h.popMarkLocation()
	h.cursor.MoveToScroll(target)
	return true
}

// killLines implements C-k with an argument: kill through n following
// newlines when positive, back to the end of the nth previous line when
// negative, and back to the beginning of the line when zero.
func (h *emacsHandler) killLines(n int) bool {
	point := h.cursor.CursorAtScroll()
	view := h.cursor.View()
	rows := view.Rows()
	var target term.Coordinates
	prepend := false
	switch {
	case n > 0:
		if point.Y+n < rows {
			target = term.Coordinates{Y: point.Y + n}
		} else {
			target = term.Coordinates{Y: rows - 1, X: view.Columns(rows - 1)}
		}
	case n == 0:
		target = term.Coordinates{Y: point.Y}
		prepend = true
	default:
		if y := point.Y + n; y >= 0 {
			target = term.Coordinates{Y: y, X: view.Columns(y)}
		}
		prepend = true
	}
	if target == point {
		return false
	}
	if !h.cursor.SelectRange(point, target) {
		return false
	}
	return h.killSelection(prepend)
}

// yankArg implements C-y with an argument: a bare C-u yanks and leaves
// point before the inserted text; a numeric argument yanks the nth most
// recent kill and primes yank-pop from there.
func (h *emacsHandler) yankArg(count int, raw bool, pasted *bool) bool {
	if raw {
		start := h.cursor.CursorAtScroll()
		if !h.yank() {
			return false
		}
		h.cursor.MoveToScroll(start)
		*pasted = true
		return true
	}
	if count < 1 {
		return false
	}
	if count == 1 {
		if !h.yank() {
			return false
		}
		*pasted = true
		return true
	}
	history, ok := registerhistory.AsHistory(h.clipboard)
	if !ok {
		return false
	}
	data, ok := history.HistoryAt(count - 1)
	if !ok {
		return false
	}
	mode, _ := data.Metadata.(text.SelectMode)
	h.cursor.Paste(data.Text, mode, false)
	h.historyIdx = count - 1
	h.lastPaste = true
	*pasted = true
	return true
}

// scrollLines scrolls the view n lines (down when positive); the cursor is
// dragged along the view edge once it would leave the window, like GNU
// scroll-up/-down with an argument.
func (h *emacsHandler) scrollLines(n int) bool {
	if n == 0 {
		return true
	}
	down := n > 0
	if !down {
		n = -n
	}
	scroll := h.less.Scroll()
	moved := false
	for range n {
		var ok bool
		if down {
			ok = scroll.SeekDown()
		} else {
			ok = scroll.SeekUp()
		}
		if !ok {
			break
		}
		moved = true
	}
	return moved
}

// caseWordsBackward applies M-u / M-l / M-c with a negative argument: the
// previous n words change case and point stays where it is.
func (h *emacsHandler) caseWordsBackward(op rune, n int) bool {
	point := h.cursor.CursorAtScroll()
	start := point
	for range n {
		prev, ok := h.backwardWordFrom(start)
		if !ok {
			break
		}
		start = prev
	}
	if start == point {
		return false
	}
	if !h.cursor.SelectRange(start, point) {
		return false
	}
	switch op {
	case 'u':
		h.cursor.UppercaseSelection()
	case 'l':
		h.cursor.LowercaseSelection()
	case 'c':
		sel := h.cursor.Selection()
		if capitalized := capitalizeWords(sel); capitalized != sel {
			h.cursor.DeleteSelection()
			h.cursor.InsertString(capitalized)
		}
	}
	h.cursor.Unselect()
	h.cursor.MoveToScroll(point)
	return true
}
