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
	"cmp"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/term"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
	"unstable.build/rune/internal/debug"
)

// masterOutput collects everything the master reads from a pty.
type masterOutput struct {
	mu  sync.Mutex
	buf strings.Builder
	// changed is closed and replaced on every read.
	changed chan struct{}
}

func collectMaster(p *pty) *masterOutput {
	o := &masterOutput{changed: make(chan struct{})}
	go debug.CapturePanicReport(func() {
		buf := make([]byte, 4096)
		for {
			n, err := p.readMaster(buf)
			o.mu.Lock()
			o.buf.Write(buf[:n])
			close(o.changed)
			o.changed = make(chan struct{})
			o.mu.Unlock()
			if err != nil {
				return
			}
		}
	})
	return o
}

func (o *masterOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// waitFor blocks until the output contains substr and returns it.
func (o *masterOutput) waitFor(t *testing.T, substr string) string {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		o.mu.Lock()
		out, changed := o.buf.String(), o.changed
		o.mu.Unlock()
		if strings.Contains(out, substr) {
			return out
		}
		select {
		case <-changed:
		case <-timeout:
			t.Fatalf("output never contained %q:\n%q", substr, out)
		}
	}
}

// newPromptSession is a session with a raw-mode pty and no shell
// behind it, enough for the prompt reader.
func newPromptSession(t *testing.T) (*session, *pty) {
	t.Helper()
	p := newPty(1, 2)
	p.ld.setRaw(nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &session{
		proc:    &process{ctx: ctx, pty: p},
		e:       &Executor{cfg: Config{Scheme: "memory"}},
		p:       p,
		history: &history{},
	}
	return s, p
}

// readLineAsync runs readLine in the background.
func readLineAsync(s *session, prompt string) <-chan readResult {
	ch := make(chan readResult, 1)
	go debug.CapturePanicReport(func() {
		line, err := s.readLine(prompt)
		ch <- readResult{data: line, err: err}
	})
	return ch
}

// chunk is one Read outcome of a promptReader.
type chunk struct {
	data string
	err  error
}

// readChunks drains the raw queue through r, one Read per chunk, and
// reports the chunks in order.
func readChunks(t *testing.T, r *promptReader) []chunk {
	t.Helper()
	var chunks []chunk
	buf := make([]byte, 64)
	for {
		r.s.p.mu.Lock()
		// A queue holding only ^G would make Read block after
		// answering it.
		pending := strings.Trim(string(r.s.p.ld.rawQ), string(rune(ctrlG)))
		r.s.p.mu.Unlock()
		if pending == "" {
			return chunks
		}
		n, err := r.Read(buf)
		chunks = append(chunks, chunk{data: string(buf[:n]), err: err})
	}
}

func TestPromptReaderChunksKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		typed  []string
		chunks []chunk
		echoed string
	}{
		{name: "a line ends at the carriage return, which comes on its own",
			typed: []string{"abc\r"}, chunks: []chunk{{data: "abc"}, {data: "\r"}}},
		{name: "a line feed becomes a carriage return",
			typed: []string{"abc\n"}, chunks: []chunk{{data: "abc"}, {data: "\r"}}},
		{name: "CRLF is one line end",
			typed: []string{"abc\r\n"}, chunks: []chunk{{data: "abc"}, {data: "\r"}}},
		{name: "keys after the line end wait for the next read",
			typed: []string{"ab\rcd"}, chunks: []chunk{{data: "ab"}, {data: "\r"}, {data: "cd"}}},
		{name: "a late line feed after a carriage return is dropped",
			typed: []string{"ab\r", "\ncd"}, chunks: []chunk{{data: "ab"}, {data: "\r"}, {data: "cd"}}},
		{name: "a late carriage return after one is kept",
			typed: []string{"ab\r", "\rcd"}, chunks: []chunk{{data: "ab"}, {data: "\r"}, {data: "\r"}, {data: "cd"}}},
		{name: "empty lines",
			typed: []string{"\r\r\n\n"}, chunks: []chunk{{data: "\r"}, {data: "\r"}, {data: "\r"}}},
		{name: "LF LF is two lines",
			typed: []string{"\n\n"}, chunks: []chunk{{data: "\r"}, {data: "\r"}}},
		{name: "^G ends the chunk before it and is answered with BEL",
			typed: []string{"ab\x07cd"}, chunks: []chunk{{data: "ab"}, {data: "cd"}}, echoed: "\x07"},
		{name: "a lone ^G is answered without a chunk",
			typed: []string{"\x07", "x"}, chunks: []chunk{{data: "x"}}, echoed: "\x07"},
		{name: "each ^G is answered",
			typed: []string{"a\x07\x07b"}, chunks: []chunk{{data: "a"}, {data: "b"}}, echoed: "\x07\x07"},
		{name: "^C ends the chunk before it and interrupts",
			typed: []string{"ab\x03"}, chunks: []chunk{{data: "ab"}, {err: errInterrupt}}},
		{name: "^C discards nothing typed after it",
			typed: []string{"\x03zz\r"}, chunks: []chunk{{err: errInterrupt}, {data: "zz"}, {data: "\r"}}},
		{name: "each ^C interrupts",
			typed: []string{"\x03\x03"}, chunks: []chunk{{err: errInterrupt}, {err: errInterrupt}}},
		{name: "SS3 arrows become CSI arrows",
			typed: []string{"\x1bOA\x1bOD"}, chunks: []chunk{{data: "\x1b[A\x1b[D"}}},
		{name: "control keys other than ^C and ^G pass through",
			typed: []string{"\x01\x04\x05\x15\x7f\x1b[3~"}, chunks: []chunk{{data: "\x01\x04\x05\x15\x7f\x1b[3~"}}},
		{name: "utf-8 passes through",
			typed: []string{"日本\r"}, chunks: []chunk{{data: "日本"}, {data: "\r"}}},
		{name: "a queue longer than the buffer comes in pieces",
			typed: []string{strings.Repeat("a", 100)}, chunks: []chunk{{data: strings.Repeat("a", 64)}, {data: strings.Repeat("a", 36)}}},
		{name: "a Tab ends its chunk",
			typed: []string{"ab\tcd"}, chunks: []chunk{{data: "ab\t"}, {data: "cd"}}},
		{name: "a lone Tab is a chunk",
			typed: []string{"\t"}, chunks: []chunk{{data: "\t"}}},
		{name: "each Tab ends a chunk",
			typed: []string{"\t\tx"}, chunks: []chunk{{data: "\t"}, {data: "\t"}, {data: "x"}}},
		{name: "Enter after a Tab is a chunk of its own",
			typed: []string{"a\t\r"}, chunks: []chunk{{data: "a\t"}, {data: "\r"}}},
		{name: "a Tab in a paste ends its chunk too",
			typed: []string{"\x1b[200~a\tb\x1b[201~"}, chunks: []chunk{{data: "\x1b[200~a\t"}, {data: "b\x1b[201~"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p := newPromptSession(t)
			r := &promptReader{s: s, markedPromptEnd: true}
			var got []chunk
			for _, keys := range tc.typed {
				_, err := p.writeMaster([]byte(keys))
				require.NoError(t, err)
				got = append(got, readChunks(t, r)...)
			}
			assert.Equal(t, tc.chunks, got)
			assert.Equal(t, tc.echoed, drainMaster(p))
		})
	}
}

