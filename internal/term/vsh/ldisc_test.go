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
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/debug"
)

// drainMaster returns the output currently queued for the master.
func drainMaster(p *pty) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := string(p.out)
	p.out = nil
	return out
}

// cookedLines returns the complete lines queued for a program.
func cookedLines(p *pty) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ret []string
	for _, l := range p.ld.lines {
		ret = append(ret, string(l))
	}
	return ret
}

// ldiscState is what the line discipline holds after some input.
type ldiscState struct {
	echo      string
	lines     []string
	partial   string
	eof       bool
	interrupt bool
}

// receive types each write at the terminal in turn and reports the
// resulting state, draining the echo.
func receive(p *pty, writes ...string) ldiscState {
	var st ldiscState
	for _, w := range writes {
		p.mu.Lock()
		if p.ld.receiveLocked([]byte(w)) {
			st.interrupt = true
		}
		p.mu.Unlock()
	}
	st.echo = drainMaster(p)
	st.lines = cookedLines(p)
	p.mu.Lock()
	defer p.mu.Unlock()
	st.partial = string(p.ld.line)
	st.eof = p.ld.eof
	return st
}

func TestLdiscCooked(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 10000)
	tests := []struct {
		name   string
		writes []string
		want   ldiscState
	}{
		{name: "printable is appended and echoed", writes: []string{"ab"}, want: ldiscState{echo: "ab", partial: "ab"}},
		{name: "CR completes the line", writes: []string{"ls\r"}, want: ldiscState{echo: "ls\r\n", lines: []string{"ls\n"}}},
		{name: "LF completes the line", writes: []string{"ls\n"}, want: ldiscState{echo: "ls\r\n", lines: []string{"ls\n"}}},
		{name: "CRLF counts once", writes: []string{"ls\r\n"}, want: ldiscState{echo: "ls\r\n", lines: []string{"ls\n"}}},
		{name: "CRLF split across writes counts once", writes: []string{"ls\r", "\n"}, want: ldiscState{echo: "ls\r\n", lines: []string{"ls\n"}}},
		{name: "CR CR is two lines", writes: []string{"a\r\r"}, want: ldiscState{echo: "a\r\n\r\n", lines: []string{"a\n", "\n"}}},
		{name: "LF LF is two lines", writes: []string{"a\n\n"}, want: ldiscState{echo: "a\r\n\r\n", lines: []string{"a\n", "\n"}}},
		{name: "CR LF LF is two lines", writes: []string{"\r\n\n"}, want: ldiscState{echo: "\r\n\r\n", lines: []string{"\n", "\n"}}},
		{name: "LF CR is two lines", writes: []string{"\n\r"}, want: ldiscState{echo: "\r\n\r\n", lines: []string{"\n", "\n"}}},
		{name: "an empty write changes nothing", writes: []string{""}, want: ldiscState{}},
		{name: "a very long line is kept whole", writes: []string{long + "\r"}, want: ldiscState{echo: long + "\r\n", lines: []string{long + "\n"}}},

		{name: "backspace erases a rune", writes: []string{"ab\x7f"}, want: ldiscState{echo: "ab\b \b", partial: "a"}},
		{name: "ctrl-h erases a rune", writes: []string{"ab\x08"}, want: ldiscState{echo: "ab\b \b", partial: "a"}},
		{name: "backspace on empty line is silent", writes: []string{"\x7f"}, want: ldiscState{}},
		{name: "backspace does not cross a completed line", writes: []string{"a\r\x7f\x7f"}, want: ldiscState{echo: "a\r\n", lines: []string{"a\n"}}},
		{name: "backspace erases a two-byte rune", writes: []string{"é\x7f"}, want: ldiscState{echo: "é\b \b"}},
		{name: "backspace erases a three-byte rune", writes: []string{"a日\x7f"}, want: ldiscState{echo: "a日\b \b", partial: "a"}},
		{name: "backspace erases a four-byte rune", writes: []string{"a😀\x7f"}, want: ldiscState{echo: "a😀\b \b", partial: "a"}},
		{name: "backspace erases a combining mark on its own", writes: []string{"e\u0301\x7f"}, want: ldiscState{echo: "e\u0301\b \b", partial: "e"}},
		{name: "more backspaces than runes stop at the line start", writes: []string{"ab\x7f\x7f\x7f"}, want: ldiscState{echo: "ab\b \b\b \b"}},

		{name: "ctrl-u erases the line", writes: []string{"abc\x15"}, want: ldiscState{echo: "abc\b \b\b \b\b \b"}},
		{name: "ctrl-u erases runes, not bytes", writes: []string{"日本\x15"}, want: ldiscState{echo: "日本\b \b\b \b"}},
		{name: "ctrl-u on empty line is silent", writes: []string{"\x15"}, want: ldiscState{}},
		{name: "ctrl-u keeps completed lines", writes: []string{"a\rbc\x15"}, want: ldiscState{echo: "a\r\nbc\b \b\b \b", lines: []string{"a\n"}}},

		{name: "ctrl-w erases the last word", writes: []string{"ab cd \x17"}, want: ldiscState{echo: "ab cd \b \b\b \b\b \b", partial: "ab "}},
		{name: "ctrl-w erases a word without trailing space", writes: []string{"ab cd\x17"}, want: ldiscState{echo: "ab cd\b \b\b \b", partial: "ab "}},
		{name: "ctrl-w erases a multibyte word by runes", writes: []string{"a wört\x17"}, want: ldiscState{echo: "a wört\b \b\b \b\b \b\b \b", partial: "a "}},
		{name: "ctrl-w on only spaces erases them", writes: []string{"   \x17"}, want: ldiscState{echo: "   \b \b\b \b\b \b"}},
		{name: "ctrl-w on empty line is silent", writes: []string{"\x17"}, want: ldiscState{}},
		{name: "ctrl-w twice erases two words", writes: []string{"a b c\x17\x17"}, want: ldiscState{echo: "a b c\b \b\b \b\b \b", partial: "a "}},

		{name: "ctrl-c flushes, echoes and interrupts", writes: []string{"ab\rcd\x03"}, want: ldiscState{echo: "ab\r\ncd^C", interrupt: true}},
		{name: "ctrl-c on empty line echoes and interrupts", writes: []string{"\x03"}, want: ldiscState{echo: "^C", interrupt: true}},
		{name: "ctrl-c discards a pending EOF", writes: []string{"\x04\x03"}, want: ldiscState{echo: "^C", interrupt: true}},
		{name: "input after ctrl-c is kept", writes: []string{"ab\x03cd\r"}, want: ldiscState{echo: "ab^Ccd\r\n", lines: []string{"cd\n"}, interrupt: true}},

		{name: "ctrl-d on empty line queues EOF", writes: []string{"\x04"}, want: ldiscState{eof: true}},
		{name: "ctrl-d completes a partial line without newline", writes: []string{"ab\x04"}, want: ldiscState{echo: "ab", lines: []string{"ab"}}},
		{name: "ctrl-d twice after text completes then queues EOF", writes: []string{"ab\x04\x04"}, want: ldiscState{echo: "ab", lines: []string{"ab"}, eof: true}},
		{name: "ctrl-d after a completed line queues EOF", writes: []string{"ab\r\x04"}, want: ldiscState{echo: "ab\r\n", lines: []string{"ab\n"}, eof: true}},
		{name: "input after ctrl-d starts a new line", writes: []string{"ab\x04cd\r"}, want: ldiscState{echo: "abcd\r\n", lines: []string{"ab", "cd\n"}}},

		{name: "ctrl-g rings the bell", writes: []string{"\x07"}, want: ldiscState{echo: "\x07"}},
		{name: "CSI sequences are dropped", writes: []string{"a\x1b[Ab\x1b[3~c"}, want: ldiscState{echo: "abc", partial: "abc"}},
		{name: "CSI with parameters is dropped", writes: []string{"a\x1b[1;5Cb"}, want: ldiscState{echo: "ab", partial: "ab"}},
		{name: "SS3 sequences are dropped", writes: []string{"a\x1bOAb"}, want: ldiscState{echo: "ab", partial: "ab"}},
		{name: "lone escape followed by a key drops the key", writes: []string{"a\x1bxb"}, want: ldiscState{echo: "ab", partial: "ab"}},
		{name: "escape split across writes is dropped", writes: []string{"a\x1b", "[", "1;5", "Cb"}, want: ldiscState{echo: "ab", partial: "ab"}},
		{name: "Enter after a lone escape completes the line", writes: []string{"a\x1b\r"}, want: ldiscState{echo: "a\r\n", lines: []string{"a\n"}}},
		{name: "ctrl-c after a lone escape interrupts", writes: []string{"a\x1b\x03"}, want: ldiscState{echo: "a^C", interrupt: true}},
		{name: "Enter inside an unfinished CSI completes the line", writes: []string{"a\x1b[1\r"}, want: ldiscState{echo: "a\r\n", lines: []string{"a\n"}}},
		{name: "backspace inside an unfinished CSI erases", writes: []string{"ab\x1b[\x7f"}, want: ldiscState{echo: "ab\b \b", partial: "a"}},
		{name: "an escape inside an escape starts a new one", writes: []string{"a\x1b\x1b[Ab"}, want: ldiscState{echo: "ab", partial: "ab"}},

		{name: "utf-8 is kept whole", writes: []string{"héllo"}, want: ldiscState{echo: "héllo", partial: "héllo"}},
		{name: "utf-8 split across writes is kept whole", writes: []string{"\xe6", "\x97", "\xa5"}, want: ldiscState{echo: "日", partial: "日"}},
		{name: "invalid utf-8 is dropped", writes: []string{"a\xffb"}, want: ldiscState{echo: "ab", partial: "ab"}},
		{name: "a stray continuation byte is dropped", writes: []string{"a\x80b"}, want: ldiscState{echo: "ab", partial: "ab"}},
		{name: "a truncated rune does not swallow the next character", writes: []string{"a\xc3b"}, want: ldiscState{echo: "ab", partial: "ab"}},
		{name: "a truncated rune does not swallow Enter", writes: []string{"a\xe6\x97\r"}, want: ldiscState{echo: "a\r\n", lines: []string{"a\n"}}},
		{name: "a truncated rune does not swallow ctrl-c", writes: []string{"a\xc3\x03"}, want: ldiscState{echo: "a^C", interrupt: true}},
		{name: "a truncated rune followed by a rune keeps the rune", writes: []string{"\xc3é"}, want: ldiscState{echo: "é", partial: "é"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, receive(newPty(1, 2), tc.writes...))
		})
	}
}

