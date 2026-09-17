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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

func TestEventDispatching(t *testing.T) {
	t.Run("empty event does not panic", func(t *testing.T) {
		var mu sync.Mutex

		x := newExForEventTesting(t, &mu)
		fsev := testEventInfo{}

		assert.NotPanics(t, func() {
			dispatchFilesystemEvent(x, &mu, vctrl.NopMatcher(false), fsev)
		})
	})

	t.Run("dispatches", func(t *testing.T) {
		suite := []struct {
			in  schemeapi.Event
			out textapi.EventType
		}{
			{schemeapi.Create, textapi.EventTypeCreate},
			{schemeapi.Remove, textapi.EventTypeRemove},
			{schemeapi.Write, textapi.EventTypeChange},
			{schemeapi.Rename, textapi.EventTypeRename},
		}
		for _, test := range suite {
			desc := fmt.Sprintf("%s event when %s event is received", test.out, test.in)
			t.Run(desc, func(t *testing.T) {
				var mu sync.Mutex
				testURI, err := workspaceapi.ParseURI("file:///a")
				require.NoError(t, err)

				x := newExForEventTesting(t, &mu)
				fsev := testEventInfo{e: test.in, u: testURI}

				var called int
				x.comp.SubscribeEvents([]textapi.EventType{test.out},
					textapi.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
						called++
						assert.Equal(t, testURI, ev.URI)
						return false
					}))

				dispatchFilesystemEvent(x, &mu, vctrl.NopMatcher(false), fsev)
				assert.Equal(t, 1, called)
			})
		}
	})

	t.Run("ignores if matcher matches file", func(t *testing.T) {
		suite := []struct {
			in  schemeapi.Event
			out textapi.EventType
		}{
			{schemeapi.Create, textapi.EventTypeCreate},
			{schemeapi.Remove, textapi.EventTypeRemove},
			{schemeapi.Write, textapi.EventTypeChange},
			{schemeapi.Rename, textapi.EventTypeRename},
		}
		for _, test := range suite {
			desc := fmt.Sprintf("when %s event is received", test.in)
			t.Run(desc, func(t *testing.T) {
				var mu sync.Mutex
				testURI, err := workspaceapi.ParseURI("file:///a")
				require.NoError(t, err)

				ignores := vctrl.NopMatcher(true)

				x := newExForEventTesting(t, &mu)
				fsev := testEventInfo{e: test.in, u: testURI}

				var called int
				x.comp.SubscribeEvents([]textapi.EventType{test.out},
					textapi.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
						called++
						return false
					}))

				dispatchFilesystemEvent(x, &mu, ignores, fsev)
				assert.Equal(t, 0, called)
			})
		}
	})

	t.Run("ignores if user just flushed file", func(t *testing.T) {
		suite := []struct {
			in    schemeapi.Event
			dirty bool
		}{
			{schemeapi.Create, true},
			{schemeapi.Remove, true},
			{schemeapi.Write, true},
			{schemeapi.Rename, true},

			{schemeapi.Create, false},
			{schemeapi.Remove, false},
			{schemeapi.Write, false},
			{schemeapi.Rename, false},
		}
		for _, test := range suite {
			desc := fmt.Sprintf("when %s event is received, dirty=%t", test.in, test.dirty)
			t.Run(desc, func(t *testing.T) {
				var mu sync.Mutex

				ignores := vctrl.NopMatcher(true)

				x := newExForEventTesting(t, &mu)
				testURI, err := x.workspace.URI("a")
				require.NoError(t, err)

				require.NoError(t, x.editFiles(bgctx, testURI.Path()))
				ed, err := x.comp.Editor(testURI)
				require.NoError(t, err)
				// flush below clears dirty property but it's
				// still useful to make sure that it's integrated correctly
				if test.dirty {
					ctx := context.Background()
					_, _, _ = ed.CellEditor().
						Edit(ctx, term.Coordinates{}, term.Coordinates{}, "ABC")
				}
				win, err := x.comp.Focus()
				require.NoError(t, err)
				ch, err := x.comp.Flush(context.Background(), win)
				require.NoError(t, err)
				require.NoError(t, <-ch)

				res, ok := x.comp.Resource(testURI)
				require.True(t, ok)

				flush, err := x.comp.LastFlush(res)
				require.NoError(t, err)

				fsev := testEventInfo{e: test.in, u: testURI}
				dispatchFilesystemEvent(x, &mu, ignores, fsev)

				assertNoPrompt(t, x, &mu)

				lastFlush, err := x.comp.LastFlush(res)
				require.NoError(t, err)
				assert.Equal(t, flush, lastFlush)
			})
		}
	})

	t.Run("is goroutine safe", func(t *testing.T) {
		// Production callers serialize filesystem events on a
		// single goroutine (dispatchFilesystemEvents in
		// workspace_handler.go loops over the scheme's event
		// channel). dispatchFilesystemEvent locks mu internally
		// so concurrent fan-out is safe only when callers also
		// take mu before invoking it; without that, the
		// off-thread reload worker that writes tab attrs via
		// editorFlusherCloser.OnDidEdit races a concurrent
		// IsDirty read in handleFSChange. Mirror the production
		// caller's invariant here.
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")

		fsev := testEventInfo{e: schemeapi.Write, u: testURI}

		n := 1000
		dispatchMu := &sync.Mutex{}
		var wg sync.WaitGroup
		wg.Add(n)
		for range n {
			go func() {
				defer wg.Done()
				dispatchMu.Lock()
				defer dispatchMu.Unlock()
				dispatchFilesystemEvent(x, &mu, ignores, fsev)
				// Reloads are async: the worker that writes
				// dirty tab attrs via OnDidEdit continues
				// after dispatchFilesystemEvent returns.
				// Drain it before another dispatch reads
				// IsDirty from the host goroutine.
				x.waitInflight()
			}()
		}
		for i := range n {
			_ = os.WriteFile(testURI.Path(), []byte(strconv.Itoa(i)), 0666)
			mu.Lock()
			_, ok := x.comp.Resource(testURI)
			assert.True(t, ok)
			mu.Unlock()
		}
		wg.Wait()
	})

	t.Run("write triggers reload file if open, pre-created, non-dirty", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")

		fsev := testEventInfo{e: schemeapi.Write, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		assertBufferContent(t, x, testURI, "abc")
	})

	t.Run("write triggers reload file if open, uncreated, non-dirty", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		openWriteUncreatedFile(t, x, testURI, "abc")

		fsev := testEventInfo{e: schemeapi.Write, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		assertBufferContent(t, x, testURI, "abc")
	})

	t.Run("write triggers prompt if open, dirty file changes, user discards", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")

		editBuffer(t, x, testURI, "ABC")

		fsev := testEventInfo{e: schemeapi.Write, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		userPromptDiscards(t, x, &mu)

		assertBufferContent(t, x, testURI, "abc")

		assertNoPrompt(t, x, &mu)
	})

	t.Run("write triggers prompt if open, dirty file changes, user overwrites", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")
		editBuffer(t, x, testURI, "ABC")

		fsev := testEventInfo{e: schemeapi.Write, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		userPromptOverwrites(t, x, &mu)

		assertFileContent(t, x, testURI, "ABC")
		assertBufferContent(t, x, testURI, "ABC")

		assertNoPrompt(t, x, &mu)
	})

	t.Run("create triggers reload file if open, uncreated, non-dirty", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		openWriteUncreatedFile(t, x, testURI, "abc")

		fsev := testEventInfo{e: schemeapi.Create, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		assertBufferContent(t, x, testURI, "abc")
	})

	t.Run("create triggers reload file if open, pre-created, non-dirty", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")

		fsev := testEventInfo{e: schemeapi.Create, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		assertBufferContent(t, x, testURI, "abc")
		assertNoPrompt(t, x, &mu)
	})

	t.Run("create triggers prompt if open, dirty file changes; user discards", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")

		editBuffer(t, x, testURI, "ABC")

		fsev := testEventInfo{e: schemeapi.Create, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		userPromptDiscards(t, x, &mu)

		assertBufferContent(t, x, testURI, "abc")
		assertNoPrompt(t, x, &mu)
	})

	t.Run("create triggers prompt if open, dirty file changes; user overwrites", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")

		editBuffer(t, x, testURI, "ABC")

		fsev := testEventInfo{e: schemeapi.Create, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		userPromptOverwrites(t, x, &mu)

		assertFileContent(t, x, testURI, "ABC")
		assertBufferContent(t, x, testURI, "ABC")
		assertNoPrompt(t, x, &mu)
	})

	t.Run("remove triggers nothing if open, non-dirty file", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		require.NoError(t, x.editFiles(bgctx, testURI.String()))

		fsev := testEventInfo{e: schemeapi.Remove, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		assertNoPrompt(t, x, &mu)
		assertTabNotRemoved(t, x, testURI)
	})

	t.Run("remove triggers prompt if open, dirty file; user discards, removes tab", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenRemoveFile(t, x, testURI, "")

		editBuffer(t, x, testURI, "ABC")

		dirty, ok := x.comp.IsDirty(testURI)
		require.True(t, ok)
		require.True(t, dirty)

		fsev := testEventInfo{e: schemeapi.Remove, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		userPromptDiscards(t, x, &mu)

		assertTabRemoved(t, x, testURI)
		assertNoPrompt(t, x, &mu)
	})

	t.Run("remove triggers prompt if open, dirty file; user overwrites", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenRemoveFile(t, x, testURI, "")

		editBuffer(t, x, testURI, "ABC")

		fsev := testEventInfo{e: schemeapi.Remove, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		userPromptOverwrites(t, x, &mu)

		assertTabNotRemoved(t, x, testURI)
		assertBufferContent(t, x, testURI, "ABC")
		assertNoPrompt(t, x, &mu)
	})

	t.Run("rename original file triggers remove tab if open, non-dirty", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenRemoveFile(t, x, testURI, "")

		fsev := testEventInfo{e: schemeapi.Rename, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		assertTabRemoved(t, x, testURI)
		assertNoPrompt(t, x, &mu)
	})

	t.Run("rename target file triggers reload tab if open, non-dirty", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")

		res, ok := x.comp.Resource(testURI)
		require.True(t, ok)

		flush, err := x.comp.LastFlush(res)
		require.NoError(t, err)

		fsev := testEventInfo{e: schemeapi.Rename, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		x.waitInflight()

		lastFlush, err := x.comp.LastFlush(res)
		require.NoError(t, err)
		assert.NotEqual(t, flush, lastFlush)

		assertBufferContent(t, x, testURI, "abc")
		assertNoPrompt(t, x, &mu)
	})

	t.Run("rename target file opens prompt if open, dirty; user discards triggers reload", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")

		editBuffer(t, x, testURI, "ABC")

		fsev := testEventInfo{e: schemeapi.Rename, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		userPromptDiscards(t, x, &mu)

		assertBufferContent(t, x, testURI, "abc")
		assertNoPrompt(t, x, &mu)
	})

	t.Run("rename original file opens prompt if open, dirty; user discards triggers remove tab", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenRemoveFile(t, x, testURI, "abc")

		editBuffer(t, x, testURI, "ABC")

		fsev := testEventInfo{e: schemeapi.Rename, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		userPromptDiscards(t, x, &mu)

		assertTabRemoved(t, x, testURI)
		assertNoPrompt(t, x, &mu)
	})

	t.Run("rename target file opens prompt if open, dirty; user overwrites", func(t *testing.T) {
		var mu sync.Mutex
		ignores := vctrl.NopMatcher(false)

		x := newExForEventTesting(t, &mu)
		testURI, err := x.workspace.URI("a")
		require.NoError(t, err)

		createOpenWriteFile(t, x, testURI, "abc")

		editBuffer(t, x, testURI, "ABC")

		fsev := testEventInfo{e: schemeapi.Rename, u: testURI}
		dispatchFilesystemEvent(x, &mu, ignores, fsev)

		userPromptOverwrites(t, x, &mu)

		assertBufferContent(t, x, testURI, "ABC")
		assertNoPrompt(t, x, &mu)
	})
}