func TestPromptReaderEndsReading(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		end  func(p *pty, cancel context.CancelFunc)
	}{
		{name: "on hangup", end: func(p *pty, _ context.CancelFunc) { p.closeMaster() }},
		{name: "when the process ends", end: func(_ *pty, cancel context.CancelFunc) { cancel() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p := newPromptSession(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.proc.ctx = ctx
			collectMaster(p)
			ch := readLineAsync(s, "$ ")
			assertBlocked(t, ch)
			tc.end(p, cancel)
			r := awaitRead(t, ch)
			assert.ErrorIs(t, r.err, io.EOF)
		})
	}
}

func TestPromptReaderReadsThePrefillFirst(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prefill string
		typed   string
		chunks  []chunk
	}{
		{name: "the prefill, then what was typed", prefill: "ab", typed: "cd\r",
			chunks: []chunk{{data: "ab"}, {data: "cd"}, {data: "\r"}}},
		{name: "a prefill longer than the buffer comes in pieces", prefill: strings.Repeat("p", 100), typed: "x",
			chunks: []chunk{{data: strings.Repeat("p", 64)}, {data: strings.Repeat("p", 36)}, {data: "x"}}},
		{name: "the prefill is not chunked at keys", prefill: "a\tb\rc\x07", typed: "x",
			chunks: []chunk{{data: "a\tb\rc\x07"}, {data: "x"}}},
		{name: "^C typed waits for the prefill", prefill: "ab", typed: "\x03",
			chunks: []chunk{{data: "ab"}, {err: errInterrupt}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p := newPromptSession(t)
			r := &promptReader{s: s, markedPromptEnd: true, prefill: []byte(tc.prefill)}
			_, err := p.writeMaster([]byte(tc.typed))
			require.NoError(t, err)
			assert.Equal(t, tc.chunks, readChunks(t, r))
			assert.Empty(t, r.prefill)
		})
	}
}

func TestPromptReaderEntersTheLineToList(t *testing.T) {
	t.Parallel()
	s, p := newPromptSession(t)
	r := &promptReader{s: s, markedPromptEnd: true, prefill: []byte("pre"), listing: []string{"a", "b"}}
	_, err := p.writeMaster([]byte("typed"))
	require.NoError(t, err)
	buf := make([]byte, 8)
	for range 2 {
		n, err := r.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, "\r", string(buf[:n]), "a listing ends the line before anything else is read")
	}
	assert.Equal(t, "pre", string(r.prefill))
	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Equal(t, "typed", string(p.ld.rawQ), "what was typed waits for the next prompt")
}

// completion is what a stubbed Tab returns.
type completion struct {
	line string
	pos  int
	list []string
}

// tabCall is what a stubbed Tab was asked.
type tabCall struct {
	line string
	pos  int
}

// stubComplete makes Tab in s answer results in turn, then leave the
// line as is, and returns what it is asked.
func stubComplete(s *session, results ...completion) *[]tabCall {
	var calls []tabCall
	s.complete = func(line string, pos int) (string, int, []string) {
		calls = append(calls, tabCall{line: line, pos: pos})
		if len(results) == 0 {
			return line, pos, nil
		}
		r := results[0]
		results = results[1:]
		return r.line, r.pos, r.list
	}
	return &calls
}

