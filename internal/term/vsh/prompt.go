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
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// errInterrupt reports a ^C typed at the prompt.
var errInterrupt = errors.New("vsh: interrupted")

// historySize bounds the lines a session remembers.
const historySize = 500

// oscMark is the OSC 133 shell-integration mark for x: "A" at prompt
// start, "B" at prompt end, "C" at command start and "D;<status>" at
// command end.
func oscMark(x string) []byte {
	return []byte("\x1b]133;" + x + "\x1b\\")
}

// promptReader feeds the raw input queue of a pty to an x/term
// Terminal one chunk at a time, in the shape that keeps the editor's
// model exact: a chunk ends before any ^G, ^C or "\r", a "\r" is a
// chunk of its own and a Tab ends one, so the editor never holds bytes
// past a finished line or a completion and a fresh Terminal per prompt
// loses nothing. It answers ^G itself with BEL after the editor flushed
// the redraw caused by the keys before it, which is the order the
// terminal's modal mode handshake needs.
type promptReader struct {
	s *session
	// t is the Terminal reading, for resizes.
	t *term.Terminal
	// out rewrites what the editor draws.
	out promptOutput
	// markedPromptEnd records that the prompt-end mark was written,
	// which happens once the editor drew the prompt and reads.
	markedPromptEnd bool
	// prefill is read before the terminal's input: the line a listing
	// of completions interrupted, typed again.
	prefill []byte
	// listing holds the completions a Tab listed, and listingPos the
	// cursor then. The editor reads an Enter next so that its line
	// ends, unremembered, for the listing to go below it.
	listing    []string
	listingPos int
}

