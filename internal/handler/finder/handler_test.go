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

package finder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/search"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/walkdir"
)

type closeTrackingIterator struct {
	next   func(context.Context) (string, bool)
	err    error
	closed bool
}

func (it *closeTrackingIterator) Next(ctx context.Context) (string, bool) { return it.next(ctx) }
func (it *closeTrackingIterator) Err() error                              { return it.err }
func (it *closeTrackingIterator) Close() error                            { it.closed = true; return nil }

// The workspace-API scan iterator owns walkdir traversal goroutines;
// every doScanDataViaWorkspaceAPI return path must close it.
func TestDoScanDataViaWorkspaceAPIClosesIterator(t *testing.T) {
	newHandler := func(stub *closeTrackingIterator) *fuzzyFinderHandler {
		return &fuzzyFinderHandler{
			workspaceFallback: func(workspaceapi.FileSystem, context.Context) (
				iterator.Iterator[string], error,
			) {
				return stub, nil
			},
		}
	}

	t.Run("iterator error", func(t *testing.T) {
		stub := &closeTrackingIterator{
			next: func(context.Context) (string, bool) { return "", false },
			err:  errors.New("walk failed"),
		}
		err := newHandler(stub).doScanDataViaWorkspaceAPI(
			context.Background(), make(chan []byte, 1))
		require.Error(t, err)
		assert.True(t, stub.closed)
	})

	t.Run("context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stub := &closeTrackingIterator{
			next: func(context.Context) (string, bool) { return "resource", true },
		}
		err := newHandler(stub).doScanDataViaWorkspaceAPI(ctx, make(chan []byte))
		require.ErrorIs(t, err, context.Canceled)
		assert.True(t, stub.closed)
	})
}

// TestNativeBackend_GitignoreFiltersResults exercises the native fuzzy-search
// backend end-to-end. It builds the handler with a real workspace.FileScheme
// rooted at testdata/, where .gitignore declares "*.TEST". The test asserts
// that:
//
//   - sample.text shows up in the rendered list
//   - sample.TEST does not (it is filtered by the gitignore matcher plumbed
//     through Clients.IgnoreMatcher → walkdir.WithContextFilter)
func TestNativeBackend_GitignoreFiltersResults(t *testing.T) {
	testdata, err := filepath.Abs("testdata")
	require.NoError(t, err)

	cwdURI, err := workspaceapi.CurrentUserHostURI(testdata)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), cwdURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	matcher, err := vctrl.LoadGitignore(scheme)
	require.NoError(t, err)

	clients := Clients{
		ResourceOpener: stubResourceOpener{},
		WindowManager:  stubWindowManager{},
		Interrupter:    term.NopInterrupter(),
		Notifications:  stubNotifications{},
		Editor:         nil,
		FileSystem:     scheme,
		Executor:       nil,
		IgnoreMatcher:  matcher,
	}

	listFiles := func(fs workspaceapi.FileSystem, ctx context.Context) (
		iterator.Iterator[string], error,
	) {
		return walkdir.ListFiles(ctx, fs, ".")
	}
	getResource := func(fs workspaceapi.FileSystem, line string) (
		workspaceapi.URI, term.Coordinates, bool,
	) {
		uri, err := fs.URI(line)
		return uri, term.Coordinates{}, err == nil
	}

	listCfg := search.ListConfig{
		Algo:        search.FuzzyMatch,
		Interrupter: term.NopInterrupter(),
		SyncSearch:  true,
	}
	rh, err := NewWithListConfig(
		context.Background(), clients, stubWindow(0),
		term.KeyComb{}, "", "", 0, listCfg,
		listFiles, getResource,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rh.Close() })

	h := rh.(*fuzzyFinderHandler)

	// Wait until the workspace scan has fully drained into the list before
	// asserting on rendered output.
	waitForScanWithTimeout(t, h, 5*time.Second)

	// Drive the handler through RunHandlerSequence as a black-box check.
	// Two assertions in one sequence:
	//
	//  1. With no query typed, the list shows the unfiltered scan output:
	//     ".gitignore" and "sample.text" — but NOT "sample.TEST", which is
	//     excluded by the testdata/.gitignore rule "*.TEST" applied through
	//     Clients.IgnoreMatcher.
	//  2. Typing "text" narrows the list to sample.text only and the count
	//     drops to "1/2".
	runner := &resizableHandler{h: h}
	initial := strings.Join([]string{
		"▐                                    2/2",
		".gitignore                              ",
		"sample.text                             ",
		"                                        ",
		"                                        ",
		"                                        ",
	}, "\n")
	filtered := strings.Join([]string{
		"text▐                                1/2",
		"sample.text                             ",
		"                                        ",
		"                                        ",
		"                                        ",
		"                                        ",
	}, "\n")
	handlertest.RunHandlerSequence(t, runner, 40, 6, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: initial},
		{InputSequence: "text", Expected: filtered},
	})
}