func TestLdiscControlCharacters(t *testing.T) {
	t.Parallel()
	// Each control character is typed between "a" and "b"; those not
	// listed are dropped in cooked mode.
	special := map[byte]ldiscState{
		ctrlC: {echo: "a^Cb", partial: "b", interrupt: true},
		ctrlD: {echo: "ab", lines: []string{"a"}, partial: "b"},
		ctrlG: {echo: "a\x07b", partial: "ab"},
		ctrlH: {echo: "a\b \bb", partial: "b"},
		'\n':  {echo: "a\r\nb", lines: []string{"a\n"}, partial: "b"},
		'\r':  {echo: "a\r\nb", lines: []string{"a\n"}, partial: "b"},
		ctrlU: {echo: "a\b \bb", partial: "b"},
		ctrlW: {echo: "a\b \bb", partial: "b"},
		esc:   {echo: "a", partial: "a"},
		del:   {echo: "a\b \bb", partial: "b"},
	}
	type row struct {
		c      byte
		cooked ldiscState
	}
	var rows []row
	for c := byte(0); c < 0x20; c++ {
		want, ok := special[c]
		if !ok {
			want = ldiscState{echo: "ab", partial: "ab"}
		}
		rows = append(rows, row{c, want})
	}
	rows = append(rows, row{del, special[del]})

	for _, r := range rows {
		input := "a" + string(r.c) + "b"
		t.Run(fmt.Sprintf("cooked %#02x", r.c), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, r.cooked, receive(newPty(1, 2), input))
		})
		t.Run(fmt.Sprintf("raw %#02x", r.c), func(t *testing.T) {
			t.Parallel()
			p := newPty(1, 2)
			p.ld.setRaw(nil)
			assert.Equal(t, ldiscState{}, receive(p, input), "raw mode neither echoes, cooks nor interrupts")
			p.mu.Lock()
			defer p.mu.Unlock()
			assert.Equal(t, input, string(p.ld.rawQ), "raw mode queues verbatim")
		})
	}
}