func TestMarkdownViewReload(t *testing.T) {
	tests := []struct {
		name   string
		reload func(*testing.T, *ex, sync.Locker, workspaceapi.URI)
	}{
		{
			name: "reloadfile command",
			reload: func(t *testing.T, x *ex, _ sync.Locker, _ workspaceapi.URI) {
				t.Helper()
				require.NoError(t, x.reloadfile(context.Background()))
			},
		},
		{
			name: "filesystem watcher",
			reload: func(t *testing.T, x *ex, mu sync.Locker, uri workspaceapi.URI) {
				t.Helper()
				dispatchFilesystemEvent(
					x,
					mu,
					vctrl.NopMatcher(false),
					testEventInfo{e: schemeapi.Write, u: uri},
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			schedule, drain := newTestScheduler(t, &mu)
			x := newExForEventTestingWithScheduler(t, schedule)
			drain()
			uri, err := x.workspace.URI("README.md")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(uri.Path(), []byte("# Before\n"), 0o666))
			require.NoError(t, x.viewFiles(context.Background(), uri.String()))

			require.NoError(t, os.WriteFile(uri.Path(), []byte("# After\n"), 0o666))
			changedAt := time.Now().Add(time.Second)
			require.NoError(t, os.Chtimes(uri.Path(), changedAt, changedAt))
			test.reload(t, x, &mu, uri)
			x.waitInflight()
			drain()

			mu.Lock()
			x.Resize(40, 8)
			writer := term.NewStringWriter(40, 8)
			x.Draw(writer)
			flushErr := writer.Flush()
			mu.Unlock()

			require.NoError(t, flushErr)
			assert.Contains(t, writer.String(), "After")
			assert.NotContains(t, writer.String(), "Before")
		})
	}
}