// waitForScanWithTimeout invokes the test-only scan barrier with a deadline so
// failures surface as a clear test error rather than a hang.
func waitForScanWithTimeout(t *testing.T, h *fuzzyFinderHandler, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		waitForScanForTest(h)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("scan did not complete within %s", d)
	}
}

func waitForScanForTest(h *fuzzyFinderHandler) {
	<-h.scanDone
	// Re-invoking Push cancels the prior push and waits for the prior
	// consumer goroutine to exit, guaranteeing the deferred pushData()
	// and sortMatchesList() in consumeAsyncElements have drained the
	// data channel. Close the new channel immediately so the freshly
	// started consumer also returns and the list returns to idle.
	ch := h.list.Push(context.Background())
	close(ch)
}

// resizableHandler is a thin tui.Handler shim around fuzzyFinderHandler so
// handlertest.RunHandlerSequence can drive it directly.
type resizableHandler struct {
	h *fuzzyFinderHandler
}

func (r *resizableHandler) Handle(ev term.Event) (exit, handled bool) {
	return r.h.Handle(ev)
}
func (r *resizableHandler) Resize(w, h int)    { r.h.Resize(w, h) }
func (r *resizableHandler) Draw(w term.Writer) { r.h.Draw(w) }
func (r *resizableHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return r.h.Cursor()
}
func (r *resizableHandler) Selection() (string, bool) { return r.h.Selection() }

// TestCloseDoesNotCloseSharedStorage verifies that closing a finder handler
// does not propagate Close to the storage service it borrowed via
// Clients.Storage. The storage handle is shared host-wide (it is the
// IDE-wide partition wrapping a single firstmover gRPC connection); closing
// it from inside the finder tears down the connection for every other
// consumer (notably command.Prompt history loads and any subsequent finder
// invocation), which manifests as
// "rpc error: code = Canceled desc = grpc: the client connection is closing".
func TestCloseDoesNotCloseSharedStorage(t *testing.T) {
	store := &closeCountingService{Service: storagestub.NewInMemoryService()}

	clients := Clients{
		Storage:        store,
		ResourceOpener: stubResourceOpener{},
		WindowManager:  stubWindowManager{},
		Interrupter:    term.NopInterrupter(),
		Notifications:  stubNotifications{},
	}
	listCfg := search.ListConfig{
		Algo:        search.FuzzyMatch,
		Interrupter: term.NopInterrupter(),
		SyncSearch:  true,
	}
	listFiles := func(_ workspaceapi.FileSystem, _ context.Context) (
		iterator.Iterator[string], error,
	) {
		return iterator.FromSlice([]string{}), nil
	}
	getResource := func(_ workspaceapi.FileSystem, _ string) (
		workspaceapi.URI, term.Coordinates, bool,
	) {
		return workspaceapi.URI{}, term.Coordinates{}, false
	}

	rh, err := NewWithListConfig(
		context.Background(), clients, stubWindow(0),
		term.KeyComb{}, "history-doc", "", 8, listCfg,
		listFiles, getResource,
	)
	require.NoError(t, err)

	require.NoError(t, rh.Close())
	assert.Equal(t, int32(0), store.closeCount.Load(),
		"finder.Close must not close the borrowed shared storage")

	// The shared storage must still be usable after the finder is closed.
	require.NoError(t, store.Set(context.Background(), "probe", map[string]any{"k": "v"}))
}

