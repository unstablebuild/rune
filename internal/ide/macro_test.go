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

package ide

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/text"
)

func TestMacroRecordAndEchoIntegration(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *macroIntegrationHarness)
	}{
		{
			name: "recorded insert keys echo back through event loop",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				handleKeys(t, tc, ":record<space>a<enter>ione<esc>:record<space>a<enter>")
				require.Equal(t, "one", editorString(t, tc.ide))

				handleKeys(t, tc, "Go<esc>:echo<space>{register}a<enter>")
				tc.drainPublishedEvents(t)
				require.Equal(t, "one\none", editorString(t, tc.ide))
			},
		},
		{
			name: "different registers hold different key sequences",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				handleKeys(t, tc, ":record<space>a<enter>ia<esc>:record<space>a<enter>")
				handleKeys(t, tc, "Go<esc>:record<space>b<enter>ib<esc>:record<space>b<enter>")
				require.Equal(t, "a\nb", editorString(t, tc.ide))

				handleKeys(t, tc, "Go<esc>:echo<space>{register}a<enter>")
				tc.drainPublishedEvents(t)
				handleKeys(t, tc, "Go<esc>:echo<space>{register}b<enter>")
				tc.drainPublishedEvents(t)

				require.Equal(t, "a\nb\na\nb", editorString(t, tc.ide))
			},
		},
		{
			name: "vim style qq q atq workflow works",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				handleKeys(t, tc, "qqabound<esc>q@q")
				tc.drainPublishedEvents(t)
				require.Equal(t, "boundbound", editorString(t, tc.ide))

				// After replay, further editing must not be affected by a
				// stale pendingMacro state (which would happen if the stop
				// q was recorded and replayed).
				handleKeys(t, tc, "atwo<esc>")
				require.Equal(t, "boundboundtwo", editorString(t, tc.ide))
			},
		},
		{
			name: "recorded command prompt command replays via echo",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				handleKeys(t, tc, ":record<space>a<enter>:tabrename<space>macroed<enter>:record<space>a<enter>")
				handleKeys(t, tc, ":notificationcloseall<enter>")
				require.Equal(t, `┌━━━━━━━━━─────────────────────────────┐
│o macroed                             │
├──────────────────────────────────────┤
│▐                                     │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                NORMAL│
└──────────────────────────────────────┘`, drawIDE(t, tc))
				handleKeys(t, tc, ":tabrename<space>reset<enter>")
				handleKeys(t, tc, ":notificationcloseall<enter>")
				require.Equal(t, `┌━━━━━━━───────────────────────────────┐
│o reset                               │
├──────────────────────────────────────┤
│▐                                     │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                NORMAL│
└──────────────────────────────────────┘`, drawIDE(t, tc))

				handleKeys(t, tc, ":echo<space>{register}a<enter>")
				tc.drainPublishedEvents(t)
				handleKeys(t, tc, ":notificationcloseall<enter>")

				require.Equal(t, `┌━━━━━━━━━─────────────────────────────┐
│o macroed                             │
├──────────────────────────────────────┤
│▐                                     │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                NORMAL│
└──────────────────────────────────────┘`, drawIDE(t, tc))
			},
		},
		{
			name: "record in one workspace and replay in another workspace",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				handleKeys(t, tc, ":record<space>a<enter>iws<esc>:record<space>a<enter>")
				require.Equal(t, "ws", editorString(t, tc.ide))

				secondWorkspace := t.TempDir()
				secondFile := filepath.Join(secondWorkspace, "second.txt")
				require.NoError(t, os.WriteFile(secondFile, nil, 0666))
				secondURI, err := workspaceapi.CurrentUserHostURI(secondFile)
				require.NoError(t, err)
				handleKeys(t, tc, ":workspaceopen<space>"+secondWorkspace+"<enter>")
				withLockedIDE(t, tc.mu, func() {
					require.NoError(t, tc.ide.Open(secondURI))
				})

				handleKeys(t, tc, ":echo<space>{register}a<enter>")
				tc.drainPublishedEvents(t)

				require.Equal(t, "ws", editorString(t, tc.ide))
			},
		},
		{
			name: "nested record command is rejected and not recorded",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				handleKeys(t, tc, ":record<space>a<enter>ix<esc>:record<space>b<enter>ay<esc>:record<space>a<enter>")
				require.Equal(t, "xy", editorString(t, tc.ide))

				handleKeys(t, tc, "Go<esc>:echo<space>{register}a<enter>")
				tc.drainPublishedEvents(t)

				require.Equal(t, "xy\nxy", editorString(t, tc.ide))
			},
		},
		{
			name: "prompt-open replay can be recorded without recording the stop command",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				handleKeys(t, tc, ":record<space>a<enter>:tabrename<space>macroed<enter>:record<space>a<enter>")
				handleKeys(t, tc, ":tabrename<space>reset<enter>:notificationcloseall<enter>")

				handleKeys(t, tc, ":record<space>b<enter>:echo<space>{register}a<enter>")
				tc.drainPublishedEvents(t)
				handleKeys(t, tc, ":record<space>b<enter>:notificationcloseall<enter>")

				require.Equal(t, `┌━━━━━━━━━─────────────────────────────┐
│o macroed                             │
├──────────────────────────────────────┤
│▐                                     │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                NORMAL│
└──────────────────────────────────────┘`, drawIDE(t, tc))

				handleKeys(t, tc, ":tabrename<space>reset<enter>:echo<space>{register}b<enter>")
				tc.drainPublishedEvents(t)
				handleKeys(t, tc, ":notificationcloseall<enter>")

				require.Equal(t, `┌━━━━━━━━━─────────────────────────────┐
│o macroed                             │
├──────────────────────────────────────┤
│▐                                     │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                NORMAL│
└──────────────────────────────────────┘`, drawIDE(t, tc))
			},
		},
		{
			name: "echoing the active recording register fails fast instead of looping",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				handleKeys(t, tc, ":record<space>q<enter>iok<esc>:echo<space>{register}q<enter>:notificationcloseall<enter>")
				tc.drainPublishedEvents(t)

				require.Equal(t, "ok", editorString(t, tc.ide))

				handleKeys(t, tc, ":record<space>q<enter>")
			},
		},
		{
			name: "reissued sequencer q after vi stop does not start recording",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				// Record via vi qq...q. The qq binding means the key
				// sequencer holds the first q and reissues it after timeout.
				handleKeys(t, tc, "qqio<esc>nnq")
				require.False(t, tc.ide.workspaceHandler.macro.IsRecording())

				// Verify register contents are clean (no trailing q).
				withLockedIDE(t, tc.mu, func() {
					data, err := tc.ide.workspaceHandler.clip.Paste("q")
					require.NoError(t, err)
					require.Equal(t, "io<esc>nn", data.Text,
						"register q must not contain the stop key")
				})

				// Replay 3 times (enough to trigger the bug).
				handleKeys(t, tc, "3@q")
				tc.drainPublishedEvents(t)

				// The reissued q from the sequencer must not leave pendingMacro.
				handleKeys(t, tc, "j")
				require.False(t, tc.ide.workspaceHandler.macro.IsRecording(),
					"j after replay must not start recording")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tc := newMacroIntegrationHarness(t)
			test.run(t, tc)
		})
	}
}