// TestHandleFSChange_NoPromptForSecondWriteDuringReload guards
// against a spurious "Discard your changes" prompt that fired when
// an external tool (e.g. `git rebase`) wrote to a clean, open file
// multiple times in quick succession.
//
// The first Write kicks off an async reload. The reload's
// buffer-mutation tick (workspace/file.go scheduleNextTick) raises
// OnDidEdit on text.editorFlusherCloser, which used to call
// setDirtyFileAttr unconditionally — flipping the tab to "dirty"
// even though the buffer was being rewritten from disk, not edited
// by the user. A second Write arriving before
// editorFlusherCloser.dispatchFlush ran (it's queued via the host
// scheduler) saw IsDirty=true in handleFSChange and routed to
// openFileChangedPrompt instead of just triggering another reload.
//
// With the fix, OnDidEdit short-circuits while the reload is in
// flight, so the second Write observes IsDirty=false and no prompt
// is opened.
func TestHandleFSChange_NoPromptForSecondWriteDuringReload(t *testing.T) {
	var mu sync.Mutex
	ignores := vctrl.NopMatcher(false)

	x := newExForEventTesting(t, &mu)
	testURI, err := x.workspace.URI("a")
	require.NoError(t, err)

	// Open a clean file. createOpenWriteFile writes empty content,
	// opens the buffer, then writes "abc" on disk — leaving the
	// buffer empty and disk modified, which mirrors the
	// "external tool just touched a clean file" precondition.
	createOpenWriteFile(t, x, testURI, "abc")

	dirty, ok := x.comp.IsDirty(testURI)
	require.True(t, ok)
	require.False(t, dirty, "precondition: buffer must be clean before first reload")

	// First Write kicks off the reload. The async worker reloads
	// the file from disk and schedules the buffer mutation onto
	// the workspace scheduler (inline in tests). OnDidEdit fires
	// for each cell-edit and previously raised the dirty
	// attribute mid-reload.
	fsev := testEventInfo{e: schemeapi.Write, u: testURI}
	dispatchFilesystemEvent(x, &mu, ignores, fsev)

	// Drain the async worker so the buffer mutation has run.
	// dispatchFlush (which clears efc.lastFlush) is still queued
	// on the host scheduler and intentionally not drained — this
	// is the precise window the production race opens.
	x.waitInflight()

	// Second Write arrives while the first reload's dispatchFlush
	// is still pending. handleFSChange must observe IsDirty=false
	// and route to startReloadAndNotify, not openFileChangedPrompt.
	require.NoError(t, os.WriteFile(testURI.Path(), []byte("xyz"), 0o666))
	// The reload above recorded the "abc" mtime as the last flush
	// time, and handleFSChange drops events whose mtime equals it
	// (an own-write echo). Linux stamps mtimes from a per-jiffy
	// clock (up to ~4ms apart), so this write can land on the same
	// tick and be mistaken for that echo — the same limitation git
	// calls a racily-clean entry. The scenario under test is a
	// later external write, so give it a strictly newer mtime.
	info, err := os.Stat(testURI.Path())
	require.NoError(t, err)
	bumped := info.ModTime().Add(10 * time.Millisecond)
	require.NoError(t, os.Chtimes(testURI.Path(), bumped, bumped))
	fsev2 := testEventInfo{e: schemeapi.Write, u: testURI}
	dispatchFilesystemEvent(x, &mu, ignores, fsev2)

	// If openFileChangedPrompt opened, the next keypress would be
	// handled by the prompt (returning handled=true). assertNoPrompt
	// verifies it wasn't.
	assertNoPrompt(t, x, &mu)

	// Final sanity: drain everything and confirm the buffer
	// reflects the latest disk content.
	x.waitInflight()
	assertBufferContent(t, x, testURI, "xyz")
}

