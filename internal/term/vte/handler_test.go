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

package vte

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/vshtest"
	"unstable.build/rune/internal/term/vte/vtetest"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// this is the timeout to wait for the shell to stop updating the
// internal state of the vte, to call a test case "complete", so
// assertions can run. The slower the host of the tests, the longer
// this timeout should be.
var defaultWaitForIdleVte = 100 * time.Millisecond

func TestHandlerIntegration(t *testing.T) {
	t.Parallel()
	cases := []vtetest.Case{
		{"",
			`$ ▐                 
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"ls",
			`$ ls▐               
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"^^echo bla>",
			`$ echo bla          
bla                 
$ ▐                 
                    
                    
                    
                    
                    
                    
                    `},
	}

	cfg := DefaultConfig()
	testSequence(t, cfg, defaultWaitForIdleVte, cases)
}

func TestHandlerEnvIntegration(t *testing.T) {
	t.Parallel()
	cases := []vtetest.Case{
		{"",
			`$ ▐                 
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		{"echo \\$MYENV>",
			`$ echo $MYENV       
hello               
$ ▐                 
                    
                    
                    
                    
                    
                    
                    `},
	}

	cfg := DefaultConfig()
	testSequenceVsh(t, cfg, defaultWaitForIdleVte,
		vshtest.Options{Env: []string{"MYENV=hello"}}, []string{"sh"}, cases)
}

func TestHandlerCloseExit(t *testing.T) {
	t.Parallel()
	cases := []vtetest.Case{
		{"exit>",
			`$ exit              
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
	}

	cfg := DefaultConfig()

	handler, _ := testSequence(t, cfg, defaultWaitForIdleVte, cases)
	exit, handled := handler.Handle(term.Event{})
	require.True(t, exit)
	assert.False(t, handled)

	_, _, show := handler.Cursor()
	assert.False(t, show)
}

func TestIsNormalPtyExit(t *testing.T) {
	t.Parallel()

	tsuite := []struct {
		desc     string
		err      error
		expected bool
	}{
		{"macOS reports EOF once the child exits", io.EOF, true},
		{"Close cancels the read loop", context.Canceled, true},
		{"Linux fails the master read with EIO", syscall.EIO, true},
		{"a wrapped EIO is still a clean exit",
			fmt.Errorf("read /dev/ptmx: %w", syscall.EIO), true},
		{"a remote EIO arrives untyped over the RPC boundary",
			errors.New("rpc error: code = Unknown desc = read error: " +
				"read /dev/ptmx: input/output error"), true},
		{"a genuine transport failure is still reported",
			errors.New("rpc error: code = Unavailable desc = " +
				"invalid file descriptor"), false},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			assert.Equal(t, tcase.expected, isNormalPtyExit(tcase.err))
		})
	}
}

func TestHandlerPublishesEventOnPtyExit(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{})

	pub := &exitWakePublisher{}
	cfg := DefaultConfig()
	cfg.WidthHint = 20
	cfg.HeightHint = 10
	cfg.CommandAndArgs = []string{"sh"}
	handler, err := NewHandler(pub, nopNotifications{}, f.Exec, f.Exec, nopTabManager{}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { handler.Close() })

	handler.Resize(20, 10)

	for _, b := range []byte("exit\n") {
		handler.Handle(term.Event{Type: term.EventKey, Ch: rune(b), Raw: []byte{b}})
	}

	require.Eventually(t, func() bool {
		return pub.SawEventNone()
	}, 5*time.Second, 10*time.Millisecond,
		"vte.Handler must publish at least one event after the "+
			"pty child exits so the host event loop can route a "+
			"Handle call and trigger tab auto-close without "+
			"further user input")
}

func TestHandlerCallsTabExiterOnPtyExit(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{})

	tm := &tabExiterRecorder{}
	cfg := DefaultConfig()
	cfg.WidthHint = 20
	cfg.HeightHint = 10
	cfg.CommandAndArgs = []string{"sh"}

	handler, err := NewHandler(&exitWakePublisher{}, nopNotifications{}, f.Exec, f.Exec, tm, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { handler.Close() })

	handler.Resize(20, 10)

	for _, b := range []byte("exit\n") {
		handler.Handle(term.Event{Type: term.EventKey, Ch: rune(b), Raw: []byte{b}})
	}

	require.Eventually(t, func() bool {
		uris := tm.ExitedURIs()
		return len(uris) == 1 && uris[0].String() == handler.Component().URI().String()
	}, 5*time.Second, 10*time.Millisecond,
		"vte.Handler must ask its TabManager to drop it by URI when the pty child exits")
}

type exitWakePublisher struct {
	mu      sync.Mutex
	sawNone bool
}

func (p *exitWakePublisher) PublishEvent(ev term.Event) error {
	if ev.Type == term.EventNone {
		p.mu.Lock()
		p.sawNone = true
		p.mu.Unlock()
	}
	return nil
}

func (p *exitWakePublisher) SawEventNone() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sawNone
}

