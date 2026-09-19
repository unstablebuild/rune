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

//go:build e2e

package vte

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/vte/vtetest"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// this is the timeout to wait for the shell to stop updating the
// internal state of the vte, to call a test case "complete", so
// assertions can run. The slower the host of the tests, the longer
// this timeout should be.
var defaultWaitForIdleVte = 100 * time.Millisecond

func init() {
	if os.Getenv("CI") == "true" {
		defaultWaitForIdleVte = 150 * time.Millisecond
	}
}

// TestMain isolates HOME for the entire package's child processes so
// vim invocations in integration tests (e.g. TestHandlerIntegration)
// write their .viminfo and .viminf[a-z].tmp lock files into a
// throwaway directory. A real $HOME left over from prior crashes can
// already carry the full a-z spinner of viminf*.tmp files, which
// triggers E929 (too many viminfo temp files) and prevents the
// editor banner from appearing at all — the test then fails on a
// golden screen mismatch rather than a vim error.
//
// PS1 is pinned here for the same reason, once, for the whole
// process: tests run t.Parallel(), and per-test os.Setenv("PS1", ...)
// plus a t.Cleanup restore races against sibling tests spawning their
// own shell — a cleanup can zero PS1 between another test's Setenv
// and its shell fork, so that shell starts with no prompt at all.
// Setting it once before any test forks a shell removes the shared
// mutable write entirely rather than trying to sequence it per test.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "vte-home")
	if err != nil {
		panic(err)
	}
	prev, hadPrev := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", tmp); err != nil {
		panic(err)
	}
	if err := os.Setenv("PS1", "$ "); err != nil {
		panic(err)
	}
	code := m.Run()
	if hadPrev {
		_ = os.Setenv("HOME", prev)
	} else {
		_ = os.Unsetenv("HOME")
	}
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

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
		{"vi>ihello",
			`hello▐              
~                   
~                   
~                   
~                   
~                   
~                   
~                   
~                   
-- INSERT --        `},
		{"<:quit!>",
			`$ echo bla          
bla                 
$ vi                
$ ▐                 
                    
                    
                    
                    
                    
                    `},
	}

	cfg := DefaultConfig()

	// vi needs quite a bit of tiem to exit
	waitForIdleVte := defaultWaitForIdleVte * 4

	testSequence(t, cfg, waitForIdleVte, cases)
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

	os.Setenv("MYENV", "hello")
	defer os.Setenv("MYENV", "")
	cfg := DefaultConfig()

	waitForIdleVte := defaultWaitForIdleVte * 4
	testSequence(t, cfg, waitForIdleVte, cases)
}