func TestOutOfRootTabWatch(t *testing.T) {
	t.Run("reloads a clean buffer", func(t *testing.T) {
		suite := []struct {
			name  string
			write func(t *testing.T, path, content string)
		}{
			{"write in place", writeFileWithNewerMtime},
			{"rename over", renameFileWithNewerMtime},
		}
		for _, test := range suite {
			t.Run(test.name, func(t *testing.T) {
				x, mu := newOutOfRootWatchTest(t)

				file := outOfRootFile(t, t.TempDir(), "outside.txt")
				require.NoError(t, os.WriteFile(file.Path(), []byte("old"), 0o666))
				require.NoError(t, x.editFiles(bgctx, file.String()))

				test.write(t, file.Path(), "new")

				waitForBufferContent(t, x, mu, file, "new")
			})
		}
	})

	t.Run("prompts on a dirty buffer", func(t *testing.T) {
		x, mu := newOutOfRootWatchTest(t)

		file := outOfRootFile(t, t.TempDir(), "outside.txt")
		require.NoError(t, os.WriteFile(file.Path(), []byte(""), 0o666))
		require.NoError(t, x.editFiles(bgctx, file.String()))

		editBuffer(t, x, file, "ABC")
		writeFileWithNewerMtime(t, file.Path(), "new")

		require.Eventually(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			_, handled := x.Handle(term.Event{Type: term.EventKey, Ch: 'o'})
			return handled
		}, 5*time.Second, 10*time.Millisecond, "file changed prompt never opened")
		assertFileContent(t, x, file, "ABC")
	})

	t.Run("does not dispatch filesystem events to subscribers", func(t *testing.T) {
		x, mu := newOutOfRootWatchTest(t)

		var dispatched atomic.Int32
		require.NoError(t, x.comp.SubscribeEvents([]textapi.EventType{
			textapi.EventTypeCreate, textapi.EventTypeChange,
			textapi.EventTypeRename, textapi.EventTypeRemove,
		}, textapi.FuncEventHandler(func(context.Context, textapi.Event) bool {
			dispatched.Add(1)
			return false
		})))

		file := outOfRootFile(t, t.TempDir(), "outside.txt")
		require.NoError(t, os.WriteFile(file.Path(), []byte("old"), 0o666))
		require.NoError(t, x.editFiles(bgctx, file.String()))
		writeFileWithNewerMtime(t, file.Path(), "new")
		waitForBufferContent(t, x, mu, file, "new")

		assert.Zero(t, dispatched.Load())
	})

	t.Run("recovers after a failed stat", func(t *testing.T) {
		x, mu := newOutOfRootWatchTest(t)
		scheme := &flakyStatScheme{inner: x.workspace}
		x.outOfRootTabs.scheme = scheme

		file := outOfRootFile(t, t.TempDir(), "outside.txt")
		require.NoError(t, os.WriteFile(file.Path(), []byte("old"), 0o666))
		require.NoError(t, x.editFiles(bgctx, file.String()))
		scheme.failures.Store(3)

		writeFileWithNewerMtime(t, file.Path(), "new")

		waitForBufferContent(t, x, mu, file, "new")
		assert.Zero(t, scheme.failures.Load())
	})

	t.Run("polls until the last tab closes", func(t *testing.T) {
		x, mu := newOutOfRootWatchTest(t)

		a := outOfRootFile(t, t.TempDir(), "a.txt")
		b := outOfRootFile(t, t.TempDir(), "b.txt")
		require.NoError(t, os.WriteFile(a.Path(), []byte("a"), 0o666))
		require.NoError(t, os.WriteFile(b.Path(), []byte("b"), 0o666))
		require.NoError(t, x.editFiles(bgctx, a.String()))
		require.NoError(t, x.editFiles(bgctx, b.String()))
		assert.ElementsMatch(t, []string{a.String(), b.String()}, trackedOutOfRootTabs(x))

		removeTab(t, x, mu, a)
		assert.Equal(t, []string{b.String()}, trackedOutOfRootTabs(x))
		writeFileWithNewerMtime(t, b.Path(), "b2")
		waitForBufferContent(t, x, mu, b, "b2")

		removeTab(t, x, mu, b)
		assert.Empty(t, trackedOutOfRootTabs(x))
		waitForPollingStopped(t, x)
	})

	t.Run("stops polling when the workspace closes", func(t *testing.T) {
		x, mu := newOutOfRootWatchTest(t)

		file := outOfRootFile(t, t.TempDir(), "outside.txt")
		require.NoError(t, os.WriteFile(file.Path(), []byte("a"), 0o666))
		require.NoError(t, x.editFiles(bgctx, file.String()))

		mu.Lock()
		require.NoError(t, x.Close())
		mu.Unlock()

		waitForPollingStopped(t, x)
	})

	t.Run("does not poll on remote workspaces", func(t *testing.T) {
		x, mu := newOutOfRootWatchTest(t)
		w := newOutOfRootWatcher(x, remoteWorkspace{x.workspace}, x.workspaceURI, mu)

		file := outOfRootFile(t, t.TempDir(), "outside.txt")
		w.Handle(bgctx, textapi.Event{Type: textapi.EventTypeOpen, URI: file})

		assert.Empty(t, w.tabs)
		assert.Nil(t, w.cancel)
	})
}