func (r *promptReader) Write(b []byte) (int, error) {
	lineLen := -1
	if n, ok := editorLineLength(r.t); ok {
		lineLen = n
	}
	if _, err := r.s.p.writeOutput(r.s.proc.ctx, r.out.rewrite(b, lineLen)); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (r *promptReader) Read(b []byte) (int, error) {
	if !r.markedPromptEnd {
		r.markedPromptEnd = true
		if _, err := r.s.p.writeOutput(r.s.proc.ctx, oscMark("B")); err != nil {
			return 0, io.EOF
		}
	}
	if r.listing != nil {
		b[0] = '\r'
		return 1, nil
	}
	if len(r.prefill) > 0 {
		n := copy(b, r.prefill)
		r.prefill = r.prefill[n:]
		return n, nil
	}
	p := r.s.p
	p.mu.Lock()
	defer p.mu.Unlock()
	stop := p.wakeOnDone(r.s.proc.ctx)
	defer stop()
	for {
		if p.hungUp || r.s.proc.ctx.Err() != nil {
			return 0, io.EOF
		}
		if p.winch {
			p.winch = false
			size := p.size
			// The editor releases its lock around Read, which makes
			// this the one safe place to resize it from.
			p.mu.Unlock()
			r.out.resize(size.Columns)
			_ = r.t.SetSize(size.Columns, size.Rows)
			p.mu.Lock()
			continue
		}
		q := p.ld.rawQ
		if len(q) == 0 {
			p.cond.Wait()
			continue
		}
		switch q[0] {
		case ctrlG:
			p.ld.rawQ = q[1:]
			p.echoLocked([]byte{ctrlG})
			continue
		case ctrlC:
			p.ld.rawQ = q[1:]
			return 0, errInterrupt
		}
		n := 0
		for n < len(q) && n < len(b) {
			c := q[n]
			if c == ctrlG || c == ctrlC {
				break
			}
			// Enter goes on its own: the editor resets its line
			// before flushing what the keys before it drew, and the
			// rewrite needs that line to tell content from erasure.
			if (c == '\r' || c == '\n') && n > 0 {
				break
			}
			n++
			// A Tab ends its chunk so that the editor reads the Enter
			// that may follow a listing before any key typed after.
			if c == '\r' || c == '\n' || c == '\t' {
				break
			}
		}
		chunk, rest := q[:n], q[n:]
		if last := chunk[n-1]; last == '\r' || last == '\n' {
			chunk[n-1] = '\r'
			switch {
			case last == '\n':
			case len(rest) > 0:
				if rest[0] == '\n' {
					rest = rest[1:]
				}
			default:
				p.ld.skipLF = true
			}
		}
		if len(rest) == 0 {
			rest = nil
		}
		p.ld.rawQ = rest
		for i := 0; i+1 < n; i++ {
			if chunk[i] == esc && chunk[i+1] == 'O' {
				chunk[i+1] = '['
			}
		}
		copy(b, chunk)
		return n, nil
	}
}

// promptOutput rewrites what the x/term editor draws into what a
// readline shell draws on an xterm, which is what the terminal's modal
// mode was built against. The editor erases by overwriting with
// spaces, which the terminal would count as content at the end of the
// prompt line; readline clears to the end of the line instead. The
// editor also ends a row it filled with an explicit "\r\n", which the
// terminal takes as a hard line break, and as a blank line when Enter
// follows; readline lets the terminal's auto-margin wrap the row. The
// rewrite tracks the cursor cell by cell: a space drawn past the end of
// the prompt and line is an erasure, one within them is content.
type promptOutput struct {
	width int
	// promptWidth is the visual length of the prompt, which precedes
	// the line on the first row.
	promptWidth int
	// row and col locate the physical cursor relative to the first
	// row of the prompt; col == width means the cursor sits on the
	// last column with a wrap pending.
	row, col int
	// suppressedWrap records an explicit wrap of the editor that was
	// left to the auto-margin: a following character wraps by itself,
	// Enter needs one newline, and a cursor move needs the row change
	// made first.
	suppressedWrap bool
}

func (o *promptOutput) resize(width int) {
	o.width = max(width, 1)
	o.col = min(o.col, o.width)
}

// cell is the cursor's position in cells from the start of the prompt.
func (o *promptOutput) cell() int {
	return o.row*o.width + o.col
}

// rewrite returns what to draw for b, the editor's output while its
// line holds lineLen characters; a negative lineLen leaves erasures
// drawn as the editor drew them.
func (o *promptOutput) rewrite(b []byte, lineLen int) []byte {
	if o.width == 0 {
		o.width = 80
	}
	extent := -1
	if lineLen >= 0 {
		extent = o.promptWidth + lineLen
	}
	out := make([]byte, 0, len(b)+16)
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == esc:
			seq := escapeSequence(b[i:])
			out = o.flushWrap(out)
			out = append(out, seq...)
			o.trackCSI(seq)
			i += len(seq)
		case c == '\r' && i+1 < len(b) && b[i+1] == '\n':
			switch {
			case o.suppressedWrap:
				o.suppressedWrap = false
				out = append(out, '\r', '\n')
				o.row++
				o.col = 0
			case o.col == o.width:
				o.suppressedWrap = true
			default:
				out = append(out, '\r', '\n')
				o.row++
				o.col = 0
			}
			i += 2
		case c == ' ' && extent >= 0 && o.cell() >= extent:
			n := 1
			for i+n < len(b) && b[i+n] == ' ' {
				n++
			}
			out = o.erase(out, n, b[i+n:])
			if left, ok := cursorLeft(b[i+n:]); ok {
				i += len(left)
			}
			i += n
		case c == '\r':
			out = append(out, c)
			o.col = 0
			o.suppressedWrap = false
			i++
		case c < 0x20 || c == del:
			out = append(out, c)
			i++
		default:
			_, size := utf8.DecodeRune(b[i:])
			out = o.printable(out, b[i:i+size])
			i += size
		}
	}
	return out
}

// erase replaces n erasing spaces with a clear to the end of the line,
// keeping the cursor where the editor's following output expects it:
// a move back is shortened by the spaces not drawn, any other escape
// is preceded by a move over them, and a wrap needs nothing since the
// newline that follows resets the column.
func (o *promptOutput) erase(out []byte, n int, rest []byte) []byte {
	out = o.flushWrap(out)
	out = append(out, esc, '[', 'K')
	// The editor writes an erasure as one run of spaces that may cross
	// into the rows of a wrapped line below, ending with a newline when
	// it fills its last row. Those rows are cleared too, and the cursor
	// put where the editor's run leaves it, which the move that follows
	// is relative to.
	end := o.cell() + n
	endRow, endCol := end/o.width, end%o.width
	crlf := len(rest) >= 2 && rest[0] == '\r' && rest[1] == '\n'
	if crlf {
		endRow--
	}
	if endRow > o.row {
		for o.row < endRow {
			out = append(out, '\r', '\n', esc, '[', 'K')
			o.row++
			o.col = 0
		}
		if crlf {
			return out
		}
		if endCol > 0 {
			out = appendCSI(out, endCol, 'C')
		}
		o.col = endCol
		return out
	}
	if left, ok := cursorLeft(rest); ok {
		switch steps := csiCount(left) - n; {
		case steps > 0:
			out = appendCSI(out, steps, 'D')
			o.col = max(min(o.col, o.width-1)-steps, 0)
		case steps < 0:
			out = appendCSI(out, -steps, 'C')
			o.col = min(o.col-steps, o.width-1)
		}
		return out
	}
	if crlf {
		return out
	}
	out = appendCSI(out, n, 'C')
	o.col = min(o.col+n, o.width-1)
	return out
}