func TestHandlerCloseExit(t *testing.T) {
	t.Parallel()
	cases := []vtetest.Case{
		{"exit>",
			`$ exit              
exit                
                    
                    
                    
                    
                    
                    
                    
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

// TestHandlerPublishesEventOnPtyExit pins the auto-close behaviour
// browser.Tab.Handle relies on: when the underlying pty child dies
// (e.g. the user types :q in an embedded vim, or exit in a shell),
// the host event loop must receive at least one event so it routes a
// Handle call to the tab. Without the wake-up the dead vte sits
// black until the user presses another key.
func TestHandlerPublishesEventOnPtyExit(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	uri, err := workspaceapi.CurrentUserHostURI(os.TempDir())
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { scheme.Close() })

	pub := &exitWakePublisher{}
	cfg := DefaultConfig()
	cfg.WidthHint = 20
	cfg.HeightHint = 10
	cfg.CommandAndArgs = []string{"sh"}
	handler, err := NewHandler(pub, nopNotifications{}, scheme, scheme, nopTabManager{}, cfg)
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

// TestHandlerMouseSelection drives press/drag/release sequences over
// the vte handler and asserts the resulting Selection() contents. The
// leftward and same-cell cases pin the user-reported bug where the
// first (leftmost) cell of a leftward drag was excluded from the
// selection.
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
			desc:   "press and drag on same cell selects that cell",
			events: []mev{left(0, helloRow), left(0, helloRow), rel(0, helloRow)},
			want:   "h",
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
			{"echo bla>echo bla><",
				`$ echo bla          
bla                 
$ echo bla          
bla                 
$ ▐                 
                    
                    
                    
                    
                    `},
		}
		cfg := DefaultConfig()
		cfg.Modal = true
		handler, ch := testSequence(t, cfg, defaultWaitForIdleVte, cases)

		handler.ClearPrimaryBuffer()
		time.Sleep(defaultWaitForIdleVte)

		cases = []vtetest.Case{
			{"echo XXX><kv0yjP",
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

func testSequence(t *testing.T, cfg Config, timeout time.Duration, cases []vtetest.Case) (
	*Handler, chan struct{},
) {
	shell := "sh" // all systems were this runs should have sh
	return testSequenceShell(t, cfg, timeout, shell, cases)
}

func testSequenceShell(t *testing.T, cfg Config, timeout time.Duration, shell string, cases []vtetest.Case) (
	*Handler, chan struct{},
) {
	return testSequenceCommand(t, cfg, timeout, []string{shell}, cases)
}

func testSequenceCommand(
	t *testing.T, cfg Config, timeout time.Duration, commandAndArgs []string, cases []vtetest.Case,
) (*Handler, chan struct{}) {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(context.Background())
	temp := os.TempDir()

	uri, err := workspaceapi.CurrentUserHostURI(temp)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)

	ch := make(chan struct{}, 50 /* big enough for the max length sequence of events */)
	cfg.WidthHint = 20
	cfg.HeightHint = 10
	cfg.CommandAndArgs = commandAndArgs
	handler, err := NewHandler(chanEventPublisher{ch}, nopNotifications{},
		scheme, scheme, nopTabManager{}, cfg)
	require.NoError(t, err)

	if ci := os.Getenv("CI"); ci == "true" {
		// the version of sh running on the CI docker containers
		// doesn't support bell (neither ctrl+g or ctrl+a + <-)
		t.SkipNow()
	}

	t.Cleanup(func() {
		handler.Close()
		scheme.Close()
		cancel()
	})

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

type nopNotifications struct {
}

func (nopNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return "", nil
}

func (nopNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return "", nil
}

func (n nopNotifications) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
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

type recordingNotifications struct {
	nopNotifications
	mu       sync.Mutex
	messages []string
}

func (n *recordingNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, fmt.Sprintf(msg, args...))
	return "", nil
}

func (n *recordingNotifications) Messages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.messages)
}

func newHandleTestHandler(master workspaceapi.File) *Handler {
	handler := &Handler{
		comp: &Component{
			pty:           workspaceapi.Pty{Master: master},
			parserHandler: &parserHandler{},
		},
		ctx:           context.Background(),
		notifications: nopNotifications{},
	}
	handler.comp.scroll.InitPerformance(cell.NewBuffer())
	handler.comp.scroll.InvertOffset = true
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

// TestHandlerHandleReturnsHandledWithoutPtyEcho pins the regression
// where a slow pty round-trip (e.g. an SSH workspace pty whose output
// arrives via workspacerpc) caused vte.Handler.Handle to return
// handled=false even though the keypress had already been written to
// the pty. The IDE sequencer would then treat the unhandled key as a
// candidate for sequence matching ("g" is a prefix of "gg"/"gf") and
// re-issue it on timeout, surfacing duplicated input ("g" -> "gg").
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

// TestHandlerPublishesPtyOutputInterrupt pins that a keystroke whose
// echo drives the embedded program to flush produces at least one
// EventInterrupt so the GUI repaints. Pacing was removed in favour of
// publishing directly and letting the event loop fold repaints per
// tick, so the contract is delivery, not coalescing.
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

// TestHandlerCtrlCInterruptsForegroundProgram pins the regression where
// pressing ctrl-c on a foreground program running in the primary buffer
// (e.g. a blocking `sleep`) failed to interrupt it. The handler must
// write the raw ETX byte (0x03) to the pty so the kernel line
// discipline delivers SIGINT to the foreground process group, returning
// control to the shell prompt.
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