func TestPromptReadLineCompletes(t *testing.T) {
	t.Parallel()
	list := completion{list: []string{"one", "two"}}
	tests := []struct {
		name    string
		cols    int
		keys    string
		results []completion
		calls   []tabCall
		line    string
		err     error
	}{
		{name: "Tab puts in the completed line", keys: "cat age\tx\r",
			results: []completion{{line: "cat agent/", pos: 10}}, calls: []tabCall{{"cat age", 7}}, line: "cat agent/x"},
		{name: "the cursor goes where the completion leaves it", keys: "abc\x1b[D\tZ\r",
			results: []completion{{line: "abXYc", pos: 4}}, calls: []tabCall{{"abc", 2}}, line: "abXYZc"},
		{name: "a completion leaving the line changes nothing", keys: "ab\tc\r",
			results: []completion{{line: "ab", pos: 2}}, calls: []tabCall{{"ab", 2}}, line: "abc"},
		{name: "a listing keeps the line and the cursor", keys: "ab\x1b[D\tX\r",
			results: []completion{list}, calls: []tabCall{{"ab", 1}}, line: "aXb"},
		{name: "keys typed with the Tab follow the listing", keys: "cat a\tx\r",
			results: []completion{list}, calls: []tabCall{{"cat a", 5}}, line: "cat ax"},
		{name: "listings in a row", keys: "a\t\tb\r",
			results: []completion{list, list}, calls: []tabCall{{"a", 1}, {"a", 1}}, line: "ab"},
		{name: "a completion after a listing", keys: "a\t\tb\r",
			results: []completion{list, {line: "abc", pos: 3}}, calls: []tabCall{{"a", 1}, {"a", 1}}, line: "abcb"},
		{name: "a listing after a completion", keys: "a\t\tb\r",
			results: []completion{{line: "abc", pos: 3}, list}, calls: []tabCall{{"a", 1}, {"abc", 3}}, line: "abcb"},
		{name: "positions count bytes", keys: "日本\x1b[D\t\r",
			results: []completion{list}, calls: []tabCall{{"日本", 3}}, line: "日本"},
		{name: "a completion's position counts bytes", keys: "é\tX\r",
			results: []completion{{line: "éàü", pos: 4}}, calls: []tabCall{{"é", 2}}, line: "éàXü"},
		{name: "an empty line lists", keys: "\t\r", results: []completion{list}, calls: []tabCall{{"", 0}}, line: ""},
		{name: "a pasted Tab is text", keys: "\x1b[200~a\tb\x1b[201~\r", line: "a\tb"},
		{name: "a listed line is typed again as text", keys: "\x1b[200~a\tb\x1b[201~\t\r",
			results: []completion{list}, calls: []tabCall{{"a\tb", 3}}, line: "a\tb"},
		{name: "other keys do not complete", keys: "a\x01\x05\x02\x06b\r", line: "ab"},
		{name: "^C after a listing interrupts", keys: "a\t\x03", results: []completion{list}, calls: []tabCall{{"a", 1}},
			err: errInterrupt},
		{name: "^D after a listing on its empty line is EOF", keys: "\t\x04", results: []completion{list},
			calls: []tabCall{{"", 0}}, err: io.EOF},
		{name: "a listing of a wrapped line", cols: 5, keys: "abcdefgh\x01\x06\x06\tX\r",
			results: []completion{list}, calls: []tabCall{{"abcdefgh", 2}}, line: "abXcdefgh"},
		{name: "history recalls past a listing", keys: "\x1b[A\t\r",
			results: []completion{list}, calls: []tabCall{{"old", 3}}, line: "old"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p := newPromptSession(t)
			s.history.Add("old")
			calls := stubComplete(s, tc.results...)
			require.NoError(t, p.setSize(workspaceapi.PtySize{Columns: cmp.Or(tc.cols, 80), Rows: 24}))
			collectMaster(p)
			_, err := p.writeMaster([]byte(tc.keys))
			require.NoError(t, err)
			line, err := s.readLine("$ ")
			assert.ErrorIs(t, err, tc.err)
			assert.Equal(t, tc.line, line)
			assert.Equal(t, tc.calls, *calls)
			want := []string{"old"}
			if tc.err == nil && tc.line != "" && tc.line != "old" {
				want = append(want, tc.line)
			}
			assert.Equal(t, want, s.history.entries, "a line ended to list is not remembered")
		})
	}
}

func TestPromptReadLineDrawsCompletions(t *testing.T) {
	t.Parallel()
	const a, b = "\x1b]133;A\x1b\\", "\x1b]133;B\x1b\\"
	tests := []struct {
		name    string
		cols    int
		keys    string
		results []completion
		want    string
	}{
		{name: "a completion redraws the line", keys: "ca\t\r", results: []completion{{line: "cat ", pos: 4}},
			want: "$ " + b + "ca" + "\x1b[2Dcat " + "\r\n"},
		{name: "a listing goes below the line and the prompt comes again",
			keys: "ab\t\r", results: []completion{{list: []string{"one", "two"}}},
			want: "$ " + b + "ab" + "\r\n" + "one  two\r\n" + a + "$ " + b + "ab" + "\r\n"},
		{name: "a listing goes below the whole line",
			keys: "ab\x1b[D\t\r", results: []completion{{list: []string{"one", "two"}}},
			want: "$ " + b + "ab\x1b[D" + "\x1b[C\r\n" + "one  two\r\n" + a + "$ " + b + "ab\x1b[D" + "\x1b[C\r\n"},
		{name: "a listing fits the terminal", cols: 8,
			keys: "\t\r", results: []completion{{list: []string{"aaa", "bbb", "ccc"}}},
			want: "$ " + b + "\r\n" + "aaa  ccc\r\nbbb\r\n" + a + "$ " + b + "\r\n"},
		{name: "a listing shows paths by their last element",
			keys: "\t\r", results: []completion{{list: []string{"docs/a.md", "docs/sub/"}}},
			want: "$ " + b + "\r\n" + "a.md  sub/\r\n" + a + "$ " + b + "\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p := newPromptSession(t)
			stubComplete(s, tc.results...)
			require.NoError(t, p.setSize(workspaceapi.PtySize{Columns: cmp.Or(tc.cols, 80), Rows: 24}))
			_, err := p.writeMaster([]byte(tc.keys))
			require.NoError(t, err)
			_, err = s.readLine("$ ")
			require.NoError(t, err)
			assert.Equal(t, tc.want, drainMaster(p))
		})
	}
}