// printable appends one character, which the auto-margin wraps when
// the row is full.
func (o *promptOutput) printable(out []byte, c []byte) []byte {
	out = append(out, c...)
	if o.suppressedWrap || o.col >= o.width {
		o.suppressedWrap = false
		o.row++
		o.col = 1
	} else {
		o.col++
	}
	return out
}

// flushWrap makes the row change the editor assumed before anything
// that is not a character, which would otherwise act on the wrong row.
// A character needs nothing: the auto-margin wraps it.
func (o *promptOutput) flushWrap(out []byte) []byte {
	if !o.suppressedWrap {
		return out
	}
	o.suppressedWrap = false
	o.row++
	o.col = 0
	return append(out, '\r', '\n')
}

func (o *promptOutput) trackCSI(seq []byte) {
	if len(seq) < 3 || seq[1] != '[' {
		return
	}
	switch seq[len(seq)-1] {
	case 'D':
		o.col = max(min(o.col, o.width-1)-csiCount(seq), 0)
	case 'C':
		o.col = min(o.col+csiCount(seq), o.width-1)
	case 'A':
		o.row = max(o.row-csiCount(seq), 0)
	case 'B':
		o.row += csiCount(seq)
	case 'H', 'f':
		o.row, o.col = 0, 0
	case 'G':
		o.col = 0
	}
}

// escapeSequence returns the escape sequence at the start of b: a CSI
// up to its final byte, an OSC up to its terminator, or ESC and one
// byte.
func escapeSequence(b []byte) []byte {
	if len(b) < 2 {
		return b
	}
	switch b[1] {
	case '[':
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return b[:i+1]
			}
		}
		return b
	case ']':
		for i := 2; i < len(b); i++ {
			if b[i] == ctrlG {
				return b[:i+1]
			}
			if b[i] == esc && i+1 < len(b) && b[i+1] == '\\' {
				return b[:i+2]
			}
		}
		return b
	}
	return b[:2]
}

// cursorLeft returns the cursor-left sequence at the start of b.
func cursorLeft(b []byte) ([]byte, bool) {
	if len(b) < 3 || b[0] != esc || b[1] != '[' {
		return nil, false
	}
	seq := escapeSequence(b)
	if seq[len(seq)-1] != 'D' {
		return nil, false
	}
	return seq, true
}

// csiCount is the numeric parameter of a one-parameter CSI, 1 when
// omitted.
func csiCount(seq []byte) int {
	n, err := strconv.Atoi(string(seq[2 : len(seq)-1]))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func appendCSI(out []byte, n int, final byte) []byte {
	out = append(out, esc, '[')
	if n > 1 {
		out = strconv.AppendInt(out, int64(n), 10)
	}
	return append(out, final)
}

// visualLength counts the cells a prompt occupies, skipping escape
// sequences the way the editor does.
func visualLength(s string) int {
	n := 0
	inEscape := false
	for _, r := range s {
		switch {
		case inEscape:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
		case r == esc:
			inEscape = true
		default:
			n++
		}
	}
	return n
}

// editorLineLength reads how many characters the editor's line holds.
// x/term does not expose it, and it is what tells an erasing space
// from a content one in the editor's output; the field has been part
// of Terminal since the package began. It reports false should the
// field go away, in which case erased cells stay drawn as spaces.
func editorLineLength(t *term.Terminal) (int, bool) {
	if t == nil {
		return 0, false
	}
	line := reflect.ValueOf(t).Elem().FieldByName("line")
	if !line.IsValid() || line.Kind() != reflect.Slice {
		return 0, false
	}
	return line.Len(), true
}

// history is a bounded, goroutine-safe term.History shared by every
// prompt of a session.
type history struct {
	mu      sync.Mutex
	entries []string
}

func (h *history) Add(entry string) {
	if strings.TrimSpace(entry) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.entries); n > 0 && h.entries[n-1] == entry {
		return
	}
	h.entries = append(h.entries, entry)
	if len(h.entries) > historySize {
		h.entries = h.entries[len(h.entries)-historySize:]
	}
}