func TestNativeMacroPlaybackIntegration(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *macroIntegrationHarness)
	}{
		{
			name: "@q replays recorded macro natively",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				// Record "hello" into register q via vi qq...q.
				handleKeys(t, tc, "qqahello<esc>q")
				require.Equal(t, "hello", editorString(t, tc.ide))

				// Native @q should replay the macro.
				handleKeys(t, tc, "@q")
				tc.drainPublishedEvents(t)
				require.Equal(t, "hellohello", editorString(t, tc.ide))
			},
		},
		{
			name: "count prefix with @ replays N times",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				// Record appending "x" into register a.
				handleKeys(t, tc, "qqax<esc>q")
				require.Equal(t, "x", editorString(t, tc.ide))

				// 3@q should replay 3 times.
				handleKeys(t, tc, "3@q")
				tc.drainPublishedEvents(t)
				require.Equal(t, "xxxx", editorString(t, tc.ide))
			},
		},
		{
			name: "@@ replays the last used register",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				// Record appending "hi" into register a.
				handleKeys(t, tc, "qqahi<esc>q")
				require.Equal(t, "hi", editorString(t, tc.ide))

				// @q plays register q.
				handleKeys(t, tc, "@q")
				tc.drainPublishedEvents(t)
				require.Equal(t, "hihi", editorString(t, tc.ide))

				// @@ replays q again.
				handleKeys(t, tc, "@@")
				tc.drainPublishedEvents(t)
				require.Equal(t, "hihihi", editorString(t, tc.ide))
			},
		},
		{
			name: "@ during active recording of same register is rejected",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				// Store something in q first.
				handleKeys(t, tc, "qqaone<esc>q")
				require.Equal(t, "one", editorString(t, tc.ide))

				// Start recording into q again, then try @q.
				handleKeys(t, tc, "qq@q")
				tc.drainPublishedEvents(t)

				// Should not have replayed (recursive guard).
				// Stop recording.
				handleKeys(t, tc, "q")

				require.Equal(t, "one", editorString(t, tc.ide))
			},
		},
		{
			name: "externally populated self-referential register is rejected",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				withLockedIDE(t, tc.mu, func() {
					require.NoError(t, tc.ide.workspaceHandler.clip.Copy("q", clipboard.Data{Text: "@q"}))
				})

				// @q publishes @q from register q. The nested @q must be rejected
				// instead of repeatedly publishing itself forever.
				handleKeys(t, tc, "@q")
				tc.drainPublishedEvents(t)

				require.Equal(t, "", editorString(t, tc.ide))
			},
		},
		{
			name: "stale trailing q does not start recording on next key",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				withLockedIDE(t, tc.mu, func() {
					require.NoError(t, tc.ide.workspaceHandler.clip.Copy("q", clipboard.Data{Text: "aworld<esc>q"}))
				})

				handleKeys(t, tc, "@q")
				tc.drainPublishedEvents(t)
				require.Equal(t, "world", editorString(t, tc.ide))

				// The trailing q from the replayed macro used to leave the vi handler
				// waiting for a macro register, so this j would start recording into j.
				handleKeys(t, tc, "j")
				require.False(t, tc.ide.workspaceHandler.macro.IsRecording())
			},
		},
		{
			name: "normal editing works after @ playback",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				handleKeys(t, tc, "qqaworld<esc>q")
				require.Equal(t, "world", editorString(t, tc.ide))

				handleKeys(t, tc, "@q")
				tc.drainPublishedEvents(t)
				require.Equal(t, "worldworld", editorString(t, tc.ide))

				// Normal editing should work fine after playback.
				handleKeys(t, tc, "a!<esc>")
				require.Equal(t, "worldworld!", editorString(t, tc.ide))
			},
		},
		{
			name: "100@q with io esc nn macro does not leave pendingMacro",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				// Exact user repro: record io<esc>nn into q, play 100 times, then press j.
				handleKeys(t, tc, "qqio<esc>nnq")
				require.False(t, tc.ide.workspaceHandler.macro.IsRecording(),
					"recording must have stopped")

				withLockedIDE(t, tc.mu, func() {
					data, err := tc.ide.workspaceHandler.clip.Paste("q")
					require.NoError(t, err)
					require.Equal(t, "io<esc>nn", data.Text,
						"register must not contain trailing stop q")
				})

				handleKeys(t, tc, "100@q")
				tc.drainPublishedEvents(t)

				handleKeys(t, tc, "j")
				require.False(t, tc.ide.workspaceHandler.macro.IsRecording(),
					"j after 100@q replay must not start recording")
			},
		},
		{
			name: "register with trailing q still works after 100x replay",
			run: func(t *testing.T, tc *macroIntegrationHarness) {
				// Simulate a register that has a trailing q (as if the stop
				// key was recorded). This is the most likely production scenario.
				withLockedIDE(t, tc.mu, func() {
					require.NoError(t, tc.ide.workspaceHandler.clip.Copy("q",
						clipboard.Data{Text: "io<esc>nnq"}))
				})

				handleKeys(t, tc, "100@q")
				tc.drainPublishedEvents(t)

				handleKeys(t, tc, "j")
				require.False(t, tc.ide.workspaceHandler.macro.IsRecording(),
					"j after replay of register with trailing q must not start recording")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tc := newMacroIntegrationHarness(t)
			test.run(t, tc)
		})
	}
}