func TestLdiscRead(t *testing.T) {
	t.Parallel()
	type read struct {
		data string
		eof  bool
	}
	tests := []struct {
		name  string
		typed string
		// bufSize is the size of each read's buffer.
		bufSize int
		// hangup closes the master after typing.
		hangup bool
		reads  []read
	}{
		{name: "one line per read", typed: "a\rb\r", bufSize: 16, reads: []read{{data: "a\n"}, {data: "b\n"}}},
		{name: "a short buffer takes a line in pieces", typed: "hello\r", bufSize: 2, reads: []read{{data: "he"}, {data: "ll"}, {data: "o\n"}}},
		{name: "a piece never crosses into the next line", typed: "abc\rd\r", bufSize: 2, reads: []read{{data: "ab"}, {data: "c\n"}, {data: "d\n"}}},
		{name: "ctrl-d after text delivers it without newline", typed: "ab\x04", bufSize: 16, reads: []read{{data: "ab"}}},
		{name: "lines come before the EOF", typed: "a\r\x04", bufSize: 16, reads: []read{{data: "a\n"}, {eof: true}}},
		{name: "ctrl-d on an empty line reads EOF", typed: "\x04", bufSize: 16, reads: []read{{eof: true}}},
		{name: "queued lines are delivered before the hangup EOF", typed: "a\rb\r", bufSize: 16, hangup: true,
			reads: []read{{data: "a\n"}, {data: "b\n"}, {eof: true}, {eof: true}}},
		{name: "a partial line is lost on hangup", typed: "abc", bufSize: 16, hangup: true, reads: []read{{eof: true}}},
		{name: "erased input is never delivered", typed: "abc\x15xy\x7fz\r", bufSize: 16, reads: []read{{data: "xz\n"}}},
		{name: "input before ctrl-c is never delivered", typed: "lost\rlost\x03kept\r", bufSize: 16, reads: []read{{data: "kept\n"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(1, 2)
			receive(p, tc.typed)
			if tc.hangup {
				p.closeMaster()
			}
			var got []read
			buf := make([]byte, tc.bufSize)
			for range tc.reads {
				n, err := p.ld.read(context.Background(), buf)
				if err != nil {
					require.ErrorIs(t, err, io.EOF)
				}
				got = append(got, read{data: string(buf[:n]), eof: err != nil})
			}
			assert.Equal(t, tc.reads, got)
		})
	}
}

func TestLdiscReadBlocksUntilInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// wake unblocks the read.
		wake    func(p *pty, cancel context.CancelFunc)
		want    string
		wantErr error
	}{
		{name: "a typed line", wake: func(p *pty, _ context.CancelFunc) { receive(p, "hi\r") }, want: "hi\n"},
		{name: "a line typed across writes", wake: func(p *pty, _ context.CancelFunc) { receive(p, "h", "i", "\r") }, want: "hi\n"},
		{name: "ctrl-d", wake: func(p *pty, _ context.CancelFunc) { receive(p, "\x04") }, wantErr: io.EOF},
		{name: "a hangup", wake: func(p *pty, _ context.CancelFunc) { p.closeMaster() }, wantErr: io.EOF},
		{name: "the context ending", wake: func(_ *pty, cancel context.CancelFunc) { cancel() }, wantErr: context.Canceled},
		{name: "the context ending after a switch to raw mode", wake: func(p *pty, cancel context.CancelFunc) {
			receive(p, "partial")
			p.ld.setRaw(nil)
			cancel()
		}, wantErr: context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(1, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			results := make(chan readResult, 1)
			go debug.CapturePanicReport(func() {
				buf := make([]byte, 16)
				n, err := p.ld.read(ctx, buf)
				results <- readResult{data: string(buf[:n]), err: err}
			})
			assertBlocked(t, results)
			tc.wake(p, cancel)
			r := awaitRead(t, results)
			assert.ErrorIs(t, r.err, tc.wantErr)
			assert.Equal(t, tc.want, r.data)
		})
	}
}