func TestHandlerMouseSelection(t *testing.T) {
	t.Parallel()

	// All cases echo "hello" so it lands on row 1. The buffer reports
	// Columns(1)=5, which clamps any to.X past column 5.
	const helloRow = 1
	type mev struct {
		key  term.Key
		x, y int
	}
	left := func(x, y int) mev { return mev{term.MouseLeft, x, y} }
	rel := func(x, y int) mev { return mev{term.MouseRelease, x, y} }

	cases := []struct {
		desc   string
		events []mev
		want   string
	}{
		{
			desc:   "rightward drag from col 0 to col 4",
			events: []mev{left(0, helloRow), left(4, helloRow), rel(4, helloRow)},
			want:   "hello",
		},
		{
			desc:   "leftward drag from col 5 to col 0 includes first cell",
			events: []mev{left(5, helloRow), left(0, helloRow), rel(0, helloRow)},
			want:   "hello",
		},
		{
			desc:   "press and jitter on the same cell selects nothing",
			events: []mev{left(0, helloRow), left(0, helloRow), rel(0, helloRow)},
		},
		{
			desc:   "press right, drag one cell left",
			events: []mev{left(4, helloRow), left(3, helloRow), rel(3, helloRow)},
			want:   "lo",
		},
		{
			desc:   "drag past row's last column clamps to Columns(y)",
			events: []mev{left(4, helloRow), left(5, helloRow), rel(5, helloRow)},
			want:   "o",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			handler, _ := testSequence(t, cfg, defaultWaitForIdleVte,
				[]vtetest.Case{{"echo hello>",
					`$ echo hello        
hello               
$ ▐                 
                    
                    
                    
                    
                    
                    
                    `}})
			for _, ev := range tc.events {
				handler.Handle(term.Event{
					Type: term.EventMouse, Key: ev.key,
					MouseX: ev.x, MouseY: ev.y,
				})
			}
			sel, ok := handler.Selection()
			if tc.want == "" {
				assert.False(t, ok, "unexpected selection %q", sel)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tc.want, sel)
		})
	}
}

func TestResetPrimaryBuffer(t *testing.T) {
	t.Parallel()
	t.Run("non modal", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			{"echo bla>echo bla>",
				`$ echo bla          
bla                 
$ echo bla          
bla                 
$ ▐                 
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		handler, ch := testSequence(t, cfg, defaultWaitForIdleVte, cases)

		handler.ClearPrimaryBuffer()
		time.Sleep(defaultWaitForIdleVte)

		cases = []vtetest.Case{
			{"echo XXX>",
				`                    
$ echo XXX          
XXX                 
$ ▐                 
                    
                    
                    
                    
                    
                    `},
		}

		vtetest.TestSequence(t, handler, 20, 10, defaultWaitForIdleVte, ch, cases)
	})

	t.Run("on modal mode", func(t *testing.T) {
		t.Parallel()
		cases := []vtetest.Case{
			// entering vi mode moves the cursor back a column, onto the
			// prompt: its start is only known once the shell answers
			{"echo bla>echo bla><",
				`$ echo bla          
bla                 
$ echo bla          
bla                 
$▐                  
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		handler, ch := testSequence(t, cfg, defaultWaitForIdleVte, cases)

		handler.ClearPrimaryBuffer()
		time.Sleep(defaultWaitForIdleVte)

		cases = []vtetest.Case{
			// l moves off the prompt's space, where entering left the cursor
			{"echo XXX><klv0yjP",
				`                    
$ echo XXX          
XXX                 
$ XX▐               
                    
                    
                    
                    
                    
                    `},
		}

		vtetest.TestSequence(t, handler, 20, 10, defaultWaitForIdleVte, ch, cases)
	})
}

func TestHandlerResizeViIntegration(t *testing.T) {
	t.Parallel()
	cases := []vtetest.Case{
		{"echo 'a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk'",
			`> b                 
> c                 
> d                 
> e                 
> f                 
> g                 
> h                 
> i                 
> j                 
> k'▐               `},
		{">",
			`c                   
d                   
e                   
f                   
g                   
h                   
i                   
j                   
k                   
$ ▐                 `},
	}

	cfg := DefaultConfig()
	cfg.Modal = true
	handler, ch := testSequence(t, cfg, defaultWaitForIdleVte, cases)

	// test same width/height resize, which simulates window manager
	// calling Resize on every children after a window re-configuration.
	handler.Resize(20, 10)

	cases = []vtetest.Case{
		{"",
			`c                   
d                   
e                   
f                   
g                   
h                   
i                   
j                   
k                   
$ ▐                 `},
	}

	vtetest.TestCases(t, handler, 20, 10, defaultWaitForIdleVte, ch, cases)
}

// testSequence plays cases on a 20x10 terminal running the Rune
// shell on an empty memory workspace, the hermetic stand-in for a
// shell in a pty.
func testSequence(t *testing.T, cfg Config, timeout time.Duration, cases []vtetest.Case) (
	*Handler, chan struct{},
) {
	return testSequenceVsh(t, cfg, timeout, vshtest.Options{}, []string{"sh"}, cases)
}