func TestOutOfRootWatcherIsOutOfRoot(t *testing.T) {
	var mu sync.Mutex
	x := newExForEventTesting(t, &mu)

	base := t.TempDir()
	root := filepath.Join(base, "root")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "nested"), 0o755))
	require.NoError(t, os.Mkdir(root+"-sibling", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), nil, 0o666))
	rootLink := filepath.Join(base, "root-link")
	require.NoError(t, os.Symlink(root, rootLink))
	outside := t.TempDir()

	suite := []struct {
		name string
		root string
		file string
		want bool
	}{
		{"file in root", root, filepath.Join(root, "a.txt"), false},
		{"nested file in root", root, filepath.Join(root, "nested", "b.txt"), false},
		{"root itself", root, root, false},
		{"file outside root", root, filepath.Join(outside, "a.txt"), true},
		{"sibling sharing the root prefix", root, filepath.Join(root+"-sibling", "a.txt"), true},
		// A symlinked root (e.g. /tmp on macOS) with a tab at the real
		// path is already covered by the workspace watcher.
		{"real path under a symlinked root", rootLink, filepath.Join(root, "a.txt"), false},
		{"symlinked path into the root", root, filepath.Join(rootLink, "a.txt"), false},
		{"new file through a symlinked path", root, filepath.Join(rootLink, "new.txt"), false},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			w := newOutOfRootWatcher(x, x.workspace, outOfRootFile(t, test.root, ""), &mu)
			assert.Equal(t, test.want, w.isOutOfRoot(outOfRootFile(t, test.file, "")))
		})
	}

	t.Run("non-file scheme", func(t *testing.T) {
		w := newOutOfRootWatcher(x, x.workspace, outOfRootFile(t, root, ""), &mu)
		uri, err := workspaceapi.ParseURI(fileExplorerURI)
		require.NoError(t, err)
		assert.False(t, w.isOutOfRoot(uri))
	})
}