func TestLdiscEOFIsConsumedOnce(t *testing.T) {
	t.Parallel()
	p := newPty(1, 2)
	receive(p, "\x04")
	_, err := p.ld.read(context.Background(), make([]byte, 1))
	require.ErrorIs(t, err, io.EOF)

	results := make(chan readResult, 1)
	go debug.CapturePanicReport(func() {
		buf := make([]byte, 16)
		n, err := p.ld.read(context.Background(), buf)
		results <- readResult{data: string(buf[:n]), err: err}
	})
	assertBlocked(t, results)
	receive(p, "next\r")
	r := awaitRead(t, results)
	require.NoError(t, r.err)
	assert.Equal(t, "next\n", r.data)
}

func TestLdiscConcurrentReadersShareTheLines(t *testing.T) {
	t.Parallel()
	const lines = 200
	p := newPty(1, 2)
	got := make(chan string, lines)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			buf := make([]byte, 64)
			for {
				n, err := p.ld.read(context.Background(), buf)
				if err != nil {
					return
				}
				got <- string(buf[:n])
			}
		})
	}
	var writers sync.WaitGroup
	for w := range 2 {
		writers.Add(1)
		go debug.CapturePanicReport(func() {
			defer writers.Done()
			for i := range lines / 2 {
				// One write per line keeps each line whole: lines from
				// two writers interleave, never their bytes.
				_, err := p.writeMaster([]byte(strings.Repeat(string(rune('a'+w)), i%7+1) + "\r"))
				assert.NoError(t, err)
			}
		})
	}
	writers.Wait()
	seen := map[string]int{}
	for range lines {
		select {
		case l := <-got:
			seen[l]++
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d lines were read", len(seen))
		}
	}
	p.closeMaster()
	wg.Wait()
	want := map[string]int{}
	for w := range 2 {
		for i := range lines / 2 {
			want[strings.Repeat(string(rune('a'+w)), i%7+1)+"\n"]++
		}
	}
	assert.Equal(t, want, seen, "every line is read exactly once by some reader")
}