type macroIntegrationHarness struct {
	mu     *sync.Mutex
	events chan term.Event
	ide    *IDE
	h      *macroTestHandler
	drain  func() uint64
}

type macroTestHandler struct {
	tc *macroIntegrationHarness
}

func (h *macroTestHandler) Handle(ev term.Event) (bool, bool) {
	h.tc.mu.Lock()
	quit, handled := h.tc.ide.root.Handle(ev)
	h.tc.mu.Unlock()
	flushMacroHarness(h.tc)
	return quit, handled
}

func (h *macroTestHandler) Draw(w term.Writer) {
	flushMacroHarness(h.tc)
	h.tc.mu.Lock()
	defer h.tc.mu.Unlock()
	h.tc.ide.root.Draw(w)
}

func (h *macroTestHandler) Resize(width, height int) {
	h.tc.mu.Lock()
	defer h.tc.mu.Unlock()
	h.tc.ide.root.Resize(width, height)
}

func (h *macroTestHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	h.tc.mu.Lock()
	defer h.tc.mu.Unlock()
	return h.tc.ide.root.Cursor()
}

func (h *macroTestHandler) Selection() (string, bool) {
	h.tc.mu.Lock()
	defer h.tc.mu.Unlock()
	return h.tc.ide.root.Selection()
}

