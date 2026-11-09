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
	"strings"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// The word commands below follow VS Code's WordOperations with its default
// editor.wordSeparators, so the keymap's word motions and deletions land where
// VS Code's cursorWordStartLeft, cursorWordEndRight, deleteWordLeft and
// deleteWordRight do.
const wordSeparators = "`~!@#$%^&*()-=+[{]}\\|;:'\",.<>/?"

type wordClass int

const (
	classRegular wordClass = iota
	classWhitespace
	classSeparator
)

func classOf(r rune) wordClass {
	switch {
	case r == ' ' || r == '\t':
		return classWhitespace
	case strings.ContainsRune(wordSeparators, r):
		return classSeparator
	}
	return classRegular
}

type wordKind int

const (
	kindNone wordKind = iota
	kindRegular
	kindSeparator
)

// word is a run of regular or separator characters on one line, [start, end).
type word struct {
	start, end int
	kind       wordKind
	next       wordClass
}

// endsWord reports whether a character of class c ends a word of kind k.
func endsWord(k wordKind, c wordClass) bool {
	return c == classWhitespace ||
		(k == kindRegular && c == classSeparator) ||
		(k == kindSeparator && c == classRegular)
}

func kindOf(c wordClass) wordKind {
	if c == classSeparator {
		return kindSeparator
	}
	return kindRegular
}

func wordEnd(line []rune, k wordKind, from int) int {
	for i := from; i < len(line); i++ {
		if endsWord(k, classOf(line[i])) {
			return i
		}
	}
	return len(line)
}

func wordStart(line []rune, k wordKind, from int) int {
	for i := from; i >= 0; i-- {
		if endsWord(k, classOf(line[i])) {
			return i + 1
		}
	}
	return 0
}

// prevWord is the last word starting before x on line.
func prevWord(line []rune, x int) (word, bool) {
	k := kindNone
	for i := x - 1; i >= 0; i-- {
		c := classOf(line[i])
		if k != kindNone && endsWord(k, c) {
			return word{start: i + 1, end: wordEnd(line, k, i+1), kind: k, next: c}, true
		}
		if c != classWhitespace {
			k = kindOf(c)
		}
	}
	if k == kindNone {
		return word{}, false
	}
	return word{end: wordEnd(line, k, 0), kind: k, next: classWhitespace}, true
}

// nextWord is the first word ending after x on line.
func nextWord(line []rune, x int) (word, bool) {
	k := kindNone
	for i := x; i < len(line); i++ {
		c := classOf(line[i])
		if k != kindNone && endsWord(k, c) {
			return word{start: wordStart(line, k, i-1), end: i, kind: k, next: c}, true
		}
		if c != classWhitespace {
			k = kindOf(c)
		}
	}
	if k == kindNone {
		return word{}, false
	}
	return word{start: wordStart(line, k, len(line)-1), end: len(line), kind: k, next: classWhitespace}, true
}

// wordDoc is the document a word command walks. Positions handed to its
// methods must lie within it.
type wordDoc struct {
	rows int
	line func(y int) []rune
}

// moveWordLeft is cursorWordStartLeft: the start of the previous word, moving
// to the end of the line above first when at a line start.
func (d wordDoc) moveWordLeft(at term.Coordinates) term.Coordinates {
	if at.X == 0 && at.Y > 0 {
		at.Y--
		at.X = len(d.line(at.Y))
	}
	w, _ := prevWord(d.line(at.Y), at.X)
	return term.Coordinates{X: w.start, Y: at.Y}
}

// moveWordRight is cursorWordEndRight: the end of the next word, moving to the
// start of the line below first when at a line end. A lone separator glued to
// the word after it is skipped, so "a.b" moves past ".b" in one step.
func (d wordDoc) moveWordRight(at term.Coordinates) term.Coordinates {
	line := d.line(at.Y)
	if at.X == len(line) && at.Y < d.rows-1 {
		at.Y++
		at.X = 0
		line = d.line(at.Y)
	}
	w, ok := nextWord(line, at.X)
	if ok && w.kind == kindSeparator && w.end-w.start == 1 && w.next == classRegular {
		w, ok = nextWord(line, w.end)
	}
	if !ok {
		return term.Coordinates{X: len(line), Y: at.Y}
	}
	return term.Coordinates{X: w.end, Y: at.Y}
}

// deleteWordLeft is the start of the range deleteWordLeft removes up to at. A
// run of two or more blanks before at is removed on its own; at a line start
// the line break before it goes. It reports false at the document start.
func (d wordDoc) deleteWordLeft(at term.Coordinates) (term.Coordinates, bool) {
	if at == (term.Coordinates{}) {
		return at, false
	}
	line := d.line(at.Y)
	blank := at.X
	for blank > 0 && classOf(line[blank-1]) == classWhitespace {
		blank--
	}
	if at.X-blank >= 2 {
		return term.Coordinates{X: blank, Y: at.Y}, true
	}
	if w, ok := prevWord(line, at.X); ok {
		return term.Coordinates{X: w.start, Y: at.Y}, true
	}
	if at.X > 0 {
		return term.Coordinates{Y: at.Y}, true
	}
	return term.Coordinates{X: len(d.line(at.Y - 1)), Y: at.Y - 1}, true
}

// deleteWordRight is the end of the range deleteWordRight removes from at.
// Blanks after at are removed on their own; at a line end the line break goes
// together with the blanks before the first word below. It reports false at
// the document end.
func (d wordDoc) deleteWordRight(at term.Coordinates) (term.Coordinates, bool) {
	line := d.line(at.Y)
	last := at.Y == d.rows-1
	if last && at.X == len(line) {
		return at, false
	}
	blank := at.X
	for blank < len(line) && classOf(line[blank]) == classWhitespace {
		blank++
	}
	if blank > at.X {
		return term.Coordinates{X: blank, Y: at.Y}, true
	}
	if w, ok := nextWord(line, at.X); ok {
		return term.Coordinates{X: w.end, Y: at.Y}, true
	}
	if at.X < len(line) || last {
		return term.Coordinates{X: len(line), Y: at.Y}, true
	}
	below := d.line(at.Y + 1)
	if w, ok := nextWord(below, 0); ok {
		return term.Coordinates{X: w.start, Y: at.Y + 1}, true
	}
	return term.Coordinates{X: len(below), Y: at.Y + 1}, true
}
