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
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
)

// syncSchedule runs fn on the calling goroutine, mirroring an event-loop
// scheduler that the wrapper marshals its observer callbacks through.
func syncSchedule(fn func()) bool {
	fn()
	return true
}

// stubREPLHandler records the last command it received and returns a
// canned iterator/error so the wrapper's forwarding and observation
// can be asserted in isolation.
type stubREPLHandler struct {
	lastCmd      repl.Command
	handleErr    error
	lastComplete struct {
		cmd  string
		args []string
	}
	lastHelpArgs []string
}

func (s *stubREPLHandler) HandleCommand(
	_ context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (sdkiterator.Iterator[component.Responsive], error) {
	s.lastCmd = cmd
	return sdkiterator.Empty[component.Responsive](), s.handleErr
}

func (s *stubREPLHandler) Complete(
	_ context.Context, cmd string, args []string,
) (sdkiterator.Iterator[string], error) {
	s.lastComplete.cmd = cmd
	s.lastComplete.args = args
	return sdkiterator.Empty[string](), nil
}

func (s *stubREPLHandler) Help(
	_ context.Context, args []string,
) (sdkiterator.Iterator[component.Responsive], error) {
	s.lastHelpArgs = args
	return sdkiterator.Empty[component.Responsive](), nil
}

var _ textapi.REPLHandler = (*stubREPLHandler)(nil)

// recordingArgsObserver captures the full observeCommand tuple so the
// wrapper's reported shape can be asserted.
type recordingArgsObserver struct {
	typed    string
	resolved string
	args     []string
	err      error
	calls    int
}

func (o *recordingArgsObserver) observeCommand(
	typed, resolved string, args []string, err error,
) {
	o.typed = typed
	o.resolved = resolved
	o.args = args
	o.err = err
	o.calls++
}

func TestObservingREPLHandlerReportsShellCommand(t *testing.T) {
	t.Parallel()

	stub := &stubREPLHandler{}
	obs := &recordingArgsObserver{}
	wrapper := observingREPLHandler{
		underlying: stub, observer: obs, name: "pkg", schedule: syncSchedule}

	iter, err := wrapper.HandleCommand(
		context.Background(),
		repl.Command{Name: "pkg", Args: []string{"install", "rune-agent"}},
		repl.NopProgressWriter(),
	)
	require.NoError(t, err)

	assert.Equal(t, 0, obs.calls,
		"a successful command must not be observed until its iterator completes")

	_, err = sdkiterator.ToSlice(context.Background(), iter)
	require.NoError(t, err)

	require.Equal(t, 1, obs.calls)
	assert.Equal(t, "console", obs.typed)
	assert.Equal(t, "console", obs.resolved)
	assert.Equal(t, []string{"pkg", "install", "rune-agent"}, obs.args)
	assert.NoError(t, obs.err)

	assert.Equal(t, repl.Command{Name: "pkg", Args: []string{"install", "rune-agent"}},
		stub.lastCmd, "the wrapper must forward the command unchanged")
}

func TestObservingREPLHandlerObservesOnceOnCloseAfterDrain(t *testing.T) {
	t.Parallel()

	stub := &stubREPLHandler{}
	obs := &recordingArgsObserver{}
	wrapper := observingREPLHandler{
		underlying: stub, observer: obs, name: "pkg", schedule: syncSchedule}

	iter, err := wrapper.HandleCommand(
		context.Background(),
		repl.Command{Name: "pkg", Args: []string{"status"}},
		repl.NopProgressWriter(),
	)
	require.NoError(t, err)

	_, err = sdkiterator.ToSlice(context.Background(), iter)
	require.NoError(t, err)
	require.NoError(t, iter.Close())

	assert.Equal(t, 1, obs.calls,
		"observation must fire exactly once across drain and Close")
}

func TestObservingREPLHandlerForwardsError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	stub := &stubREPLHandler{handleErr: wantErr}
	obs := &recordingArgsObserver{}
	wrapper := observingREPLHandler{
		underlying: stub, observer: obs, name: "models", schedule: syncSchedule}

	iter, err := wrapper.HandleCommand(
		context.Background(),
		repl.Command{Name: "models", Args: []string{"providers", "openai", "add", "default"}},
		repl.NopProgressWriter(),
	)
	require.ErrorIs(t, err, wantErr)
	if iter != nil {
		_ = iter.Close()
	}

	require.Equal(t, 1, obs.calls)
	assert.Equal(t, "console", obs.typed)
	assert.Equal(t, "console", obs.resolved)
	assert.Equal(t,
		[]string{"models", "providers", "openai", "add", "default"}, obs.args)
	assert.ErrorIs(t, obs.err, wantErr)
}

func TestObservingREPLHandlerForwardsCompleteAndHelp(t *testing.T) {
	t.Parallel()

	stub := &stubREPLHandler{}
	wrapper := observingREPLHandler{
		underlying: stub, observer: &recordingArgsObserver{},
		name: "pkg", schedule: syncSchedule}

	_, err := wrapper.Complete(context.Background(), "pkg", []string{"inst"})
	require.NoError(t, err)
	assert.Equal(t, "pkg", stub.lastComplete.cmd)
	assert.Equal(t, []string{"inst"}, stub.lastComplete.args)

	_, err = wrapper.Help(context.Background(), []string{"install"})
	require.NoError(t, err)
	assert.Equal(t, []string{"install"}, stub.lastHelpArgs)
}

func TestObservingREPLHandlerObservesOnSchedulerNotCaller(t *testing.T) {
	t.Parallel()

	// scheduled collects the observer callbacks the wrapper defers
	// instead of running inline; the "event loop" goroutine drains them.
	var mu sync.Mutex
	var scheduled []func()
	schedule := func(fn func()) bool {
		mu.Lock()
		scheduled = append(scheduled, fn)
		mu.Unlock()
		return true
	}

	// shared stands in for event-loop-owned state (e.g.
	// tutorialRunner.overlay) that both the loop and the observer touch.
	var shared int
	obs := &funcObserver{fn: func() { shared++ }}
	stub := &stubREPLHandler{}
	wrapper := observingREPLHandler{
		underlying: stub, observer: obs, name: "pkg", schedule: schedule}

	// Worker goroutine: the shell-command goroutine that drives the
	// command and drains its output iterator off the event loop.
	done := make(chan struct{})
	go func() {
		defer close(done)
		iter, err := wrapper.HandleCommand(
			context.Background(),
			repl.Command{Name: "pkg", Args: []string{"status"}},
			repl.NopProgressWriter(),
		)
		require.NoError(t, err)
		_, _ = sdkiterator.ToSlice(context.Background(), iter)
	}()

	// Event-loop goroutine: mutate shared and run any scheduled
	// observer callbacks. Because the wrapper defers the callback to the
	// scheduler, the access to shared stays on this goroutine.
	for {
		mu.Lock()
		pending := scheduled
		scheduled = nil
		mu.Unlock()
		for _, fn := range pending {
			fn()
		}
		shared++
		select {
		case <-done:
			mu.Lock()
			pending = scheduled
			scheduled = nil
			mu.Unlock()
			for _, fn := range pending {
				fn()
			}
			require.Equal(t, 1, obs.calls,
				"observer must fire exactly once, on the scheduler")
			return
		default:
		}
	}
}

// funcObserver runs fn on each observeCommand and counts invocations.
type funcObserver struct {
	fn    func()
	calls int
}

func (o *funcObserver) observeCommand(_, _ string, _ []string, _ error) {
	o.fn()
	o.calls++
}