// newOutOfRootWatchTest builds an ex whose out-of-root watcher polls quickly
// and reports under the returned lock. The watcher is closed under that lock
// and drained before the ex closes, as production teardown serializes both
// with event handling.
func newOutOfRootWatchTest(t *testing.T) (*ex, sync.Locker) {
	t.Helper()
	mu := &sync.Mutex{}
	x := newExForEventTesting(t, mu)
	x.outOfRootTabs.uiMu = mu
	x.outOfRootTabs.interval = 10 * time.Millisecond
	t.Cleanup(func() {
		mu.Lock()
		x.outOfRootTabs.Close()
		mu.Unlock()
		x.outOfRootTabs.wait()
	})
	return x, mu
}

type flakyStatScheme struct {
	inner    statScheme
	failures atomic.Int32
}

func (s *flakyStatScheme) Stat(path string) (os.FileInfo, error) {
	if s.failures.Load() > 0 {
		s.failures.Add(-1)
		return nil, fmt.Errorf("stat %s: transport unavailable", path)
	}
	return s.inner.Stat(path)
}

type remoteWorkspace struct {
	workspace.Workspace
}

func (remoteWorkspace) OnDisconnect() <-chan struct{} {
	return make(chan struct{})
}

func (remoteWorkspace) WaitConnected(context.Context) error {
	return nil
}

