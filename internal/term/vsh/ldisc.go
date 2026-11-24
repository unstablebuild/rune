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

package vsh

import (
	"context"
	"io"
	"unicode/utf8"
)

const (
	ctrlA = 0x01
	ctrlC = 0x03
	ctrlD = 0x04
	ctrlG = 0x07
	ctrlH = 0x08
	ctrlU = 0x15
	ctrlW = 0x17
	esc   = 0x1b
	del   = 0x7f
)

// lineDiscipline turns the bytes written to a pty master into program
// input. In raw mode, while a prompt is being edited, bytes queue up
// verbatim for the prompt reader. In cooked mode, while a command
// runs, it edits and echoes a line the way a kernel tty does and
// hands complete lines to the program. The pty's lock guards it.
type lineDiscipline struct {
	p *pty

	raw  bool
	rawQ []byte
	// skipLF drops the "\n" of a "\r\n" whose "\r" already ended a
	// raw chunk.
	skipLF bool

	line  []byte
	lines [][]byte
	eof   bool
	// lastCR folds "\r\n" into one line end.
	lastCR      bool
	utf8Pending []byte
	escPending  []byte

	// onInterrupt runs, with no lock held, after ^C is typed in
	// cooked mode.
	onInterrupt func()
}

// receiveLocked takes bytes typed at the terminal. It reports whether
// a ^C was typed in cooked mode.
func (l *lineDiscipline) receiveLocked(b []byte) (interrupt bool) {
	if l.raw {
		if l.skipLF && len(b) > 0 {
			l.skipLF = false
			if b[0] == '\n' {
				b = b[1:]
			}
		}
		l.rawQ = append(l.rawQ, b...)
		l.p.cond.Broadcast()
		return false
	}
	for _, c := range b {
		if l.cookedByteLocked(c) {
			interrupt = true
		}
	}
	return interrupt
}

func (l *lineDiscipline) cookedByteLocked(c byte) (interrupt bool) {
	// A control character is never part of an escape sequence, and a
	// byte that starts a rune is never part of the rune before it: each
	// ends what was pending and takes effect, so that an Enter or ^C
	// typed after a lone Esc or a truncated rune is not lost.
	if c < 0x20 || c == del {
		l.escPending = nil
	}
	if utf8.RuneStart(c) {
		l.utf8Pending = nil
	}
	if len(l.escPending) > 0 {
		l.escPending = append(l.escPending, c)
		switch {
		case len(l.escPending) == 2 && (c == '[' || c == 'O'):
		case l.escPending[1] == '[' && !(c >= 0x40 && c <= 0x7e):
		default:
			l.escPending = nil
		}
		return false
	}
	if len(l.utf8Pending) > 0 || c >= utf8.RuneSelf {
		l.utf8Pending = append(l.utf8Pending, c)
		if utf8.FullRune(l.utf8Pending) {
			if r, _ := utf8.DecodeRune(l.utf8Pending); r != utf8.RuneError {
				l.line = append(l.line, l.utf8Pending...)
				l.p.echoLocked(l.utf8Pending)
			}
			l.utf8Pending = nil
		} else if len(l.utf8Pending) >= utf8.UTFMax {
			l.utf8Pending = nil
		}
		return false
	}
	wasCR := l.lastCR
	l.lastCR = c == '\r'
	switch c {
	case '\r':
		l.completeLineLocked(true)
	case '\n':
		if !wasCR {
			l.completeLineLocked(true)
		}
	case del, ctrlH:
		if l.eraseRuneLocked() {
			l.p.echoLocked([]byte("\b \b"))
		}
	case ctrlU:
		for l.eraseRuneLocked() {
			l.p.echoLocked([]byte("\b \b"))
		}
	case ctrlW:
		for len(l.line) > 0 && l.line[len(l.line)-1] == ' ' {
			l.eraseRuneLocked()
			l.p.echoLocked([]byte("\b \b"))
		}
		for len(l.line) > 0 && l.line[len(l.line)-1] != ' ' {
			l.eraseRuneLocked()
			l.p.echoLocked([]byte("\b \b"))
		}
	case ctrlC:
		l.line = nil
		l.lines = nil
		l.eof = false
		l.p.echoLocked([]byte("^C"))
		l.p.cond.Broadcast()
		return true
	case ctrlD:
		if len(l.line) == 0 {
			l.eof = true
			l.p.cond.Broadcast()
		} else {
			l.completeLineLocked(false)
		}
	case ctrlG:
		l.p.echoLocked([]byte{ctrlG})
	case esc:
		l.escPending = append(l.escPending[:0], esc)
	default:
		if c < 0x20 {
			return false
		}
		l.line = append(l.line, c)
		l.p.echoLocked([]byte{c})
	}
	return false
}

