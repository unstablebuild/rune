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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/textrpc"
	"unstable.build/rune/internal/text/texttest"
)

// subscribeStepRecorder registers a command that appends its argv to out.
func subscribeStepRecorder(t *testing.T, b testEx, name string, out *[]string) {
	t.Helper()
	require.NoError(t, b.comp.SubscribeCommand(
		textapi.CommandManual{Name: name},
		text.FuncCommandHandler(func(_ context.Context, cmd textapi.Command) error {
			*out = append(*out, strings.Join(cmd.Args, " "))
			return nil
		}, nil)))
}

func newExForAliasRun(
	t *testing.T, aliases map[string]text.CommandAlias,
) testEx {
	t.Helper()
	return newExForTesting(t, texttest.NopEditor(),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithCommandKey(testCommandKey),
		text.WithCommandAliases(aliases))
}

// TestAliasPluginWaitDoesNotBlockEventLoop is the RUNE-345 regression: a
// `!!` alias step used to run the shell inline on the host event loop,
// so `:worktreeremove` froze the UI for as long as git took.
func TestAliasPluginWaitDoesNotBlockEventLoop(t *testing.T) {
	b := newExForAliasRun(t, map[string]text.CommandAlias{
		"slowalias": {Commands: []string{"!! sleep 10", "edit wi.go"}},
	})
	defer b.Close()
	b.ex.pluginWaitTimeout = 3 * time.Second
	b.Resize(20, 10)

	start := time.Now()
	require.NoError(t, b.ex.dispatchCommand("slowalias"))
	require.Less(t, time.Since(start), 500*time.Millisecond,
		"an alias whose step is a slow !! must not block the dispatcher")

	start = time.Now()
	b.ex.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
	require.Less(t, time.Since(start), 500*time.Millisecond,
		"the event loop must keep handling keys while the !! step runs")
}

// TestAliasPluginWaitStillOrdersChain pins the guarantee that the old
// inline run bought: step N+1 resolves the vars step N captured.
func TestAliasPluginWaitStillOrdersChain(t *testing.T) {
	b := newExForTestingWithWorkspace(t, &realExecLoader{testLoader: testLoader{}},
		texttest.NopEditor(), vte.DefaultConfig(),
		nopPublishEvent, clipboard.NewInMemory(),
		text.WithCommandOverlayConfig(testCommandOverlayConfig()),
		text.WithCommandKey(testCommandKey),
		text.WithCommandAliases(map[string]text.CommandAlias{
			"chained": {Commands: []string{
				"!! CAPTURED=$(/bin/echo chain-value)",
				"sink $CAPTURED",
			}},
		}))
	defer b.Close()

	var steps []string
	subscribeStepRecorder(t, b, "sink", &steps)

	require.NoError(t, b.ex.dispatchCommand("chained"))
	b.drainAliasRuns()

	assert.Equal(t, []string{"chain-value"}, steps,
		"the step after a !! must expand against that step's captures")
}

// TestAliasStepErrorAbortsChainAsync pins first-failing-step abort
// semantics for a failure that arrives from off-loop.
func TestAliasStepErrorAbortsChainAsync(t *testing.T) {
	b := newExForAliasRun(t, map[string]text.CommandAlias{
		"failing": {Commands: []string{"!! exit 1", "sink after"}},
	})
	defer b.Close()
	notes := &pluginWaitNotifications{inner: b.ex.notifications}
	b.ex.notifications = notes

	var steps []string
	subscribeStepRecorder(t, b, "sink", &steps)

	require.NoError(t, b.ex.dispatchCommand("failing"))
	b.drainAliasRuns()

	assert.Empty(t, steps, "a failed !! step must abort the rest of the chain")
	errs := notes.errorMessages()
	require.Len(t, errs, 1,
		"the aborted chain must surface the failing step in a notification")
	assert.Contains(t, errs[0], "!! exit 1: ",
		"the notification must name the step that failed")
}

// TestAliasDispatchQueuesFollowingCommands pins that freeing the loop
// does not reorder commands.
func TestAliasDispatchQueuesFollowingCommands(t *testing.T) {
	b := newExForAliasRun(t, map[string]text.CommandAlias{
		"queuing": {Commands: []string{"!! CAPTURED=x", "sink first"}},
	})
	defer b.Close()

	var steps []string
	subscribeStepRecorder(t, b, "sink", &steps)

	require.NoError(t, b.ex.dispatchCommand("queuing"))
	require.NoError(t, b.ex.dispatchCommand("sink", "second"))
	b.drainAliasRuns()

	assert.Equal(t, []string{"first", "second"}, steps,
		"a command dispatched after an alias must not overtake it")
}