func (h *history) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

func (h *history) At(idx int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.entries[len(h.entries)-1-idx]
}

// readLine edits one line at the prompt. It returns errInterrupt on
// ^C and io.EOF on ^D at an empty line, on hangup and once the process
// ends. A Tab listing completions writes them below the line and
// prompts again with the line as it was.
func (s *session) readLine(prompt string) (string, error) {
	var prefill []byte
	for {
		cols, rows := s.p.windowSize()
		r := &promptReader{s: s, out: promptOutput{promptWidth: visualLength(prompt)}, prefill: prefill}
		r.out.resize(cols)
		t := term.NewTerminal(r, prompt)
		t.History = promptHistory{history: s.history, r: r}
		_ = t.SetSize(cols, rows)
		r.t = t
		if s.complete != nil {
			t.AutoCompleteCallback = r.autoComplete
		}
		line, err := t.ReadLine()
		// A line pasted with bracketed paste is a line like any other;
		// the editor flags it for callers that treat pastes specially.
		if errors.Is(err, term.ErrPasteIndicator) {
			err = nil
		}
		if r.listing == nil || err != nil {
			return line, err
		}
		cols, _ = s.p.windowSize()
		s.write(formatList(r.listing, cols))
		s.write(oscMark("A"))
		prefill = retype(line, r.listingPos)
	}
}

// autoComplete is the editor's completion callback: Tab completes the
// line, or lists the candidates.
func (r *promptReader) autoComplete(line string, pos int, key rune) (string, int, bool) {
	if key != '\t' {
		return "", 0, false
	}
	newLine, newPos, listing := r.s.complete(line, pos)
	if listing != nil {
		r.listing, r.listingPos = listing, pos
		return "", 0, false
	}
	return newLine, newPos, newLine != line || newPos != pos
}

// promptHistory is the session's history as one prompt's editor sees
// it: the line it ends to list completions was not entered.
type promptHistory struct {
	*history
	r *promptReader
}

func (h promptHistory) Add(entry string) {
	if h.r.listing == nil {
		h.history.Add(entry)
	}
}

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
	cursorBack = "\x1b[D"
)

// retype is the input that types line again with the cursor at byte
// pos: a bracketed paste, which the editor takes as text whatever the
// characters, then a move back over what follows the cursor.
func retype(line string, pos int) []byte {
	if line == "" {
		return nil
	}
	b := []byte(pasteStart + line + pasteEnd)
	for range utf8.RuneCountInString(line[pos:]) {
		b = append(b, cursorBack...)
	}
	return b
}

// promptString is PS1 expanded against the shell's variables and
// status, the "$?" of the last line, or "<scheme>://<dir> $ " without
// one. A continuation line gets "> ".
func (s *session) promptString(continuation bool, status int) string {
	if continuation {
		return "> "
	}
	ps1, ok := s.variable("PS1")
	if !ok {
		return s.e.cfg.Scheme + "://" + s.runner.Dir + " $ "
	}
	word, err := syntax.NewParser().Document(strings.NewReader(ps1))
	if err != nil {
		return ps1
	}
	expanded, err := expand.Literal(&expand.Config{Env: expand.FuncEnviron(func(name string) string {
		if name == "?" {
			return strconv.Itoa(status)
		}
		v, _ := s.variable(name)
		return v
	})}, word)
	if err != nil {
		return ps1
	}
	return expanded
}

// variable reads a shell variable: what the last command left, else
// the environment the shell started with.
func (s *session) variable(name string) (string, bool) {
	if v, ok := s.runner.Vars[name]; ok {
		return v.String(), v.IsSet()
	}
	if v := s.runner.Env.Get(name); v.IsSet() {
		return v.String(), true
	}
	return "", false
}