// closeCountingService wraps a storageapi.Service and counts Close calls so
// tests can assert that borrowed services are not closed by their consumer.
type closeCountingService struct {
	storageapi.Service
	closeCount atomic.Int32
}

func (s *closeCountingService) Close() error {
	s.closeCount.Add(1)
	return s.Service.Close()
}

// TestScanDoneWaitsForCommandOutputDrain guards the ScanWaiter
// contract: a closed ScanDone must mean readCommand has forwarded
// every line of the command's output to the list consumer. The
// executor stub signals process exit immediately after writing, so
// if the exit outruns the reader's drain, DrainList swaps the list
// consumer away and the buffered tail is silently dropped.
func TestScanDoneWaitsForCommandOutputDrain(t *testing.T) {
	const total = 200
	clients := Clients{
		ResourceOpener: stubResourceOpener{},
		WindowManager:  stubWindowManager{},
		Interrupter:    term.NopInterrupter(),
		Notifications:  stubNotifications{},
		Executor:       &instantExitExecutor{lines: total},
	}
	listCfg := search.ListConfig{
		Algo:        search.FuzzyMatch,
		Interrupter: term.NopInterrupter(),
		SyncSearch:  true,
	}
	rh, err := NewWithListConfig(
		context.Background(), clients, stubWindow(0),
		term.KeyComb{}, "", "list-entries", 0, listCfg,
		nil, nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rh.Close() })
	h := rh.(*fuzzyFinderHandler)

	waitForScanWithTimeout(t, h, 5*time.Second)

	assert.Equal(t, total, h.list.TotalCount(),
		"every line written before the process exited must be in the list")
}

// instantExitExecutor writes its lines to the command's stdout and
// reports process exit right away, without closing the write end —
// mirroring a real child that exits while the parent still holds its
// copy of the pipe and the reader has not yet drained the buffer.
type instantExitExecutor struct {
	lines int
}

func (e *instantExitExecutor) Start(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	var buf bytes.Buffer
	for i := range e.lines {
		fmt.Fprintf(&buf, "entry-%03d:1\n", i)
	}
	if _, err := cmd.Stdout.Write(buf.Bytes()); err != nil {
		return 0, err
	}
	ch := cmd.Watcher.WatchProcess()
	go debug.CapturePanicReport(func() { ch <- nil })
	return 1, nil
}

func (e *instantExitExecutor) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}

func (e *instantExitExecutor) Close() error { return nil }

// --- stubs ----------------------------------------------------------------

type stubWindow uint64

func (w stubWindow) WindowID() uint64 { return uint64(w) }

type stubResourceOpener struct{}

func (stubResourceOpener) Open(workspaceapi.URI) (browserapi.Handler, error) {
	return stubBrowserHandler{}, nil
}

type stubBrowserHandler struct{}

func (stubBrowserHandler) Handle(term.Event) (bool, bool) { return false, false }
func (stubBrowserHandler) Resize(int, int)                {}
func (stubBrowserHandler) Draw(term.Writer)               {}
func (stubBrowserHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}
func (stubBrowserHandler) Selection() (string, bool) { return "", false }
func (stubBrowserHandler) Close() error              { return nil }

type stubWindowManager struct{}

func (stubWindowManager) Focus() (browserapi.Window, error) { return stubWindow(0), nil }
func (stubWindowManager) Split(browserapi.Orientation, browserapi.Window, browserapi.Handler) (browserapi.Window, error) {
	return stubWindow(0), nil
}
func (stubWindowManager) Floating(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error) {
	return stubWindow(0), nil
}
func (stubWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (stubWindowManager) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return stubBrowserHandler{}, nil
}
func (stubWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error { return nil }
func (stubWindowManager) CloseWindow(browserapi.Window) error                          { return nil }

func (stubWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

type stubNotifications struct{}

func (stubNotifications) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (stubNotifications) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (stubNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}