func TestLdiscSetRaw(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cooked   string
		handback string
		rawQ     string
	}{
		{name: "nothing typed", rawQ: ""},
		{name: "handback, lines and the partial line in order",
			cooked: "one\rtwo\rthr", handback: "zero\n", rawQ: "zero\rone\rtwo\rthr"},
		{name: "a pending EOF is replayed as ctrl-d", cooked: "\x04", rawQ: "\x04"},
		{name: "a pending EOF follows the lines", cooked: "a\r\x04", rawQ: "a\r\x04"},
		{name: "a line completed by ctrl-d has no line end", cooked: "ab\x04cd", rawQ: "abcd"},
		{name: "handback newlines become carriage returns", handback: "a\nb\n", rawQ: "a\rb\r"},
		{name: "erased input is not replayed", cooked: "abc\x7f\x15de", rawQ: "de"},
		{name: "a half-typed escape is not replayed", cooked: "a\x1b[", rawQ: "a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(1, 2)
			receive(p, tc.cooked)
			p.ld.setRaw([]byte(tc.handback))
			p.mu.Lock()
			defer p.mu.Unlock()
			assert.True(t, p.ld.raw)
			assert.Equal(t, tc.rawQ, string(p.ld.rawQ))
			assert.Nil(t, p.ld.lines)
			assert.Nil(t, p.ld.line)
			assert.False(t, p.ld.eof)
			assert.Nil(t, p.ld.escPending)
			assert.Nil(t, p.ld.utf8Pending)
		})
	}
}