func TestPromptRetype(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
		pos  int
		want string
	}{
		{name: "nothing to type", line: "", pos: 0, want: ""},
		{name: "the cursor at the end", line: "ab", pos: 2, want: "\x1b[200~ab\x1b[201~"},
		{name: "the cursor at the start", line: "ab", pos: 0, want: "\x1b[200~ab\x1b[201~\x1b[D\x1b[D"},
		{name: "moves count characters", line: "日本", pos: 3, want: "\x1b[200~日本\x1b[201~\x1b[D"},
		{name: "keys are typed as text", line: "a\tb\x01", pos: 1, want: "\x1b[200~a\tb\x01\x1b[201~\x1b[D\x1b[D\x1b[D"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, string(retype(tc.line, tc.pos)))
		})
	}
}

func TestPromptHistorySkipsTheListedLine(t *testing.T) {
	t.Parallel()
	h := &history{}
	r := &promptReader{}
	ph := promptHistory{history: h, r: r}
	ph.Add("entered")
	r.listing = []string{"x"}
	ph.Add("listed")
	assert.Equal(t, []string{"entered"}, h.entries)
	assert.Equal(t, 1, ph.Len())
	assert.Equal(t, "entered", ph.At(0))
}

func TestPromptReadLineEditing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		history []string
		cols    int
		keys    string
		line    string
		err     error
	}{
		{name: "a typed line", keys: "echo hi\r", line: "echo hi"},
		{name: "an empty line", keys: "\r", line: ""},
		{name: "LF ends the line", keys: "ls\n", line: "ls"},
		{name: "keys after Enter wait for the next line", keys: "a\rb\r", line: "a"},
		{name: "left arrow inserts before the cursor", keys: "ac\x1b[Db\r", line: "abc"},
		{name: "SS3 left arrow inserts before the cursor", keys: "ac\x1bODb\r", line: "abc"},
		{name: "right arrow at the end is a no-op", keys: "ab\x1b[Cc\r", line: "abc"},
		{name: "left arrow at the start is a no-op", keys: "\x1b[Dab\r", line: "ab"},
		{name: "ctrl-b and ctrl-f move", keys: "ab\x02\x02X\x06Y\r", line: "XaYb"},
		{name: "ctrl-a goes home and ctrl-e to the end", keys: "bc\x01a\x05d\r", line: "abcd"},
		{name: "home and end keys", keys: "bc\x1b[Ha\x1b[Fd\r", line: "abcd"},
		{name: "ctrl-k kills to the end", keys: "abcdef\x01\x06\x06\x0b\r", line: "ab"},
		{name: "ctrl-k at the end is a no-op", keys: "abc\x0b\r", line: "abc"},
		{name: "ctrl-u kills to the start", keys: "abc\x02\x15\r", line: "c"},
		{name: "ctrl-w deletes a word", keys: "echo foo bar\x17\r", line: "echo foo "},
		{name: "ctrl-w deletes trailing spaces and a word", keys: "echo foo   \x17\r", line: "echo "},
		{name: "ctrl-w in the middle", keys: "aa bb cc\x02\x02\x02\x17\r", line: "aa  cc"},
		{name: "backspace in the middle", keys: "abxc\x02\x7f\r", line: "abc"},
		{name: "ctrl-h in the middle", keys: "abxc\x02\x08\r", line: "abc"},
		{name: "backspace at the start is a no-op", keys: "ab\x01\x7f\r", line: "ab"},
		{name: "delete under the cursor", keys: "abxc\x02\x02\x1b[3~\r", line: "abc"},
		{name: "ctrl-d deletes under the cursor", keys: "abxc\x02\x02\x04\r", line: "abc"},
		{name: "ctrl-d at the end of a line is a no-op", keys: "ab\x04\r", line: "ab"},
		{name: "ctrl-d on an empty line is EOF", keys: "\x04", err: io.EOF},
		{name: "ctrl-d after erasing the line is EOF", keys: "ab\x15\x04", err: io.EOF},
		{name: "ctrl-c interrupts", keys: "abc\x03", err: errInterrupt},
		{name: "ctrl-t transposes", keys: "ab\x14\r", line: "ba"},
		{name: "alt-left and alt-right move by words", keys: "foo bar\x1b[1;3DX\x1b[1;3CY\r", line: "foo XbarY"},
		{name: "ctrl-l keeps the line", keys: "ab\x0c\r", line: "ab"},
		{name: "a bell in the line is dropped", keys: "a\x07b\r", line: "ab"},
		{name: "a tab is dropped", keys: "a\tb\r", line: "ab"},
		{name: "utf-8", keys: "héllo wörld\r", line: "héllo wörld"},
		{name: "wide characters are edited by rune", keys: "日本語\x7f\x02X\r", line: "日X本"},
		{name: "a combining mark is erased on its own", keys: "e\u0301\x7f\r", line: "e"},
		{name: "four-byte runes", keys: "😀\x1b[Da\r", line: "a😀"},
		{name: "a line longer than a narrow terminal", cols: 5, keys: "abcdefghijkl\r", line: "abcdefghijkl"},
		{name: "editing a wrapped line", cols: 5, keys: "abcdefgh\x01\x06\x06\x06\x06\x06X\r", line: "abcdeXfgh"},

		{name: "up recalls the newest entry", history: []string{"one", "two"}, keys: "\x1b[A\r", line: "two"},
		{name: "up twice recalls the older entry", history: []string{"one", "two"}, keys: "\x1b[A\x1b[A\r", line: "one"},
		{name: "up stops at the oldest entry", history: []string{"one", "two"}, keys: "\x1b[A\x1b[A\x1b[A\x1b[A\r", line: "one"},
		{name: "down returns to the newer entry", history: []string{"one", "two"}, keys: "\x1b[A\x1b[A\x1b[B\r", line: "two"},
		{name: "down past the newest restores the typed line", history: []string{"one"}, keys: "typed\x1b[A\x1b[B\r", line: "typed"},
		{name: "down without history moves is a no-op", history: []string{"one"}, keys: "x\x1b[B\r", line: "x"},
		{name: "a recalled entry can be edited", history: []string{"echo one"}, keys: "\x1b[A\x17two\r", line: "echo two"},
		{name: "ctrl-p and ctrl-n walk the history", history: []string{"one", "two"}, keys: "\x10\x10\x0e\r", line: "two"},
		{name: "up without history is a no-op", keys: "\x1b[Ax\r", line: "x"},

		{name: "a bracketed paste is a line", keys: "\x1b[200~echo hi\x1b[201~\r", line: "echo hi"},
		{name: "a bracketed paste ending in Enter is a line", keys: "\x1b[200~echo hi\r\x1b[201~", line: "echo hi"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p := newPromptSession(t)
			for _, h := range tc.history {
				s.history.Add(h)
			}
			require.NoError(t, p.setSize(workspaceapi.PtySize{Columns: cmp.Or(tc.cols, 80), Rows: 24}))
			collectMaster(p)
			_, err := p.writeMaster([]byte(tc.keys))
			require.NoError(t, err)
			line, err := s.readLine("$ ")
			assert.ErrorIs(t, err, tc.err)
			assert.Equal(t, tc.line, line)
		})
	}
}