// TestNestedAliasPluginWaitPropagatesCompletion covers a nested alias
// whose step completes off-loop.
func TestNestedAliasPluginWaitPropagatesCompletion(t *testing.T) {
	b := newExForAliasRun(t, map[string]text.CommandAlias{
		"outer": {Commands: []string{"inner", "sink outer-done"}},
		"inner": {Commands: []string{"!! CAPTURED=x", "sink inner-done"}},
	})
	defer b.Close()

	var steps []string
	subscribeStepRecorder(t, b, "sink", &steps)

	require.NoError(t, b.ex.dispatchCommand("outer"))
	b.drainAliasRuns()

	assert.Equal(t, []string{"inner-done", "outer-done"}, steps,
		"the outer alias must wait for the nested one to finish")
}

// TestAliasRunCancelledOnClose proves a parked run does not fire
// callbacks into a closed ex.
func TestAliasRunCancelledOnClose(t *testing.T) {
	b := newExForAliasRun(t, map[string]text.CommandAlias{
		"slowalias": {Commands: []string{"!! sleep 10", "sink ran"}},
	})
	b.ex.pluginWaitTimeout = 200 * time.Millisecond

	var steps []string
	subscribeStepRecorder(t, b, "sink", &steps)

	require.NoError(t, b.ex.dispatchCommand("slowalias"))
	require.NotNil(t, b.ex.runInFlight, "the !! step must have detached")

	require.NoError(t, b.Close())
	time.Sleep(500 * time.Millisecond)
	b.flushScheduled()

	assert.Empty(t, steps, "a closed ex must not keep walking alias steps")
}

// claimThenFailHandler claims its waiter and then fails the dispatch, as
// the extension command stream does when the send itself fails. Nothing
// will ever arrive on the waiter channel.
type claimThenFailHandler struct{ err error }

func (h claimThenFailHandler) HandleCommand(
	ctx context.Context, _ textapi.Command,
) error {
	if w, ok := textrpc.WaiterFromContext(ctx); ok {
		w.Claimed = true
	}
	return h.err
}

func (claimThenFailHandler) Complete(
	context.Context, textapi.Command,
) (iterator.Iterator[string], string, error) {
	return nil, "", nil
}

// claimThenSilentHandler claims its waiter and never reports, as a
// wedged extension does when its stream dies with replies outstanding.
type claimThenSilentHandler struct{}

func (claimThenSilentHandler) HandleCommand(
	ctx context.Context, _ textapi.Command,
) error {
	if w, ok := textrpc.WaiterFromContext(ctx); ok {
		w.Claimed = true
	}
	return nil
}

func (claimThenSilentHandler) Complete(
	context.Context, textapi.Command,
) (iterator.Iterator[string], string, error) {
	return nil, "", nil
}

// claimThenReportHandler claims its waiter and reports success after a
// delay, off the event loop.
type claimThenReportHandler struct{ after time.Duration }

func (h claimThenReportHandler) HandleCommand(
	ctx context.Context, _ textapi.Command,
) error {
	w, ok := textrpc.WaiterFromContext(ctx)
	if !ok {
		return nil
	}
	w.Claimed = true
	go debug.CapturePanicReport(func() {
		time.Sleep(h.after)
		w.Ch <- nil
	})
	return nil
}

func (claimThenReportHandler) Complete(
	context.Context, textapi.Command,
) (iterator.Iterator[string], string, error) {
	return nil, "", nil
}

// TestAliasStepClaimedWithErrorAbortsChain pins that a dispatch error
// wins over a claimed waiter. Nothing will report on a waiter whose
// handler failed, so parking on it would wedge the dispatcher.
func TestAliasStepClaimedWithErrorAbortsChain(t *testing.T) {
	b := newExForAliasRun(t, map[string]text.CommandAlias{
		"failing": {Commands: []string{"claimfail", "sink after"}},
	})
	defer b.Close()

	var steps []string
	subscribeStepRecorder(t, b, "sink", &steps)
	require.NoError(t, b.comp.SubscribeCommand(
		textapi.CommandManual{Name: "claimfail"},
		claimThenFailHandler{err: errors.New("send failed")}))

	err := b.ex.dispatchCommand("failing")
	require.ErrorContains(t, err, "claimfail: send failed")
	assert.Nil(t, b.ex.runInFlight, "a failed step must not hold the dispatch slot")
	assert.Empty(t, steps, "a failed step must abort the rest of the chain")
}