func TestLdiscSetCooked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want ldiscState
	}{
		{name: "nothing typed", want: ldiscState{}},
		{name: "typeahead is cooked and echoed", raw: "ab\x7fc\rrest",
			want: ldiscState{echo: "ab\b \bc\r\nrest", lines: []string{"ac\n"}, partial: "rest"}},
		{name: "a ctrl-c in the typeahead interrupts", raw: "a\x03b", want: ldiscState{echo: "a^Cb", partial: "b", interrupt: true}},
		{name: "a ctrl-d in the typeahead is EOF", raw: "\x04", want: ldiscState{eof: true}},
		{name: "escape sequences in the typeahead are dropped", raw: "\x1b[Aab\x1bOD", want: ldiscState{echo: "ab", partial: "ab"}},
		{name: "several lines", raw: "a\rb\nc\r\n", want: ldiscState{echo: "a\r\nb\r\nc\r\n", lines: []string{"a\n", "b\n", "c\n"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(1, 2)
			p.ld.setRaw(nil)
			receive(p, tc.raw)
			interrupt := p.ld.setCooked()
			got := receive(p)
			got.interrupt = interrupt
			assert.Equal(t, tc.want, got)
			p.mu.Lock()
			defer p.mu.Unlock()
			assert.False(t, p.ld.raw)
			assert.Nil(t, p.ld.rawQ)
		})
	}
}

func TestLdiscRawSkipsLFAfterChunkEndingInCR(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		writes []string
		rawQ   string
	}{
		{name: "a leading LF is dropped", writes: []string{"\nab"}, rawQ: "ab"},
		{name: "a chunk not starting with LF clears the flag without dropping", writes: []string{"cd", "\n"}, rawQ: "cd\n"},
		{name: "an empty write keeps the flag", writes: []string{"", "\nx"}, rawQ: "x"},
		{name: "only one LF is dropped", writes: []string{"\n\n"}, rawQ: "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(1, 2)
			p.ld.setRaw(nil)
			p.mu.Lock()
			p.ld.skipLF = true
			p.mu.Unlock()
			receive(p, tc.writes...)
			p.mu.Lock()
			defer p.mu.Unlock()
			assert.Equal(t, tc.rawQ, string(p.ld.rawQ))
		})
	}
}

func TestLdiscSetCookedClearsSkipLF(t *testing.T) {
	t.Parallel()
	p := newPty(1, 2)
	p.ld.setRaw(nil)
	p.mu.Lock()
	p.ld.skipLF = true
	p.mu.Unlock()
	p.ld.setCooked()
	assert.Equal(t, ldiscState{echo: "\r\n", lines: []string{"\n"}}, receive(p, "\n"))
}

func TestLdiscInterruptHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		raw   bool
		typed string
		calls int
	}{
		{name: "ctrl-c in cooked mode", typed: "\x03", calls: 1},
		{name: "one call per write with ctrl-c", typed: "\x03\x03", calls: 1},
		{name: "no ctrl-c", typed: "abc\r"},
		{name: "ctrl-c in raw mode goes to the prompt", raw: true, typed: "\x03"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPty(1, 2)
			if tc.raw {
				p.ld.setRaw(nil)
			}
			calls := 0
			p.ld.setInterruptHandler(func() {
				// Taking the lock proves the callback runs with it
				// released.
				p.mu.Lock()
				defer p.mu.Unlock()
				calls++
			})
			_, err := (&ptyMaster{p: p}).Write([]byte(tc.typed))
			require.NoError(t, err)
			assert.Equal(t, tc.calls, calls)
		})
	}
}

func TestLdiscWithoutInterruptHandler(t *testing.T) {
	t.Parallel()
	p := newPty(1, 2)
	n, err := (&ptyMaster{p: p}).Write([]byte("a\x03"))
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, "a^C", drainMaster(p))
}