func TestPromptReadLineAddsToHistory(t *testing.T) {
	t.Parallel()
	s, p := newPromptSession(t)
	collectMaster(p)
	_, err := p.writeMaster([]byte("first\r\rsecond\rsecond\r\x1b[A\x1b[A\r"))
	require.NoError(t, err)
	var lines []string
	for range 5 {
		line, err := s.readLine("$ ")
		require.NoError(t, err)
		lines = append(lines, line)
	}
	assert.Equal(t, []string{"first", "", "second", "second", "first"}, lines,
		"blank and repeated lines are not remembered, so two ups reach first")
}

func TestPromptReadLineDraws(t *testing.T) {
	t.Parallel()
	const b = "\x1b]133;B\x1b\\"
	tests := []struct {
		name string
		cols int
		keys string
		want string
	}{
		{name: "typed text", keys: "ls\r", want: "$ " + b + "ls\r\n"},
		{name: "backspace clears instead of overwriting", keys: "abc\x7f\r", want: "$ " + b + "abc\x1b[D\x1b[K\r\n"},
		{name: "insertion redraws the tail", keys: "ac\x1b[Db\r", want: "$ " + b + "ac\x1b[Dbc\x1b[D\x1b[C\r\n"},
		{name: "ctrl-u clears the line", keys: "abc\x15\r", want: "$ " + b + "abc\x1b[3D\x1b[K\r\n"},
		{name: "ctrl-k clears the tail", keys: "abc\x01\x0b\r", want: "$ " + b + "abc\x1b[3D\x1b[K\r\n"},
		{name: "^G after ^A rings after the move", keys: "abc\x01\x07\r", want: "$ " + b + "abc\x1b[3D\x07\x1b[3C\r\n"},
		{name: "^G before keys rings before their echo", keys: "\x07abc\r", want: "$ " + b + "\x07abc\r\n"},
		{name: "^G between keys rings between their echoes", keys: "a\x07b\r", want: "$ " + b + "a\x07b\r\n"},
		{name: "a filled row wraps by the auto-margin", cols: 5, keys: "abc\r", want: "$ " + b + "abc\r\n"},
		{name: "a long line wraps by the auto-margin", cols: 5, keys: "abcdefgh\r", want: "$ " + b + "abcdefgh\r\n"},
		{name: "erasing across a wrap clears both rows", cols: 5, keys: "abcd\x15x\r",
			want: "$ " + b + "abcd\x1b[A\x1b[C \x1b[K\r\n\x1b[K\x1b[C\x1b[A\x1b[Cx\r\n"},
		{name: "backspace on the second row", cols: 5, keys: "abcd\x7f\r", want: "$ " + b + "abcd\x1b[D\x1b[K\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p := newPromptSession(t)
			require.NoError(t, p.setSize(workspaceapi.PtySize{Columns: cmp.Or(tc.cols, 80), Rows: 24}))
			_, err := p.writeMaster([]byte(tc.keys))
			require.NoError(t, err)
			_, err = s.readLine("$ ")
			require.NoError(t, err)
			assert.Equal(t, tc.want, drainMaster(p))
		})
	}
}

func TestPromptReaderBelFollowsTheRedraw(t *testing.T) {
	t.Parallel()
	s, p := newPromptSession(t)
	out := collectMaster(p)
	lineCh := readLineAsync(s, "$ ")
	_, err := p.writeMaster([]byte("abc\x01\x07"))
	require.NoError(t, err)
	got := out.waitFor(t, "\x07")
	assert.True(t, strings.HasSuffix(got, "abc\x1b[3D\x07"), "BEL must follow the cursor move ^A caused: %q", got)

	_, err = p.writeMaster([]byte("\r"))
	require.NoError(t, err)
	r := awaitRead(t, lineCh)
	require.NoError(t, r.err)
	assert.Equal(t, "abc", r.data)
}