// TestAliasStepWaiterTimeoutReleasesDispatch pins that a step whose
// handler claims its waiter and never reports cannot hold the dispatch
// slot forever.
func TestAliasStepWaiterTimeoutReleasesDispatch(t *testing.T) {
	b := newExForAliasRun(t, map[string]text.CommandAlias{
		"wedged": {Commands: []string{"claimsilent", "sink after"}},
	})
	defer b.Close()
	b.ex.stepWaitTimeout = 50 * time.Millisecond
	notes := &pluginWaitNotifications{inner: b.ex.notifications}
	b.ex.notifications = notes

	var steps []string
	subscribeStepRecorder(t, b, "sink", &steps)
	require.NoError(t, b.comp.SubscribeCommand(
		textapi.CommandManual{Name: "claimsilent"}, claimThenSilentHandler{}))

	require.NoError(t, b.ex.dispatchCommand("wedged"))
	require.NotNil(t, b.ex.runInFlight, "the step must have taken the slot")

	require.NoError(t, b.ex.dispatchCommand("sink", "queued"))
	b.drainAliasRuns()

	assert.Nil(t, b.ex.runInFlight,
		"the dispatch slot must be released once the step times out")
	assert.Equal(t, []string{"queued"}, steps,
		"the chain must abort but the queue behind it must still drain")
	errs := notes.errorMessages()
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "claimsilent: ")
}

// TestNestedAliasNotBoundByOneStepBudget pins that the step timeout
// applies per leaf step: an outer alias parked on a nested alias must
// wait for the whole sub-chain, even when that outlasts one budget.
func TestNestedAliasNotBoundByOneStepBudget(t *testing.T) {
	b := newExForAliasRun(t, map[string]text.CommandAlias{
		"outer": {Commands: []string{"inner", "sink outer-done"}},
		"inner": {Commands: []string{"slow", "slow", "slow", "sink inner-done"}},
	})
	defer b.Close()
	b.ex.stepWaitTimeout = 350 * time.Millisecond
	notes := &pluginWaitNotifications{inner: b.ex.notifications}
	b.ex.notifications = notes

	var steps []string
	subscribeStepRecorder(t, b, "sink", &steps)
	require.NoError(t, b.comp.SubscribeCommand(
		textapi.CommandManual{Name: "slow"},
		claimThenReportHandler{after: 200 * time.Millisecond}))

	require.NoError(t, b.ex.dispatchCommand("outer"))
	b.drainAliasRuns()

	assert.Equal(t, []string{"inner-done", "outer-done"}, steps,
		"every leaf finished within budget, so the outer must run to the end")
	assert.Empty(t, notes.errorMessages(),
		"a nested alias must not be reported as a timed-out step")
}

// TestAliasWaiterCompletesAfterWholeChain pins the Waiter contract for a
// deferred dispatch: the result arrives once, after the whole chain.
func TestAliasWaiterCompletesAfterWholeChain(t *testing.T) {
	b := newExForAliasRun(t, map[string]text.CommandAlias{
		"chained": {Commands: []string{"!! CAPTURED=x", "sink chain"}},
	})
	defer b.Close()

	var steps []string
	subscribeStepRecorder(t, b, "sink", &steps)

	chainWaiter := &textrpc.Waiter{Ch: make(chan error, 1)}
	require.NoError(t, b.ex.dispatchCommandCtx(
		textrpc.ContextWithWaiter(context.Background(), chainWaiter), "chained"))
	require.True(t, chainWaiter.Claimed,
		"a detaching alias must claim the caller's waiter")

	queuedWaiter := &textrpc.Waiter{Ch: make(chan error, 1)}
	require.NoError(t, b.ex.dispatchCommandCtx(
		textrpc.ContextWithWaiter(context.Background(), queuedWaiter),
		"sink", "queued"))
	require.True(t, queuedWaiter.Claimed,
		"a queued dispatch must claim the caller's waiter too")

	require.Empty(t, chainWaiter.Ch,
		"the waiter must not report before the chain finished")

	b.drainAliasRuns()

	require.Len(t, chainWaiter.Ch, 1)
	require.NoError(t, <-chainWaiter.Ch)
	require.Len(t, queuedWaiter.Ch, 1)
	require.NoError(t, <-queuedWaiter.Ch)
	assert.Equal(t, []string{"chain", "queued"}, steps)
}