// testSequenceVsh plays cases on a terminal running commandAndArgs on
// the Rune shell's executor over a memory workspace built from opts.
func testSequenceVsh(
	t *testing.T, cfg Config, timeout time.Duration, opts vshtest.Options,
	commandAndArgs []string, cases []vtetest.Case,
) (*Handler, chan struct{}) {
	f := vshtest.New(t, opts)
	return newTestSequence(t, cfg, timeout, f.Exec, f.Exec, commandAndArgs, cases)
}

// newTestSequence builds a 20x10 Handler running commandAndArgs on
// terminal and executor, plays cases on it and returns it with the
// channel its events arrive on.
func newTestSequence(
	t *testing.T, cfg Config, timeout time.Duration,
	terminal schemeapi.Terminal, executor schemeapi.Executor,
	commandAndArgs []string, cases []vtetest.Case,
) (*Handler, chan struct{}) {
	return newTestSequenceTabs(t, cfg, timeout, terminal, executor, nopTabManager{}, commandAndArgs, cases)
}

// newTestSequenceTabs is newTestSequence with the tab manager the
// Handler reports exits and program statuses to.
func newTestSequenceTabs(
	t *testing.T, cfg Config, timeout time.Duration,
	terminal schemeapi.Terminal, executor schemeapi.Executor, tm browser.TabManager,
	commandAndArgs []string, cases []vtetest.Case,
) (*Handler, chan struct{}) {
	ch := make(chan struct{}, 50 /* big enough for the max length sequence of events */)
	cfg.WidthHint = 20
	cfg.HeightHint = 10
	cfg.CommandAndArgs = commandAndArgs
	handler, err := NewHandler(chanEventPublisher{ch}, nopNotifications{},
		terminal, executor, tm, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { handler.Close() })

	vtetest.TestSequence(t, handler, cfg.WidthHint, cfg.HeightHint,
		timeout, ch, cases)

	return handler, ch
}

type chanEventPublisher struct {
	ch chan struct{}
}

func (p chanEventPublisher) PublishEvent(term.Event) error {
	p.ch <- struct{}{}
	return nil
}

type recordingPtyFile struct {
	workspacetest.File
	err    error
	writes chan struct{}
	record bool
}

func (f *recordingPtyFile) Write(data []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if f.record {
		f.File.Writes = append(f.File.Writes, bytes.Clone(data))
	}
	if f.writes != nil {
		f.writes <- struct{}{}
	}
	return len(data), nil
}

func newHandleTestHandler(master workspaceapi.File) *Handler {
	ph := &parserHandler{
		keyboard: new(keyboardState),
		tm:       new(tabExiterRecorder),
	}
	handler := &Handler{
		comp: &Component{
			pty:           workspaceapi.Pty{Master: master},
			ptyIn:         master,
			parserHandler: ph,
			keyboard:      ph.keyboard,
		},
		ctx:           context.Background(),
		notifications: nopNotifications{},
	}
	handler.comp.scroll.InitPerformance(cell.NewBuffer())
	handler.comp.scroll.InvertOffset = true
	ph.sync.mu = &handler.comp.mu
	return handler
}

func TestHandlerHandleWritesBeforeReturning(t *testing.T) {
	t.Parallel()
	master := &recordingPtyFile{record: true}
	handler := newHandleTestHandler(master)

	exit, handled := handler.Handle(term.Event{
		Type: term.EventKey,
		Ch:   'a',
		Raw:  []byte("a"),
	})

	assert.False(t, exit)
	assert.True(t, handled)
	assert.Equal(t, [][]byte{[]byte("a")}, master.Writes)
}

func TestHandlerHandlePtyWriteError(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("write failed")
	master := &recordingPtyFile{err: writeErr}
	notifications := new(recordingNotifications)
	handler := newHandleTestHandler(master)
	handler.notifications = notifications

	exit, handled := handler.Handle(term.Event{
		Type: term.EventKey,
		Ch:   'a',
		Raw:  []byte("a"),
	})

	assert.False(t, exit)
	assert.False(t, handled)
	assert.Equal(t, []string{"write to pty: write failed"}, notifications.Messages())
}

func TestHandlerHandleInputPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		ev          term.Event
		configure   func(*Handler)
		expected    []byte
		expectWrite bool
		handled     bool
	}{
		{
			name:        "printable",
			ev:          term.Event{Type: term.EventKey, Ch: 'a', Raw: []byte("a")},
			expected:    []byte("a"),
			expectWrite: true,
			handled:     true,
		},
		{
			name:        "shifted",
			ev:          term.Event{Type: term.EventKey, Ch: 'A', Mod: term.ModShift, Raw: []byte("A")},
			expected:    []byte("A"),
			expectWrite: true,
			handled:     true,
		},
		{
			name:        "ctrl-c",
			ev:          term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl, Raw: []byte{0x03}},
			expected:    []byte{0x03},
			expectWrite: true,
			handled:     true,
		},
		{
			name: "application cursor",
			ev:   term.Event{Type: term.EventKey, Key: term.KeyArrowUp, Raw: []byte("\x1b[A")},
			configure: func(handler *Handler) {
				handler.comp.parserHandler.modeCursorKeys = true
			},
			expected:    []byte("\x1bOA"),
			expectWrite: true,
			handled:     true,
		},
		{
			name: "new line mode",
			ev:   term.Event{Type: term.EventKey, Key: term.KeyEnter, Raw: []byte("\r")},
			configure: func(handler *Handler) {
				handler.comp.parserHandler.modeLineFeedNewLine = true
			},
			expected:    []byte("\r\n"),
			expectWrite: true,
			handled:     true,
		},
		{
			name:    "unsupported modifier",
			ev:      term.Event{Type: term.EventKey, Ch: 'a', Mod: term.ModAlt, Raw: []byte("a")},
			handled: false,
		},
		{
			name:    "unknown modifier bit",
			ev:      term.Event{Type: term.EventKey, Ch: 'a', Mod: term.Modifier(1 << 7), Raw: []byte("a")},
			handled: false,
		},
		{
			name:    "empty input",
			ev:      term.Event{Type: term.EventKey},
			handled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			master := &recordingPtyFile{record: true}
			handler := newHandleTestHandler(master)
			if tc.configure != nil {
				tc.configure(handler)
			}

			exit, handled := handler.Handle(tc.ev)

			assert.False(t, exit)
			assert.Equal(t, tc.handled, handled)
			if tc.expectWrite {
				assert.Equal(t, [][]byte{tc.expected}, master.Writes)
			} else {
				assert.Empty(t, master.Writes)
			}
		})
	}
}

func BenchmarkHandlerHandlePrintableKey(b *testing.B) {
	writes := make(chan struct{}, 1)
	master := &recordingPtyFile{writes: writes}
	handler := newHandleTestHandler(master)
	ev := term.Event{Type: term.EventKey, Ch: 'a', Raw: []byte("a")}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		exit, handled := handler.Handle(ev)
		if exit || !handled {
			b.Fatalf("Handle returned exit=%t handled=%t", exit, handled)
		}
		<-writes
	}
}

func TestHandlerHandleReturnsHandledWithoutPtyEcho(t *testing.T) {
	t.Parallel()

	// Use a real shell only to keep parity with other handler tests:
	// what we exercise is the Handle return contract, not echo.
	cases := []vtetest.Case{
		{"",
			`$ ▐                 
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
	}
	cfg := DefaultConfig()
	handler, _ := testSequence(t, cfg, defaultWaitForIdleVte, cases)

	// Drive Handle directly with a key event and assert handled=true
	// returns without waiting for a pty update. The underlying contract:
	// writing to the pty is the moment the event is "consumed" by
	// vte.Handler.
	exit, handled := handler.Handle(term.Event{
		Type: term.EventKey,
		Ch:   'g',
		Raw:  []byte("g"),
	})
	assert.False(t, exit)
	assert.True(t, handled,
		"vte.Handler.Handle must return handled=true once the event "+
			"is written to the pty, regardless of whether the pty echoes it. "+
			"Otherwise, callers chaining into a key sequencer "+
			"(e.g. ide/ex) duplicate input on slow remote ptys.")
}

func TestHandlerHandleBurstDoesNotWaitForPtyEcho(t *testing.T) {
	t.Parallel()

	const (
		keys        = 4
		maxDuration = 100 * time.Millisecond
	)
	master := &recordingPtyFile{record: true}
	handler := newHandleTestHandler(master)

	start := time.Now()
	for range keys {
		exit, handled := handler.Handle(term.Event{
			Type: term.EventKey,
			Ch:   'a',
			Raw:  []byte("a"),
		})
		require.False(t, exit)
		require.True(t, handled)
	}
	require.Less(t, time.Since(start), maxDuration,
		"a burst of PTY writes must not block the UI event loop waiting for echo")
	require.Len(t, master.Writes, keys)
}

func TestHandlerPasteEndWritesBufferedInput(t *testing.T) {
	t.Parallel()

	master := &recordingPtyFile{record: true}
	handler := newHandleTestHandler(master)
	handler.comp.parserHandler.useAlt = true

	exit, handled := handler.Handle(term.Event{Type: term.EventPasteStart})
	assert.False(t, exit)
	assert.True(t, handled)
	assert.Empty(t, master.Writes)

	exit, handled = handler.Handle(term.Event{
		Type: term.EventKey,
		Ch:   's',
		Raw:  []byte("secret\n"),
	})
	assert.False(t, exit)
	assert.True(t, handled)
	assert.Empty(t, master.Writes)

	exit, handled = handler.Handle(term.Event{Type: term.EventPasteEnd})
	assert.False(t, exit)
	assert.True(t, handled)
	assert.Equal(t, [][]byte{[]byte("secret\r")}, master.Writes)
}

func TestHandlerPublishesPtyOutputInterrupt(t *testing.T) {
	t.Parallel()
	cases := []vtetest.Case{
		{"", `$ ▐                 
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
	}
	cfg := DefaultConfig()
	handler, ch := testSequence(t, cfg, defaultWaitForIdleVte, cases)

	drain(ch)
	exit, handled := handler.Handle(term.Event{
		Type: term.EventKey, Ch: 'a', Raw: []byte("a"),
	})
	assert.False(t, exit)
	assert.True(t, handled)
	time.Sleep(defaultWaitForIdleVte)

	assert.GreaterOrEqual(t, len(ch), 1,
		"a keystroke echo must publish at least one EventInterrupt so the GUI repaints")
}

func drain(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestHandlerCtrlCInterruptsForegroundProgram(t *testing.T) {
	t.Parallel()
	cases := []vtetest.Case{
		{"",
			`$ ▐                 
                    
                    
                    
                    
                    
                    
                    
                    
                    `},
		// start a foreground program that blocks; the shell prompt
		// must not return until the program is interrupted.
		{"sleep 30>",
			`$ sleep 30          
▐                   
                    
                    
                    
                    
                    
                    
                    
                    `},
	}
	cfg := DefaultConfig()
	handler, ch := testSequence(t, cfg, defaultWaitForIdleVte, cases)

	drain(ch)

	// ctrl-c with the raw ETX byte the gui input layer produces.
	exit, handled := handler.Handle(term.Event{
		Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c', Raw: []byte{0x03},
	})
	assert.False(t, exit)
	assert.True(t, handled)

	w := term.NewStringWriter(20, 10)
	require.Eventually(t, func() bool {
		require.NoError(t, w.Clear(term.Attributes{}))
		handler.Draw(w)
		require.NoError(t, w.Flush())
		// after the interrupt the shell must print a fresh prompt
		// below the interrupted command line.
		return strings.Count(w.String(), "$ ") >= 2
	}, 5*time.Second, 20*time.Millisecond,
		"ctrl-c must interrupt the foreground sleep and return the "+
			"shell prompt; screen was:\n%s", w.String())
}

func TestHandlerAnswersQueriesInSynchronizedUpdate(t *testing.T) {
	t.Parallel()
	// crossterm's cursor::position does this inside ratatui's first
	// frame, and gives up after 2s without a reply.
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{
		"sync_query.sh": "printf '\\033[?2026h\\033[6n'\nsleep 60\n",
	}})
	rec := &recordingTerminal{Executor: f.Exec}
	newTestSequence(t, DefaultConfig(), defaultWaitForIdleVte, rec, f.Exec,
		[]string{"sh", "/sync_query.sh"}, nil)

	require.Eventually(t, func() bool {
		return strings.Contains(rec.written(), "\x1b[1;1R")
	}, 5*time.Second, 20*time.Millisecond,
		"the cursor position report must not wait for the synchronized update to end; the program read %q", rec.written())
}