func TestPromptReaderResizesTheEditor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size workspaceapi.PtySize
		want string
	}{
		{name: "wider", size: workspaceapi.PtySize{Columns: 40, Rows: 10}, want: "\x1b[5D\x1b[K$ abc"},
		{name: "narrower than the line", size: workspaceapi.PtySize{Columns: 4, Rows: 10}, want: "\x1b[3D\x1b[K$ abc"},
		{name: "a zero size", size: workspaceapi.PtySize{}, want: "\x1b[K$ abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, p := newPromptSession(t)
			out := collectMaster(p)
			_, err := p.writeMaster([]byte("abc"))
			require.NoError(t, err)
			lineCh := readLineAsync(s, "$ ")
			before := out.waitFor(t, "$ "+string(oscMark("B"))+"abc")

			require.NoError(t, p.setSize(tc.size))
			got := out.waitFor(t, tc.want)
			assert.Equal(t, before+tc.want, got, "a resized terminal redraws the prompt and line")

			_, err = p.writeMaster([]byte("d\r"))
			require.NoError(t, err)
			r := awaitRead(t, lineCh)
			require.NoError(t, r.err)
			assert.Equal(t, "abcd", r.data)
			p.mu.Lock()
			defer p.mu.Unlock()
			assert.False(t, p.winch, "the resize was consumed")
		})
	}
}

func TestPromptReaderMarksPromptEndOnFirstRead(t *testing.T) {
	t.Parallel()
	s, p := newPromptSession(t)
	_, err := p.writeMaster([]byte("ls\r"))
	require.NoError(t, err)
	line, err := s.readLine("$ ")
	require.NoError(t, err)
	assert.Equal(t, "ls", line)
	assert.Equal(t, "$ "+string(oscMark("B"))+"ls\r\n", drainMaster(p),
		"the prompt-end mark follows the drawn prompt and precedes the echo")
}

func TestPromptReaderInterruptDiscardsTheLine(t *testing.T) {
	t.Parallel()
	s, p := newPromptSession(t)
	collectMaster(p)
	_, err := p.writeMaster([]byte("abc\x03def\r"))
	require.NoError(t, err)
	_, err = s.readLine("$ ")
	require.ErrorIs(t, err, errInterrupt)
	line, err := s.readLine("$ ")
	require.NoError(t, err)
	assert.Equal(t, "def", line)
}

// draw is one flush of the editor: its bytes and the length of the
// line it holds afterwards.
type draw struct {
	out  string
	line int
}

func TestPromptOutputDrawsLikeReadline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		width int
		draws []draw
		want  string
	}{
		{name: "content passes through",
			draws: []draw{{"$ ", 0}, {"echo bla", 8}}, want: "$ echo bla"},
		{name: "content spaces are kept",
			draws: []draw{{"$ ", 0}, {"a  ", 3}, {"b", 4}}, want: "$ a  b"},
		{name: "a trailing content space is kept",
			draws: []draw{{"$ ", 0}, {"echo ", 5}}, want: "$ echo "},
		{name: "erasing one character clears to the end of the line",
			draws: []draw{{"$ echo bl", 7}, {" \x1b[D", 6}}, want: "$ echo bl\x1b[K"},
		{name: "erasing keeps a trailing content space",
			draws: []draw{{"$ echo b", 6}, {"\x1b[D \x1b[D", 5}}, want: "$ echo b\x1b[D\x1b[K"},
		{name: "erasing the character before a trailing content space",
			draws: []draw{{"$ echo ", 5}, {"\x1b[D", 5}, {"\x1b[D  \x1b[2D", 4}}, want: "$ echo \x1b[D\x1b[D \x1b[K\x1b[D"},
		{name: "erasing two characters at once",
			draws: []draw{{"$ echo bl", 7}, {"l \x1b[2D", 6}, {" \x1b[D", 5}}, want: "$ echo bll\x1b[K\x1b[D\x1b[K"},
		{name: "a shorter history entry clears the rest and keeps the move back",
			draws: []draw{{"$ echo second", 11}, {"\x1b[11Decho first \x1b[D", 10}}, want: "$ echo second\x1b[11Decho first\x1b[K"},
		{name: "erasing more spaces than the move back moves forward",
			draws: []draw{{"$ abcd", 4}, {"\x1b[4D   \x1b[D", 1}}, want: "$ abcd\x1b[4D \x1b[K\x1b[C"},
		{name: "one run of erasing spaces crossing into a wrapped row clears both rows",
			width: 5, draws: []draw{{"$ abcd", 4}, {"\x1b[A\x1b[C    \x1b[A\x1b[C", 0}},
			want: "$ abcd\x1b[A\x1b[C\x1b[K\r\n\x1b[K\x1b[C\x1b[A\x1b[C"},
		{name: "one run of erasing spaces filling the rows below clears them",
			width: 5, draws: []draw{{"$ abcdefgh\r\n", 8}, {"\x1b[2A\x1b[2C        \r\n\x1b[2A\x1b[2C", 0}},
			want: "$ abcdefgh\r\n\x1b[2A\x1b[2C\x1b[K\r\n\x1b[K\r\n\x1b[2A\x1b[2C"},
		{name: "erasing before a vertical move keeps the cursor where the editor thinks",
			draws: []draw{{"$ ab", 2}, {"  \x1b[A", 0}}, want: "$ ab\x1b[K\x1b[2C\x1b[A"},
		{name: "erasing at the end of the output moves over the spaces",
			draws: []draw{{"$ ab", 2}, {"\x1b[2D  ", 0}}, want: "$ ab\x1b[2D\x1b[K\x1b[2C"},
		{name: "without the line length erasures are drawn as spaces",
			draws: []draw{{"$ echo bl", -1}, {" \x1b[D", -1}}, want: "$ echo bl \x1b[D"},
		{name: "an OSC does not move the cursor",
			width: 5, draws: []draw{{"$ \x1b]133;B\x1b\\", 0}, {"abc\r\n", 3}}, want: "$ \x1b]133;B\x1b\\abc"},
		{name: "a row filled exactly wraps by the auto-margin",
			width: 5, draws: []draw{{"$ ", 0}, {"abc\r\n", 3}}, want: "$ abc"},
		{name: "Enter after a filled row adds one newline",
			width: 5, draws: []draw{{"$ ", 0}, {"abc\r\n", 3}, {"\r\n", 0}}, want: "$ abc\r\n"},
		{name: "a character after a filled row wraps by itself",
			width: 5, draws: []draw{{"$ ", 0}, {"abc\r\n", 3}, {"d", 4}}, want: "$ abcd"},
		{name: "a cursor move after a filled row first changes rows",
			width: 5, draws: []draw{{"$ ", 0}, {"abc\r\n", 3}, {"\x1b[A\x1b[4C", 3}}, want: "$ abc\r\n\x1b[A\x1b[4C"},
		{name: "a carriage return after a filled row stays on it",
			width: 5, draws: []draw{{"$ ", 0}, {"abc\r\n", 3}, {"\r", 3}}, want: "$ abc\r"},
		{name: "a wrapped line keeps wrapping",
			width: 5, draws: []draw{{"$ ", 0}, {"abc\r\nde", 5}, {"fgh\r\ni", 9}}, want: "$ abcdefghi"},
		{name: "a row not filled keeps its explicit wrap",
			width: 5, draws: []draw{{"$ ab\r\nc", 3}}, want: "$ ab\r\nc"},
		{name: "erasing across a wrap clears both rows",
			width: 5, draws: []draw{{"$ abc\r\nde", 5}, {"   \r\n  \x1b[A\x1b[2D", 0}},
			want: "$ abcde\x1b[K\r\n\x1b[K\x1b[2C\x1b[A\x1b[2D"},
		{name: "clearing the screen homes the cursor",
			width: 5, draws: []draw{{"$ abc", 3}, {"\x1b[2J\x1b[H$ abc\r\n", 3}}, want: "$ abc\x1b[2J\x1b[H$ abc"},
		{name: "a column move homes the column",
			draws: []draw{{"$ abc", 3}, {"\x1b[G$ abc", 3}}, want: "$ abc\x1b[G$ abc"},
		{name: "wide characters count one cell each, as the editor counts them",
			width: 5, draws: []draw{{"$ ", 0}, {"日本語\r\n", 3}}, want: "$ 日本語"},
		{name: "control bytes pass through",
			draws: []draw{{"$ \x07", 0}}, want: "$ \x07"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := &promptOutput{promptWidth: 2}
			o.resize(cmp.Or(tc.width, 20))
			var got []byte
			for _, d := range tc.draws {
				got = append(got, o.rewrite([]byte(d.out), d.line)...)
			}
			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestPromptOutputResize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, col    int
		resize        int
		wantW, wantCo int
	}{
		{name: "wider keeps the column", width: 10, col: 5, resize: 20, wantW: 20, wantCo: 5},
		{name: "narrower clamps the column", width: 10, col: 8, resize: 4, wantW: 4, wantCo: 4},
		{name: "zero is one column", width: 10, col: 3, resize: 0, wantW: 1, wantCo: 1},
		{name: "negative is one column", width: 10, col: 0, resize: -3, wantW: 1, wantCo: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := &promptOutput{width: tc.width, col: tc.col}
			o.resize(tc.resize)
			assert.Equal(t, tc.wantW, o.width)
			assert.Equal(t, tc.wantCo, o.col)
		})
	}
}