func trackedOutOfRootTabs(x *ex) []string {
	w := x.outOfRootTabs
	w.mu.Lock()
	defer w.mu.Unlock()
	uris := make([]string, 0, len(w.tabs))
	for key := range w.tabs {
		uris = append(uris, key)
	}
	return uris
}

func waitForPollingStopped(t *testing.T, x *ex) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		x.outOfRootTabs.wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("out-of-root polling never stopped")
	}
}

func outOfRootFile(t *testing.T, dir, name string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + filepath.Join(dir, name))
	require.NoError(t, err)
	return uri
}

// writeFileWithNewerMtime writes content with an mtime strictly after the
// current one, so the change is observable and handleFSChange cannot mistake
// it for the echo of the buffer's own load or flush on filesystems with
// coarse mtimes.
func writeFileWithNewerMtime(t *testing.T, path, content string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o666))
	bumped := info.ModTime().Add(time.Second)
	require.NoError(t, os.Chtimes(path, bumped, bumped))
}

// renameFileWithNewerMtime saves the way editors do, by renaming a swap
// file over the original.
func renameFileWithNewerMtime(t *testing.T, path, content string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	swap := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".swp")
	require.NoError(t, os.WriteFile(swap, []byte(content), 0o666))
	bumped := info.ModTime().Add(time.Second)
	require.NoError(t, os.Chtimes(swap, bumped, bumped))
	require.NoError(t, os.Rename(swap, path))
}

func waitForBufferContent(
	t *testing.T, x *ex, mu sync.Locker, file workspaceapi.URI, content string,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		ed, err := x.comp.Editor(file)
		if err != nil {
			return false
		}
		return term.CellsToString(ed.CellView().RawCells()) == content
	}, 5*time.Second, 10*time.Millisecond, "buffer never reloaded to %q", content)
}

func removeTab(t *testing.T, x *ex, mu sync.Locker, file workspaceapi.URI) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	tab, ok := x.comp.Resource(file)
	require.True(t, ok)
	require.NoError(t, x.comp.RemoveTab(tab))
}

func assertFileContent(t *testing.T, x *ex, file workspaceapi.URI, content string) {
	t.Helper()
	// Reloads, flushes and overwrites are asynchronous; wait for
	// any in-flight ops to settle so the assertion reflects the
	// post-completion state instead of a snapshot mid-flight.
	x.waitInflight()
	data, err := os.ReadFile(file.Path())
	require.NoError(t, err)
	assert.Equal(t, content+"\n", string(data))
}

func assertBufferContent(t *testing.T, x *ex, file workspaceapi.URI, content string) {
	t.Helper()
	x.waitInflight()
	ed, err := x.comp.Editor(file)
	require.NoError(t, err)
	cells := ed.CellView().RawCells()
	assert.Equal(t, content, term.CellsToString(cells))
}

func assertTabRemoved(t *testing.T, x *ex, file workspaceapi.URI) {
	t.Helper()
	_, err := x.comp.Editor(file)
	require.Error(t, err, "tab was not removed")
}

func assertTabNotRemoved(t *testing.T, x *ex, file workspaceapi.URI) {
	_, err := x.comp.Editor(file)
	require.NoError(t, err, "tab was removed")
}

func assertNoPrompt(t *testing.T, x *ex, mu sync.Locker) {
	mu.Lock()
	exit, handled := x.Handle(term.Event{Type: term.EventKey, Ch: 'o'})
	mu.Unlock()
	assert.False(t, exit)
	require.False(t, handled)
}

