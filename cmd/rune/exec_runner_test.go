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

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/textrpc"
)

// execHarness is a configured bootstrap with test commands for an
// execRunner to run:
//   - xrecord appends its arguments, joined by |, to recorded;
//   - xfail returns an error spanning two lines;
//   - xqueue publishes an event that publishes another, which records
//     "queued", as keys that queue events do;
//   - xlater claims the caller's waiter and reports an error after it
//     returns;
//   - xopen leaves files loading for the next three checks, and records
//     "loaded" once they have loaded;
//   - xprompt opens a prompt and leaves files loading until Y answers it,
//     as opening a file whose grammar is not installed does, and records
//     "answered".
type execHarness struct {
	b         *bootstrapHandler
	recorded  []string
	exits     chan struct{}
	shotCalls []bool
	// loadingChecks counts the checks files stay loading for.
	loadingChecks int
	// awaitingAnswer reports that files load once xprompt's prompt is
	// answered.
	awaitingAnswer bool
}

func newExecHarness(t *testing.T) *execHarness {
	t.Helper()
	h := &execHarness{
		b: newConfiguredBootstrapForEnvTest(t, configFilename,
			"editor:\n  mode: modal\n", t.TempDir()),
		exits: make(chan struct{}, 1),
	}
	h.b.publishEvent = tickPublisher(t, h.b.publishEvent, func(ev term.Event) {
		if ev.Type == term.EventNone {
			h.exits <- struct{}{}
		}
	})
	commands := map[string]func(context.Context, textapi.Command) error{
		"xrecord": func(_ context.Context, cmd textapi.Command) error {
			h.recorded = append(h.recorded, strings.Join(cmd.Args, "|"))
			return nil
		},
		"xfail": func(context.Context, textapi.Command) error {
			return errors.New("first\nsecond")
		},
		"xqueue": func(context.Context, textapi.Command) error {
			h.b.publishEvent(term.Event{Type: term.EventInterrupt, UserFunc: func() {
				h.b.publishEvent(term.Event{Type: term.EventInterrupt, UserFunc: func() {
					h.recorded = append(h.recorded, "queued")
				}})
			}})
			return nil
		},
		"xlater": func(ctx context.Context, _ textapi.Command) error {
			w, ok := textrpc.WaiterFromContext(ctx)
			require.True(t, ok)
			w.Claimed = true
			go debug.CapturePanicReport(func() {
				time.Sleep(20 * time.Millisecond)
				w.Ch <- errors.New("late")
			})
			return nil
		},
		"xopen": func(context.Context, textapi.Command) error {
			h.loadingChecks = 3
			return nil
		},
		"xprompt": func(context.Context, textapi.Command) error {
			h.awaitingAnswer = true
			h.b.realIDE.Prompt("Install?", []string{"Yes"}, []term.KeyComb{{Ch: 'Y'}},
				handler.FuncPromptHandler(func(int, string) {
					h.awaitingAnswer = false
					h.recorded = append(h.recorded, "answered")
				}, func() error { return nil }))
			return nil
		},
	}
	for name, f := range commands {
		require.NoError(t, h.b.realIDE.SubscribeCommand(
			textapi.CommandManual{Name: name}, text.FuncCommandHandler(f, nil)))
	}
	return h
}

// tickPublisher publishes to publish in ticks, as the GUI runs events:
// what is published while a tick runs waits for the next one. It shows
// every event to observe as it is published.
func tickPublisher(
	t *testing.T, publish func(term.Event) bool, observe func(term.Event),
) func(term.Event) bool {
	var mu sync.Mutex
	var pending []term.Event
	stop, stopped := make(chan struct{}), make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(stopped)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			mu.Lock()
			batch := pending
			pending = nil
			mu.Unlock()
			if len(batch) == 0 {
				continue
			}
			publish(term.Event{Type: term.EventInterrupt, UserFunc: func() {
				for _, ev := range batch {
					if ev.UserFunc != nil {
						ev.UserFunc()
					}
				}
			}})
		}
	})
	t.Cleanup(func() {
		close(stop)
		<-stopped
	})
	return func(ev term.Event) bool {
		observe(ev)
		mu.Lock()
		defer mu.Unlock()
		pending = append(pending, ev)
		return true
	}
}

func (h *execHarness) runner(lines []string, stdin io.Reader, out io.Writer) *execRunner {
	r := newExecRunner(h.b, lines, stdin, out)
	r.takeScreenshot = func(reload bool, done func(string, error)) {
		h.shotCalls = append(h.shotCalls, reload)
		done("/shots/screenshot.png", nil)
	}
	loading := r.loading
	r.loading = func() bool {
		if h.awaitingAnswer {
			return true
		}
		if h.loadingChecks == 0 {
			return loading()
		}
		h.loadingChecks--
		if h.loadingChecks == 0 {
			h.recorded = append(h.recorded, "loaded")
		}
		return true
	}
	return r
}

// exitsOnNextEvent reports whether the IDE exits on the next event it
// handles, once the runner's exit request had time to run.
func (h *execHarness) exitsOnNextEvent(t *testing.T, requested bool) bool {
	t.Helper()
	if requested {
		select {
		case <-h.exits:
		case <-time.After(5 * time.Second):
			t.Fatal("the runner did not ask Rune to exit")
		}
	} else {
		// Lets a wrongly queued exit request run first.
		done := make(chan struct{})
		require.True(t, h.b.scheduleNextTick(func() { close(done) }))
		<-done
		require.Empty(t, h.exits, "the runner must not ask Rune to exit")
	}
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	exit, _ := h.b.Handle(term.Event{Type: term.EventNone})
	return exit
}