func TestPromptOutputUnsizedDefaultsToEighty(t *testing.T) {
	t.Parallel()
	o := &promptOutput{}
	assert.Equal(t, "x", string(o.rewrite([]byte("x"), -1)))
	assert.Equal(t, 80, o.width)
}

func TestPromptEscapeSequence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"\x1b[31mX", "\x1b[31m"},
		{"\x1b[DX", "\x1b[D"},
		{"\x1b[1;5CX", "\x1b[1;5C"},
		{"\x1b[12", "\x1b[12"},
		{"\x1b]133;A\x1b\\X", "\x1b]133;A\x1b\\"},
		{"\x1b]0;title\x07X", "\x1b]0;title\x07"},
		{"\x1b]0;unterminated", "\x1b]0;unterminated"},
		{"\x1b]0;\x1b", "\x1b]0;\x1b"},
		{"\x1b7X", "\x1b7"},
		{"\x1b", "\x1b"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, string(escapeSequence([]byte(tc.in))), "%q", tc.in)
	}
}

func TestPromptCursorLeft(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"\x1b[DX", "\x1b[D", true},
		{"\x1b[12DX", "\x1b[12D", true},
		{"\x1b[C", "", false},
		{"\x1b[", "", false},
		{"\x1bOD", "", false},
		{"D", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		got, ok := cursorLeft([]byte(tc.in))
		assert.Equal(t, tc.ok, ok, "%q", tc.in)
		assert.Equal(t, tc.want, string(got), "%q", tc.in)
	}
}

func TestPromptCSICount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int
	}{
		{"\x1b[D", 1},
		{"\x1b[1D", 1},
		{"\x1b[12D", 12},
		{"\x1b[0D", 1},
		{"\x1b[xD", 1},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, csiCount([]byte(tc.in)), "%q", tc.in)
	}
}

func TestPromptAppendCSI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n     int
		final byte
		want  string
	}{
		{1, 'D', "\x1b[D"},
		{2, 'C', "\x1b[2C"},
		{120, 'D', "\x1b[120D"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, string(appendCSI([]byte("x"), tc.n, tc.final)[1:]))
	}
}

func TestPromptVisualLength(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"$ ", 2},
		{"\x1b[1;31m$\x1b[0m ", 2},
		{"日本 ", 3},
		{"memory:///docs $ ", 17},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, visualLength(tc.in), "%q", tc.in)
	}
}