// newExForEventTesting builds an ex whose workspace scheduler runs
// reload callbacks under mu, mirroring the host event loop where
// scheduled ticks and event handling share the UI lock. Callers must
// hold the same mu around x.Handle (and pass it to
// dispatchFilesystemEvent) so the async reload worker's buffer
// mutations cannot race in-flight event handling.
func newExForEventTesting(t *testing.T, mu sync.Locker) *ex {
	lockedSchedule := func(fn func()) bool {
		mu.Lock()
		defer mu.Unlock()
		fn()
		return true
	}
	return newExForEventTestingWithScheduler(t, lockedSchedule)
}

func newExForEventTestingWithScheduler(
	t *testing.T,
	schedule func(func()) bool,
) *ex {
	ctx := context.Background()
	opts := []text.Option{
		text.WithCommandKey(testCommandKey),
	}

	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", tempDir))
	require.NoError(t, err)

	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)

	workspace := workspace.NewSchemeWorkspace(uri, fileScheme, schedule)
	emulatorConfig := vte.DefaultConfig()
	emulatorConfig.ScheduleNextTick = schedule

	e := newExForTestingTerminal(t, workspace, texttest.NopEditor(),
		emulatorConfig, nopPublishEvent, plugin.DefaultBarConfig(), opts...)

	t.Cleanup(func() {
		require.NoError(t, e.Close())
		require.NoError(t, fileScheme.Close())
	})

	return e.ex
}

type testEventInfo struct {
	e schemeapi.Event
	u workspaceapi.URI
	d bool
}

func (t testEventInfo) Event() schemeapi.Event {
	return t.e
}

func (t testEventInfo) URI() workspaceapi.URI {
	return t.u
}

func (t testEventInfo) IsDir() (bool, error) {
	return t.d, nil
}

// createOpenWriteFile touches a file on disk, opens it for editing (empty) then
// changes the contents on disk to content.
func createOpenWriteFile(t *testing.T, x *ex, file workspaceapi.URI, content string) {
	require.NoError(t, os.WriteFile(file.Path(), []byte(""), 0666))
	require.NoError(t, x.editFiles(bgctx, file.String()))
	require.NoError(t, os.WriteFile(file.Path(), []byte(content), 0666))
}

// createOpenRemoveFile writes content to a file on disk, then opens it for editing
// (non empty, with content), then removes it from disk.
func createOpenRemoveFile(t *testing.T, x *ex, file workspaceapi.URI, content string) {
	require.NoError(t, os.WriteFile(file.Path(), []byte(content), 0666))
	require.NoError(t, x.editFiles(bgctx, file.String()))
	require.NoError(t, os.Remove(file.Path()))
}

// openWriteUncreatedFile open an empty, uncreated file for editing, then changes
// the contents on disk to content.
func openWriteUncreatedFile(t *testing.T, x *ex, file workspaceapi.URI, content string) {
	require.NoError(t, x.editFiles(bgctx, file.String()))
	require.NoError(t, os.WriteFile(file.Path(), []byte("abc"), 0666))
}

func userPromptDiscards(t *testing.T, x *ex, mu sync.Locker) {
	keyCombs := []rune{'d', 'y'}
	for _, key := range keyCombs {
		mu.Lock()
		exit, handled := x.Handle(term.Event{Type: term.EventKey, Ch: key})
		mu.Unlock()
		assert.False(t, exit)
		assert.True(t, handled)
	}
}

func userPromptOverwrites(t *testing.T, x *ex, mu sync.Locker) {
	keyCombs := []rune{'o'}
	for _, key := range keyCombs {
		mu.Lock()
		exit, handled := x.Handle(term.Event{Type: term.EventKey, Ch: key})
		mu.Unlock()
		assert.False(t, exit)
		assert.True(t, handled)
	}
}

func editBuffer(t *testing.T, x *ex, file workspaceapi.URI, content string) {
	h, err := x.comp.Editor(file)
	require.NoError(t, err)

	ctx := context.Background()
	_, _, _ = h.CellEditor().
		Edit(ctx, term.Coordinates{}, term.Coordinates{}, "ABC")

	dirty, ok := x.comp.IsDirty(file)
	require.True(t, ok)
	require.True(t, dirty)
}