func TestExecRunner(t *testing.T) {
	tests := []struct {
		name         string
		lines        []string
		stdin        string
		wantReplies  []string
		wantRecorded []string
		wantShots    []bool
		wantExit     bool
	}{
		{
			name:         "replies in order with prompt quoting",
			lines:        []string{`xrecord a "b c" 'd e' f\ g`, "xrecord x"},
			wantReplies:  []string{"ok", "ok"},
			wantRecorded: []string{"a|b c|d e|f g", "x"},
		},
		{
			name:        "an error stays on one line",
			lines:       []string{"xfail", "xrecord after"},
			wantReplies: []string{`error: first\nsecond`, "ok"}, wantRecorded: []string{"after"},
		},
		{
			name:        "unknown command",
			lines:       []string{"nosuchcommand"},
			wantReplies: []string{`error: unknown command or command alias "nosuchcommand"`},
		},
		{
			name:         "the next command sees the events the previous one queued",
			lines:        []string{"xqueue", "xrecord after"},
			wantReplies:  []string{"ok", "ok"},
			wantRecorded: []string{"queued", "after"},
		},
		{
			name:         "the next command waits for files to load",
			lines:        []string{"xopen", "xrecord after"},
			wantReplies:  []string{"ok", "ok"},
			wantRecorded: []string{"loaded", "after"},
		},
		{
			name:         "a prompt that a load waits on is left for the next command to answer",
			lines:        []string{"xprompt", "echo Y", "xrecord after"},
			wantReplies:  []string{"ok", "ok", "ok"},
			wantRecorded: []string{"answered", "after"},
		},
		{
			name:        "a result reported after dispatch",
			lines:       []string{"xlater"},
			wantReplies: []string{"error: late"},
		},
		{
			name:         "extensionready waits for its command",
			lines:        []string{"extensionready dummy xrecord ready", "xrecord after"},
			wantReplies:  []string{"ok", "ok"},
			wantRecorded: []string{"ready", "after"},
		},
		{
			name:        "bare workspaceready",
			lines:       []string{"workspaceready"},
			wantReplies: []string{"ok"},
		},
		{
			name:  "screenshot replies with its path",
			lines: []string{"screenshot", "screenshot --reload", "screenshot now"},
			wantReplies: []string{"ok /shots/screenshot.png", "ok /shots/screenshot.png",
				`error: unexpected arguments ["now"]: expected [--reload]`},
			wantShots: []bool{false, true},
		},
		{
			name:         "stdin lines in place of -, then exit",
			lines:        []string{"xrecord first", "-", "xrecord last"},
			stdin:        "xrecord one\n\n   \nxrecord two",
			wantReplies:  []string{"ok", "ok", "ok", "ok"},
			wantRecorded: []string{"first", "one", "two", "last"},
			wantExit:     true,
		},
		{
			name:        "empty stdin exits",
			lines:       []string{"-"},
			wantExit:    true,
			wantReplies: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newExecHarness(t)
			var out bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			h.runner(tt.lines, strings.NewReader(tt.stdin), &out).run(ctx)

			var replies []string
			if out.Len() > 0 {
				replies = strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			}
			assert.Equal(t, tt.wantReplies, replies)
			assert.Equal(t, tt.wantShots, h.shotCalls)
			assert.Equal(t, tt.wantExit, h.exitsOnNextEvent(t, tt.wantExit))
			h.b.mu.Lock()
			defer h.b.mu.Unlock()
			assert.Equal(t, tt.wantRecorded, h.recorded)
		})
	}
}

func TestExecRunnerExitsWithADirtyBuffer(t *testing.T) {
	h := newExecHarness(t)
	file := filepath.Join(t.TempDir(), "dirty.txt")
	require.NoError(t, os.WriteFile(file, []byte("clean\n"), 0o644))
	stdin, feed := io.Pipe()
	replies, out := io.Pipe()
	go debug.CapturePanicReport(func() {
		h.runner([]string{execStdin}, stdin, out).run(context.Background())
		_ = out.Close()
	})
	read := bufio.NewScanner(replies)
	send := func(line string) string {
		t.Helper()
		_, err := io.WriteString(feed, line+"\n")
		require.NoError(t, err)
		require.True(t, read.Scan(), "no reply to %q", line)
		return read.Text()
	}

	require.Equal(t, "ok", send("edit "+file))
	h.b.mu.Lock()
	for _, ch := range "ix" {
		h.b.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	h.b.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	h.b.mu.Unlock()
	require.NoError(t, feed.Close())
	assert.False(t, read.Scan(), "no reply after stdin ends")
	assert.True(t, h.exitsOnNextEvent(t, true),
		"a dirty buffer must not hold the exit at the confirm prompt")
}

func TestExecRunnerSendsKeysToTheFileItOpened(t *testing.T) {
	h := newExecHarness(t)
	file := filepath.Join(t.TempDir(), "keys.txt")
	// Long enough to still be loading once the events edit queued ran.
	rest := strings.Repeat("filler line of text\n", 20000)
	require.NoError(t, os.WriteFile(file, []byte("clean\n"+rest), 0o644))
	var out bytes.Buffer
	h.runner([]string{"edit " + file, "echo ix<esc>", "write"}, nil, &out).
		run(context.Background())

	assert.Equal(t, "ok\nok\nok\n", out.String())
	got, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, "xclean\n"+rest, string(got))
}