func TestPromptEditorLineLengthFollowsTheEditor(t *testing.T) {
	t.Parallel()
	_, ok := editorLineLength(nil)
	assert.False(t, ok)

	s, p := newPromptSession(t)
	r := &promptReader{s: s, out: promptOutput{promptWidth: 2}, markedPromptEnd: true}
	r.out.resize(20)
	r.t = term.NewTerminal(r, "$ ")
	n, ok := editorLineLength(r.t)
	require.True(t, ok, "x/term's Terminal no longer has a line field: erasures will be drawn as spaces")
	assert.Equal(t, 0, n)

	_, err := p.writeMaster([]byte("abc\x7f\r"))
	require.NoError(t, err)
	line, err := r.t.ReadLine()
	require.NoError(t, err)
	assert.Equal(t, "ab", line)
	assert.Equal(t, "$ "+"abc"+"\x1b[D\x1b[K"+"\r\n", drainMaster(p),
		"the backspace clears the cell instead of overwriting it")
}

func TestPromptString(t *testing.T) {
	t.Parallel()
	newShell := func(t *testing.T, env ...string) *session {
		t.Helper()
		runner, err := interp.New(interp.Env(expand.ListEnviron(env...)))
		require.NoError(t, err)
		runner.Dir = "/docs/sub"
		return &session{e: &Executor{cfg: Config{Scheme: "memory"}}, runner: runner}
	}
	run := func(t *testing.T, s *session, src string) {
		t.Helper()
		f, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		require.NoError(t, err)
		require.NoError(t, s.runner.Run(context.Background(), f))
	}

	tests := []struct {
		name   string
		env    []string
		run    string
		status int
		want   string
	}{
		{name: "without PS1 the prompt names the scheme and directory", want: "memory:///docs/sub $ "},
		{name: "an unset PS1 is the default", env: []string{"PS1=$ "}, run: "unset PS1", want: "memory:///docs/sub $ "},
		{name: "an empty PS1 is no prompt", env: []string{"PS1="}, want: ""},
		{name: "a literal PS1", env: []string{"PS1=$ "}, want: "$ "},
		{name: "variables expand", env: []string{"PS1=[$USER] ", "USER=rune"}, want: "[rune] "},
		{name: "braced variables expand", env: []string{"PS1=${USER}@vsh ", "USER=rune"}, want: "rune@vsh "},
		{name: "an unset variable is empty", env: []string{"PS1=[$NOPE] "}, want: "[] "},
		{name: "a default for an unset variable", env: []string{"PS1=${NOPE:-none} "}, want: "none "},
		{name: "parameter expansion", env: []string{"PS1=${PWD##*/} > ", "PWD=/docs/sub"}, want: "sub > "},
		{name: "arithmetic expands", env: []string{"PS1=$((6*7))> "}, want: "42> "},
		{name: "the last status expands", env: []string{"PS1=$? $ "}, status: 130, want: "130 $ "},
		{name: "a zero status expands", env: []string{"PS1=[$?] "}, want: "[0] "},
		{name: "quotes are literal", env: []string{`PS1='a' "b" `}, want: `'a' "b" `},
		{name: "bash prompt escapes are not interpreted", env: []string{`PS1=\w \$ `}, want: `\w \$ `},
		{name: "escape sequences pass through", env: []string{"PS1=\x1b[1m$\x1b[0m "}, want: "\x1b[1m$\x1b[0m "},
		{name: "command substitution is not run", env: []string{"PS1=$(echo hi)> "}, want: "$(echo hi)> "},
		{name: "backquote substitution is not run", env: []string{"PS1=`echo hi`> "}, want: "`echo hi`> "},
		{name: "a value set by a command wins over the environment",
			env: []string{"PS1=$ "}, run: "PS1='changed> '", want: "changed> "},
		{name: "a variable set by a command expands", env: []string{"PS1=$X> "}, run: "X=1", want: "1> "},
		{name: "an unparsable PS1 is used verbatim", env: []string{"PS1=${ "}, want: "${ "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newShell(t, tc.env...)
			run(t, s, tc.run)
			assert.Equal(t, tc.want, s.promptString(false, tc.status))
			assert.Equal(t, "> ", s.promptString(true, tc.status), "a continuation line ignores PS1")
		})
	}
}

func TestPromptHistory(t *testing.T) {
	t.Parallel()
	many := make([]string, historySize+10)
	for i := range many {
		many[i] = strings.Repeat("x", i+1)
	}
	tests := []struct {
		name string
		adds []string
		// want lists the entries newest first, as At indexes them.
		want []string
	}{
		{name: "empty"},
		{name: "blank lines are skipped", adds: []string{"", "   ", "\t"}},
		{name: "entries are newest first", adds: []string{"one", "two"}, want: []string{"two", "one"}},
		{name: "an immediate repeat is skipped", adds: []string{"one", "one", "two", "two"}, want: []string{"two", "one"}},
		{name: "an earlier repeat is kept", adds: []string{"one", "two", "one"}, want: []string{"one", "two", "one"}},
		{name: "surrounding spaces make a distinct entry", adds: []string{"one", " one"}, want: []string{" one", "one"}},
		{name: "the oldest entries are dropped past the bound", adds: many,
			want: func() []string {
				var w []string
				for i := len(many) - 1; i >= 10; i-- {
					w = append(w, many[i])
				}
				return w
			}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &history{}
			for _, a := range tc.adds {
				h.Add(a)
			}
			var got []string
			for i := range h.Len() {
				got = append(got, h.At(i))
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPromptHistoryIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	h := &history{}
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			for i := range 100 {
				h.Add(strings.Repeat(string(rune('a'+w)), i%5+1))
				if n := h.Len(); n > 0 {
					_ = h.At(0)
				}
			}
		})
	}
	wg.Wait()
	assert.LessOrEqual(t, h.Len(), 400)
	assert.Positive(t, h.Len())
}

var _ term.History = (*history)(nil)