// recordingTerminal serves the Executor's ptys with a master that
// records what the terminal writes to the program.
type recordingTerminal struct {
	*vsh.Executor
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *recordingTerminal) NewPty(ctx context.Context) (workspaceapi.Pty, error) {
	pt, err := r.Executor.NewPty(ctx)
	if err != nil {
		return pt, err
	}
	pt.Master = &recordingMaster{File: pt.Master, rec: r}
	return pt, nil
}

func (r *recordingTerminal) SetPtySize(pt workspaceapi.Pty, size workspaceapi.PtySize) error {
	if m, ok := pt.Master.(*recordingMaster); ok {
		pt.Master = m.File
	}
	return r.Executor.SetPtySize(pt, size)
}

func (r *recordingTerminal) written() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

type recordingMaster struct {
	workspaceapi.File
	rec *recordingTerminal
}

func (m *recordingMaster) Write(b []byte) (int, error) {
	m.rec.mu.Lock()
	m.rec.buf.Write(b)
	m.rec.mu.Unlock()
	return m.File.Write(b)
}

// blank is an empty 20-column screen row.
const blank = "                    "

// screen joins rows into a 20x10 screen, padding each row to 20
// columns and the screen to 10 rows.
func screen(rows ...string) string {
	var b strings.Builder
	for i := range 10 {
		if i > 0 {
			b.WriteByte('\n')
		}
		row := blank
		if i < len(rows) {
			row = rows[i]
		}
		b.WriteString(row)
		b.WriteString(blank[:max(0, 20-len([]rune(row)))])
	}
	return b.String()
}

func TestHandlerVshPrompt(t *testing.T) {
	t.Parallel()
	t.Run("ctrl-l clears the screen and redraws the line", func(t *testing.T) {
		t.Parallel()
		testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "echo a>echo b", Expected: screen("$ echo a", "a", "$ echo b▐")},
			{InputSequence: "$", Expected: screen("$ echo b▐")},
			{InputSequence: ">", Expected: screen("$ echo b", "b", "$ ▐")},
		})
	})
	t.Run("a long line wraps and the cursor follows", func(t *testing.T) {
		t.Parallel()
		as := strings.Repeat("a", 30)
		testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "echo " + as, Expected: screen("$ echo "+as[:13], as[13:]+"▐")},
			{InputSequence: "^^^^^^^^^^^", Expected: screen("$ echo "+as[:13], as[13:19]+"▐")},
			{InputSequence: ">", Expected: screen("$ echo "+as[:13], as[13:19], as[:19], "$ ▐")},
		})
	})
	t.Run("a line that fills the row wraps without a blank line", func(t *testing.T) {
		t.Parallel()
		testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "echo 0123456789abc", Expected: screen("$ echo 0123456789ab▐")},
			{InputSequence: ">", Expected: screen("$ echo 0123456789abc", "0123456789abc", "$ ▐")},
		})
	})
}

func TestHandlerVshCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		opts  vshtest.Options
		cases []vtetest.Case
	}{
		{name: "ls lists the seeded files",
			opts: vshtest.Options{Files: map[string]string{"src/a.txt": "", "src/b.txt": ""}},
			cases: []vtetest.Case{
				{InputSequence: "ls src>", Expected: screen("$ ls src", "a.txt  b.txt", "$ ▐")},
			}},
		{name: "a pipeline",
			opts: vshtest.Options{Files: map[string]string{"src/a.txt": "", "src/b.txt": ""}},
			cases: []vtetest.Case{
				{InputSequence: "ls src | wc -l>", Expected: screen("$ ls src | wc -l", "2", "$ ▐")},
			}},
		{name: "redirections write workspace files",
			cases: []vtetest.Case{
				{InputSequence: "echo one \\> f>echo two \\>\\> f>cat f>",
					Expected: screen("$ echo one > f", "$ echo two >> f", "$ cat f", "one", "two", "$ ▐")},
			}},
		{name: "cd changes the directory PS1 shows",
			opts: vshtest.Options{Files: map[string]string{"src/a.txt": ""}, Env: []string{"PS1=$PWD $ "}},
			cases: []vtetest.Case{
				{InputSequence: "", Expected: screen("/ $ ▐")},
				{InputSequence: "cd src>", Expected: screen("/ $ cd src", "/src $ ▐")},
				{InputSequence: "cd>", Expected: screen("/ $ cd src", "/src $ cd", "/ $ ▐")},
			}},
		{name: "variables and functions persist across lines",
			cases: []vtetest.Case{
				{InputSequence: "x=5>f() { echo f:\\$x; }>f>",
					Expected: screen("$ x=5", "$ f() { echo f:$x; }", "$ f", "f:5", "$ ▐")},
			}},
		{name: "a for loop",
			cases: []vtetest.Case{
				{InputSequence: "for i in 1 2; do echo \\$i; done>",
					Expected: screen("$ for i in 1 2; do e", "cho $i; done", "1", "2", "$ ▐")},
			}},
		{name: "the continuation prompt",
			cases: []vtetest.Case{
				{InputSequence: "for i in 1 2; do>", Expected: screen("$ for i in 1 2; do", "> ▐")},
				{InputSequence: "echo \\$i>done>",
					Expected: screen("$ for i in 1 2; do", "> echo $i", "> done", "1", "2", "$ ▐")},
			}},
		{name: "a syntax error is reported and the shell recovers",
			cases: []vtetest.Case{
				{InputSequence: "echo )>echo ok>",
					Expected: screen("$ echo )", "sh: 1:6: a command c", "an only contain word", "s and redirects; enc",
						"ountered `)`", "$ echo ok", "ok", "$ ▐")},
			}},
		{name: "an unknown command exits 127",
			cases: []vtetest.Case{
				{InputSequence: "nope>echo \\$?>",
					Expected: screen("$ nope", "sh: nope: command no", "t found", "$ echo $?", "127", "$ ▐")},
			}},
		{name: "help names the shell",
			cases: []vtetest.Case{
				{InputSequence: "help | head -1>", Expected: screen("$ help | head -1", "Rune built-in shell", "$ ▐")},
			}},
		{name: "Tab completes a path",
			opts: vshtest.Options{Files: map[string]string{"src/a.txt": "A\n"}},
			cases: []vtetest.Case{
				{InputSequence: "cat s✌", Expected: screen("$ cat src/▐")},
				{InputSequence: "a✌", Expected: screen("$ cat src/a.txt ▐")},
				{InputSequence: ">", Expected: screen("$ cat src/a.txt", "A", "$ ▐")},
			}},
		{name: "Tab lists what it cannot choose between",
			opts: vshtest.Options{Files: map[string]string{"src/a.txt": "", "src/b.txt": ""}},
			cases: []vtetest.Case{
				{InputSequence: "ls src/✌", Expected: screen("$ ls src/", "a.txt  b.txt", "$ ls src/▐")},
				{InputSequence: "b✌>", Expected: screen("$ ls src/", "a.txt  b.txt", "$ ls src/b.txt", "src/b.txt", "$ ▐")},
			}},
		{name: "Tab completes a command name",
			cases: []vtetest.Case{
				{InputSequence: "ech✌hi>", Expected: screen("$ echo hi", "hi", "$ ▐")},
			}},
		{name: "Tab escapes a name with a space",
			opts: vshtest.Options{Files: map[string]string{"my notes.md": "N\n"}},
			cases: []vtetest.Case{
				{InputSequence: "cat m✌>", Expected: screen("$ cat my\\ notes.md", "N", "$ ▐")},
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testSequenceVsh(t, DefaultConfig(), defaultWaitForIdleVte, tc.opts, []string{"sh"}, tc.cases)
		})
	}
}