func (l *lineDiscipline) completeLineLocked(newline bool) {
	line := l.line
	if newline {
		line = append(line, '\n')
		l.p.echoLocked([]byte("\r\n"))
	}
	l.lines = append(l.lines, line)
	l.line = nil
	l.p.cond.Broadcast()
}

func (l *lineDiscipline) eraseRuneLocked() bool {
	if len(l.line) == 0 {
		return false
	}
	_, size := utf8.DecodeLastRune(l.line)
	l.line = l.line[:len(l.line)-size]
	return true
}

// read is a cooked read: it copies the next complete line, or what
// remains of one longer than b, and returns io.EOF for a ^D typed on
// an empty line or a hangup. ctx aborts a blocked read.
func (l *lineDiscipline) read(ctx context.Context, b []byte) (int, error) {
	p := l.p
	p.mu.Lock()
	defer p.mu.Unlock()
	var stop func() bool
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	for {
		if len(l.lines) > 0 {
			n := copy(b, l.lines[0])
			if n == len(l.lines[0]) {
				l.lines = l.lines[1:]
			} else {
				l.lines[0] = l.lines[0][n:]
			}
			return n, nil
		}
		if l.eof {
			l.eof = false
			return 0, io.EOF
		}
		if p.hungUp {
			return 0, io.EOF
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if stop == nil {
			stop = p.wakeOnDone(ctx)
		}
		p.cond.Wait()
	}
}

// setRawLocked switches to raw mode for prompt editing. Input that a
// program never consumed is replayed to the prompt: handback, the
// lines still queued (each ending in "\r"), the partial line and a
// pending ^D, in that order.
func (l *lineDiscipline) setRawLocked(handback []byte) {
	q := make([]byte, 0, len(handback)+len(l.line)+len(l.rawQ))
	q = append(q, handback...)
	for _, line := range l.lines {
		q = append(q, line...)
	}
	q = append(q, l.line...)
	if l.eof {
		q = append(q, ctrlD)
	}
	for i, c := range q {
		if c == '\n' {
			q[i] = '\r'
		}
	}
	l.rawQ = append(q, l.rawQ...)
	l.lines = nil
	l.line = nil
	l.eof = false
	l.lastCR = false
	l.utf8Pending = nil
	l.escPending = nil
	l.raw = true
	l.p.cond.Broadcast()
}

// setCookedLocked switches to cooked mode for a running program. The
// typeahead left in the raw queue is fed through the cooked rules; it
// reports whether that typeahead held a ^C.
func (l *lineDiscipline) setCookedLocked() (interrupt bool) {
	q := l.rawQ
	l.rawQ = nil
	l.skipLF = false
	l.raw = false
	for _, c := range q {
		if l.cookedByteLocked(c) {
			interrupt = true
		}
	}
	return interrupt
}

// setRaw and setCooked are the locking forms of setRawLocked and
// setCookedLocked.
func (l *lineDiscipline) setRaw(handback []byte) {
	l.p.mu.Lock()
	l.setRawLocked(handback)
	l.p.mu.Unlock()
}

func (l *lineDiscipline) setCooked() (interrupt bool) {
	l.p.mu.Lock()
	defer l.p.mu.Unlock()
	return l.setCookedLocked()
}

// setInterruptHandler installs the cooked-mode ^C callback.
func (l *lineDiscipline) setInterruptHandler(fn func()) {
	l.p.mu.Lock()
	l.onInterrupt = fn
	l.p.mu.Unlock()
}