func newMacroIntegrationHarness(t *testing.T) *macroIntegrationHarness {
	t.Helper()
	dir := t.TempDir()
	cfgName := macroTestConfig(t, dir)
	fileName := filepath.Join(dir, "macro.txt")
	require.NoError(t, os.WriteFile(fileName, nil, 0666))
	uri, err := workspaceapi.CurrentUserHostURI(fileName)
	require.NoError(t, err)

	mu := new(sync.Mutex)
	events := make(chan term.Event, 4096)
	sched, drain := newTestScheduler(t, mu)
	i, err := New(dir, cfgName, dir, pkgtrust.NewStore(dir, nil), newTestStorage(t, dir),
		WithLocker(mu),
		WithScheduleNextTick(sched),
		WithPublishEvent(func(ev term.Event) bool {
			if ev.Type == term.EventInterrupt && ev.UserFunc == nil {
				return true
			}
			select {
			case events <- ev:
			default:
				t.Fatalf("event queue is full")
			}
			return true
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, i.Close()) })
	_ = i.Ready()
	tc := &macroIntegrationHarness{mu: mu, events: events, ide: i, drain: drain}
	tc.h = &macroTestHandler{tc: tc}

	// addWorkspace is async: New kicks off Phase B in a goroutine and
	// Phase C lands the install via scheduleNextTick. Wait for the
	// boot workspace to install before the test starts driving keys.
	flushMacroHarness(tc)

	withLockedIDE(t, mu, func() {
		require.NoError(t, i.Open(uri))
		i.root.Resize(40, 12)
	})
	return tc
}

func (h *macroIntegrationHarness) drainPublishedEvents(t *testing.T) {
	t.Helper()
	drainPublishedEvents(t, h, h.events)
}

func drawIDE(t *testing.T, tc *macroIntegrationHarness) string {
	t.Helper()
	return handlertest.DrawHandler(tc.h, 40, 12)
}

func macroTestConfig(t *testing.T, dir string) string {
	t.Helper()
	name := filepath.Join(dir, "rune.yaml")
	require.NoError(t, os.WriteFile(name, []byte(`
editor:
  mode: modal
command:
  show_manual: false
  key: ":"
  key_bindings:
    qq: record q
`), 0666))
	return name
}

func handleKeys(t *testing.T, tc *macroIntegrationHarness, seq string) {
	t.Helper()
	keys, err := term.ParseKeys(seq)
	require.NoError(t, err)
	for _, key := range keys {
		tc.h.Handle(term.Event{
			Type: term.EventKey,
			Ch:   key.Ch,
			Mod:  key.Mod,
			Key:  key.Key,
		})
		flushMacroHarness(tc)
	}
}

func drainPublishedEvents(t *testing.T, tc *macroIntegrationHarness, events <-chan term.Event) {
	t.Helper()
	for range 4096 {
		select {
		case ev := <-events:
			if ev.UserFunc != nil {
				withLockedIDE(t, tc.mu, func() {
					ev.UserFunc()
				})
			} else {
				tc.h.Handle(ev)
			}
			flushMacroHarness(tc)
		default:
			return
		}
	}
	t.Fatalf("published event drain limit exceeded")
}

func editorString(t *testing.T, i *IDE) string {
	t.Helper()
	// The fs-watcher and scheduled callbacks mutate editor state
	// under the IDE locker; take it for the read as well.
	i.workspaceHandler.mu.Lock()
	defer i.workspaceHandler.mu.Unlock()
	win, err := i.Browser().Focus()
	require.NoError(t, err)
	h, err := win.Content()
	require.NoError(t, err)
	if tab, ok := h.(*browser.Tab); ok {
		h = tab.Handler()
	}
	ed, ok := h.(text.Handler)
	require.True(t, ok)
	return ed.CellView().String()
}

func withLockedIDE(t *testing.T, mu *sync.Mutex, fn func()) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	fn()
}
func flushMacroHarness(tc *macroIntegrationHarness) {
	if tc == nil {
		return
	}
	quiesceHandler(tc.ide.workspaceHandler, tc.drain)
	tc.mu.Lock()
	handler := tc.ide.workspaceHandler.focusHandler()
	ex, ok := handler.(*ex)
	if !ok {
		ex = handler.(*workspaceHandler).ex
	}
	cmd := ex.cmd
	tc.mu.Unlock()
	if cmd != nil {
		cmd.Cancel()
	}
	ex.Wait()
}