// ctrlD is the EOF key as the GUI input layer produces it.
var ctrlD = term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'd', Raw: []byte{0x04}}

func TestHandlerVshInputToPrograms(t *testing.T) {
	t.Parallel()
	t.Run("cat echoes cooked lines until ^D", func(t *testing.T) {
		t.Parallel()
		handler, ch := testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "cat>hel^llo>", Expected: screen("$ cat", "hello", "hello", "▐")},
		})
		_, handled := handler.Handle(ctrlD)
		require.True(t, handled)
		vtetest.TestCases(t, handler, 20, 10, defaultWaitForIdleVte, ch, []vtetest.Case{
			{InputSequence: "", Expected: screen("$ cat", "hello", "hello", "$ ▐")},
		})
	})
	t.Run("read takes a line", func(t *testing.T) {
		t.Parallel()
		testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "read x>value>echo got \\$x>",
				Expected: screen("$ read x", "value", "$ echo got $x", "got value", "$ ▐")},
		})
	})
}

// ctrlC is the interrupt key with the raw ETX byte the GUI input layer
// produces.
var ctrlC = term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c', Raw: []byte{0x03}}

func TestHandlerVshInterrupt(t *testing.T) {
	t.Parallel()
	t.Run("^C at the prompt drops the line", func(t *testing.T) {
		t.Parallel()
		handler, ch := testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "echo abc", Expected: screen("$ echo abc▐")},
		})
		_, handled := handler.Handle(ctrlC)
		require.True(t, handled)
		vtetest.TestCases(t, handler, 20, 10, defaultWaitForIdleVte, ch, []vtetest.Case{
			{InputSequence: "", Expected: screen("$ echo abc^C", "$ ▐")},
			{InputSequence: "echo \\$?>", Expected: screen("$ echo abc^C", "$ echo $?", "130", "$ ▐")},
		})
	})
	t.Run("^C during sleep prints ^C and a new prompt", func(t *testing.T) {
		t.Parallel()
		handler, ch := testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "sleep 30>", Expected: screen("$ sleep 30", "▐")},
		})
		_, handled := handler.Handle(ctrlC)
		require.True(t, handled)
		vtetest.TestCases(t, handler, 20, 10, defaultWaitForIdleVte, ch, []vtetest.Case{
			{InputSequence: "", Expected: screen("$ sleep 30", "^C", "$ ▐")},
			{InputSequence: "echo \\$?>", Expected: screen("$ sleep 30", "^C", "$ echo $?", "130", "$ ▐")},
		})
	})
	t.Run("typeahead during sleep runs after it", func(t *testing.T) {
		t.Parallel()
		testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "sleep 1>echo x>", Expected: screen("$ sleep 1", "echo x", "$ echo x", "x", "$ ▐")},
		})
	})
}

func TestHandlerVshExit(t *testing.T) {
	t.Parallel()
	t.Run("^D exits the shell", func(t *testing.T) {
		t.Parallel()
		f := vshtest.New(t, vshtest.Options{})
		tm := &tabExiterRecorder{}
		pub := &exitWakePublisher{}
		cfg := DefaultConfig()
		cfg.WidthHint, cfg.HeightHint = 20, 10
		cfg.CommandAndArgs = []string{"sh"}
		handler, err := NewHandler(pub, nopNotifications{}, f.Exec, f.Exec, tm, cfg)
		require.NoError(t, err)
		t.Cleanup(func() { handler.Close() })
		handler.Resize(20, 10)

		_, handled := handler.Handle(ctrlD)
		require.True(t, handled)
		require.Eventually(t, func() bool { return len(tm.ExitedURIs()) == 1 && pub.SawEventNone() },
			5*time.Second, 10*time.Millisecond)
		assert.NoError(t, handler.ExitErr())
		exit, _ := handler.Handle(term.Event{})
		assert.True(t, exit)

		w := term.NewStringWriter(20, 10)
		handler.Draw(w)
		require.NoError(t, w.Flush())
		assert.Equal(t, screen("$ exit"), w.String(), "^D echoes exit like bash")
	})
	t.Run("the exit status is reported", func(t *testing.T) {
		t.Parallel()
		handler, _ := testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "exit 3>", Expected: screen("$ exit 3")},
		})
		var exitErr *vsh.ExitError
		require.Eventually(t, func() bool {
			exit, _ := handler.Handle(term.Event{})
			return exit
		}, 5*time.Second, 10*time.Millisecond)
		require.ErrorAs(t, handler.ExitErr(), &exitErr)
		assert.Equal(t, 3, exitErr.ExitCode())
	})
}

func TestHandlerVshResize(t *testing.T) {
	t.Parallel()
	t.Run("a resize at the prompt keeps the line", func(t *testing.T) {
		t.Parallel()
		handler, ch := testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "echo a>echo bc", Expected: screen("$ echo a", "a", "$ echo bc▐")},
		})
		handler.Resize(10, 5)
		vtetest.TestCases(t, handler, 10, 5, defaultWaitForIdleVte, ch, []vtetest.Case{
			{InputSequence: ">", Expected: "$ echo a  \na         \n$ echo bc \nbc        \n$ ▐       "},
		})
	})
	t.Run("commands lay out in the new width", func(t *testing.T) {
		t.Parallel()
		handler, ch := testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
			{InputSequence: "", Expected: screen("$ ▐")},
		})
		handler.Resize(40, 10)
		vtetest.TestCases(t, handler, 40, 10, defaultWaitForIdleVte, ch, []vtetest.Case{
			{InputSequence: "ls /bin>", Expected: "[         env       mv        test      \n" +
				"basename  false     printf    touch     \n" +
				"bash      fish      pwd       true      \n" +
				"cat       grep      rm        wc        \n" +
				"clear     head      rmdir     which     \n" +
				"cp        help      sh        zsh       \n" +
				"dash      ksh       sleep               \n" +
				"dirname   ls        tail                \n" +
				"echo      mkdir     tee                 \n" +
				"$ ▐                                     "},
		})
	})
}

func TestHandlerVshScrollback(t *testing.T) {
	t.Parallel()
	var lines strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&lines, "line%d\n", i)
	}
	handler, _ := testSequenceVsh(t, DefaultConfig(), defaultWaitForIdleVte,
		vshtest.Options{Files: map[string]string{"lines": lines.String()}}, []string{"sh"}, []vtetest.Case{
			{InputSequence: "cat lines>", Expected: screen("line22", "line23", "line24", "line25", "line26",
				"line27", "line28", "line29", "line30", "$ ▐")},
		})

	require.True(t, handler.Component().ScrollUp(5))
	w := term.NewStringWriter(20, 10)
	handler.Draw(w)
	require.NoError(t, w.Flush())
	assert.Equal(t, screen("line17", "line18", "line19", "line20", "line21", "line22", "line23", "line24",
		"line25", "line26"), w.String())

	for _, ev := range []term.Event{
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 0, MouseY: 2},
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 5, MouseY: 2},
		{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 5, MouseY: 2},
	} {
		handler.Handle(ev)
	}
	sel, ok := handler.Selection()
	require.True(t, ok)
	assert.Equal(t, "line19", sel)
}

// statusRecorder records the program statuses a Handler reports.
type statusRecorder struct {
	nopTabManager
	mu      sync.Mutex
	reports []programstatus.Report
	ended   int
}

func (r *statusRecorder) SetTabStatus(_ workspaceapi.URI, report programstatus.Report) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, report)
	return nil
}

func (r *statusRecorder) EndTabStatus(workspaceapi.URI) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended++
	return nil
}

func (r *statusRecorder) snapshot() ([]programstatus.Report, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.reports), r.ended
}

func TestHandlerVshShellIntegration(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{
		"work.sh": "printf '\\033]7501;state=working\\033\\\\'\nsleep 60\n",
	}})
	tm := &statusRecorder{}
	// A program reports it is working; the prompt that follows its
	// end withdraws the report, and neither sequence takes a cell.
	handler, ch := newTestSequenceTabs(t, DefaultConfig(), defaultWaitForIdleVte, f.Exec, f.Exec, tm,
		[]string{"sh"}, []vtetest.Case{
			{InputSequence: "sh work.sh>", Expected: screen("$ sh work.sh", "▐")},
		})
	require.Eventually(t, func() bool {
		reports, _ := tm.snapshot()
		return len(reports) == 1 && reports[0].State == programstatus.Working
	}, 5*time.Second, 10*time.Millisecond)

	_, handled := handler.Handle(ctrlC)
	require.True(t, handled)
	vtetest.TestCases(t, handler, 20, 10, defaultWaitForIdleVte, ch, []vtetest.Case{
		{InputSequence: "", Expected: screen("$ sh work.sh", "^C", "$ ▐")},
	})
	require.Eventually(t, func() bool {
		_, ended := tm.snapshot()
		return ended == 1
	}, 5*time.Second, 10*time.Millisecond, "the next prompt ends the status")
}

func TestHandlerVshPasteRunsLineByLine(t *testing.T) {
	t.Parallel()
	handler, ch := testSequence(t, DefaultConfig(), defaultWaitForIdleVte, []vtetest.Case{
		{InputSequence: "", Expected: screen("$ ▐")},
	})
	handler.Handle(term.Event{Type: term.EventPasteStart})
	handler.Handle(term.Event{Type: term.EventKey, Ch: 'e', Raw: []byte("echo a\necho b\n")})
	handler.Handle(term.Event{Type: term.EventPasteEnd})
	vtetest.TestCases(t, handler, 20, 10, defaultWaitForIdleVte, ch, []vtetest.Case{
		{InputSequence: "", Expected: screen("$ echo a", "echo b", "a", "$ echo b", "b", "$ ▐")},
	})
}
