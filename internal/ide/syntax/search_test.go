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

package syntax

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ernestrc/go-multierror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/workspace/walkdir"
)

type countingPkgManager struct {
	mu    sync.Mutex
	calls int
	files []string
}

func (m *countingPkgManager) LibDir(context.Context, string) (iterator.Iterator[string], error) {
	m.mu.Lock()
	m.calls++
	files := append([]string(nil), m.files...)
	m.mu.Unlock()
	return iterator.FromSlice(files), nil
}

func (m *countingPkgManager) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestCachingPkgManagerReturnsCachedFiles(t *testing.T) {
	root := &countingPkgManager{files: []string{"tree-sitter.so", "highlights.scm"}}
	pkg := newCachingPkgManager(root)
	pkg.cache("go", root.files)

	it, err := pkg.LibDir(context.Background(), "go")
	require.NoError(t, err)
	files, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	assert.Equal(t, []string{"tree-sitter.so", "highlights.scm"}, files)
	assert.Equal(t, 0, root.Calls())
}

type recordingProgress struct {
	mu     sync.Mutex
	events []progressEvent
}

type progressEvent struct {
	msg         string
	found       int
	step, total int64
}

func (r *recordingProgress) Report(msg string, found int, step, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, progressEvent{msg: msg, found: found, step: step, total: total})
}

func (r *recordingProgress) snapshot() []progressEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]progressEvent(nil), r.events...)
}

// TestAggregateProgressMonotonicAcrossSpecs feeds the aggregator the exact
// per-phase reports three specs emit — each restarting at step 0 against a
// fixed total of 4 — and asserts the forwarded stream is monotonic, never
// exceeds the total, grows the denominator per spec, and passes messages
// through unchanged.
func TestAggregateProgressMonotonicAcrossSpecs(t *testing.T) {
	sink := &recordingProgress{}
	agg := newAggregateProgress(sink)

	raw := []progressEvent{
		{msg: "Searching references…", step: 0, total: 4},
		{msg: "Searching definitions…", step: 2, total: 4},
		{msg: "Searching references…", step: 0, total: 4},
		{msg: "Searching definitions…", step: 2, total: 4},
		{msg: "Searching references…", step: 0, total: 4},
		{msg: "Searching definitions…", step: 2, total: 4},
	}
	for _, e := range raw {
		agg.Report(e.msg, e.found, e.step, e.total)
	}

	got := sink.snapshot()
	require.Len(t, got, len(raw))

	var prevStep, prevTotal int64 = -1, 0
	for i, e := range got {
		assert.Equalf(t, raw[i].msg, e.msg, "message passes through at %d", i)
		assert.GreaterOrEqualf(t, e.step, prevStep, "step non-decreasing at %d", i)
		assert.LessOrEqualf(t, e.step, e.total, "step <= total at %d", i)
		assert.GreaterOrEqualf(t, e.total, prevTotal, "total non-decreasing at %d", i)
		prevStep, prevTotal = e.step, e.total
	}
	assert.Equal(t, int64(12), got[len(got)-1].total, "denominator grows 4→8→12")
}

// assertMonotonic verifies the aggregator's core contract on a forwarded
// stream: step never decreases, total never decreases, and step never
// exceeds total. UpdateNotificationProgress rejects any violation, so these
// invariants must hold regardless of what a misbehaving spec reports.
func assertMonotonic(t *testing.T, got []progressEvent) {
	t.Helper()
	var prevStep, prevTotal int64 = -1, 0
	for i, e := range got {
		assert.GreaterOrEqualf(t, e.step, prevStep, "step non-decreasing at %d: %+v", i, got)
		assert.GreaterOrEqualf(t, e.total, prevTotal, "total non-decreasing at %d: %+v", i, got)
		assert.LessOrEqualf(t, e.step, e.total, "step <= total at %d: %+v", i, got)
		assert.GreaterOrEqualf(t, e.step, int64(0), "step non-negative at %d: %+v", i, got)
		prevStep, prevTotal = e.step, e.total
	}
}

// TestAggregateProgressMisbehavingReports exercises the aggregator against
// specs that report nonsensical progress — out-of-range steps, negative
// values, varying or zero totals, and stalled or repeated steps. The forwarded
// stream must stay monotonic and within bounds in every case so a buggy query
// can never break UpdateNotificationProgress.
func TestAggregateProgressMisbehavingReports(t *testing.T) {
	tests := []struct {
		name string
		raw  []progressEvent
	}{
		{
			name: "rawStep exceeds per-spec total",
			raw: []progressEvent{
				{step: 0, total: 4},
				{step: 9, total: 4},
				{step: 0, total: 4},
				{step: 7, total: 4},
			},
		},
		{
			name: "negative rawStep",
			raw: []progressEvent{
				{step: -5, total: 4},
				{step: 2, total: 4},
				{step: -1, total: 4},
			},
		},
		{
			name: "stalled repeated rawStep",
			raw: []progressEvent{
				{step: 2, total: 4},
				{step: 2, total: 4},
				{step: 2, total: 4},
			},
		},
		{
			name: "decreasing then increasing within a spec",
			raw: []progressEvent{
				{step: 3, total: 4},
				{step: 1, total: 4},
				{step: 2, total: 4},
			},
		},
		{
			name: "varying and zero rawTotal is ignored",
			raw: []progressEvent{
				{step: 0, total: 0},
				{step: 2, total: 99},
				{step: 0, total: -3},
				{step: 2, total: 1},
			},
		},
		{
			name: "monotonically increasing rawStep never restarts",
			raw: []progressEvent{
				{step: 0, total: 4},
				{step: 1, total: 4},
				{step: 2, total: 4},
				{step: 3, total: 4},
			},
		},
		{
			name: "single report",
			raw: []progressEvent{
				{step: 0, total: 4},
			},
		},
		{
			name: "near-overflow rawStep is clamped",
			raw: []progressEvent{
				{step: 0, total: 4},
				{step: math.MaxInt64, total: 4},
				{step: 0, total: 4},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &recordingProgress{}
			agg := newAggregateProgress(sink)
			for _, e := range tt.raw {
				agg.Report(e.msg, e.found, e.step, e.total)
			}
			got := sink.snapshot()
			require.Len(t, got, len(tt.raw))
			assertMonotonic(t, got)
		})
	}
}

// TestAggregateProgressMessagePassthrough confirms the aggregator only
// renormalizes step and total: the message and found count are forwarded
// verbatim even when the step values are nonsensical.
func TestAggregateProgressMessagePassthrough(t *testing.T) {
	sink := &recordingProgress{}
	agg := newAggregateProgress(sink)

	raw := []progressEvent{
		{msg: "phase one", found: 3, step: 9, total: 4},
		{msg: "phase two", found: 7, step: 0, total: 4},
	}
	for _, e := range raw {
		agg.Report(e.msg, e.found, e.step, e.total)
	}

	got := sink.snapshot()
	require.Len(t, got, len(raw))
	for i, e := range got {
		assert.Equal(t, raw[i].msg, e.msg)
		assert.Equal(t, raw[i].found, e.found)
	}
	assertMonotonic(t, got)
}

// TestAggregateProgressConcurrentReports drives the aggregator from many
// goroutines to prove the mutex keeps every forwarded event individually
// valid (step within bounds) under the race detector, since Report may be
// invoked concurrently in principle.
func TestAggregateProgressConcurrentReports(t *testing.T) {
	sink := &recordingProgress{}
	agg := newAggregateProgress(sink)

	const goroutines = 16
	const perGoroutine = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range perGoroutine {
				agg.Report("phase", 0, int64(i%resolveStepsPerSpec), resolveStepsPerSpec)
			}
		}()
	}
	wg.Wait()

	got := sink.snapshot()
	require.Len(t, got, goroutines*perGoroutine)
	// Concurrent callers interleave arbitrarily, so per-event monotonicity is
	// not guaranteed across goroutines; assert the invariant that must hold
	// for every individual event regardless of ordering.
	for i, e := range got {
		assert.LessOrEqualf(t, e.step, e.total, "step <= total at %d", i)
		assert.GreaterOrEqualf(t, e.step, int64(0), "step non-negative at %d", i)
	}
}

func TestNewParserCachesPackageFilesOnlyAfterRequiredFilesFound(t *testing.T) {
	dir := t.TempDir()
	root := &countingPkgManager{files: []string{
		filepath.Join(dir, "go", ParserFilename),
	}}
	pkg := newCachingPkgManager(root)

	_, err := newParser(context.Background(), "go", pkg, HighlightsFilename, "")
	require.ErrorIs(t, err, errNotInstalled)
	_, ok := pkg.files.Load("go")
	assert.False(t, ok, "missing query file should not populate the package cache")
	assert.Equal(t, 1, root.Calls())

	root.files = append(root.files, filepath.Join(dir, "go", HighlightsFilename))
	_, err = newParser(context.Background(), "go", pkg, HighlightsFilename, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open lib query file")
	assert.Equal(t, 2, root.Calls())
	files, ok := pkg.files.Load("go")
	require.True(t, ok)
	assert.Equal(t, root.files, files)

	_, err = newParser(context.Background(), "go", pkg, HighlightsFilename, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open lib query file")
	assert.Equal(t, 2, root.Calls(), "cached package files should skip LibDir")
}

func TestListSymbolsIteratorNext(t *testing.T) {
	tests := []struct {
		name       string
		send       []syntaxapi.Result
		closeCh    bool
		cancelCtx  bool
		cancelSelf bool
		wantCount  int
		wantErr    bool
	}{
		{
			name: "receives all results then channel closes",
			send: []syntaxapi.Result{
				{Text: "foo", CaptureName: "name"},
				{Text: "bar", CaptureName: "name"},
			},
			closeCh:   true,
			wantCount: 2,
		},
		{
			name:      "empty channel closed immediately",
			closeCh:   true,
			wantCount: 0,
		},
		{
			name: "single result",
			send: []syntaxapi.Result{
				{Text: "only", CaptureName: "definition"},
			},
			closeCh:   true,
			wantCount: 1,
		},
		{
			name:      "external context cancellation returns error",
			cancelCtx: true,
			wantCount: 0,
			wantErr:   true,
		},
		{
			name:       "iterator context cancellation returns error",
			cancelSelf: true,
			wantCount:  0,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := newTestIterator(len(tt.send))

			for _, r := range tt.send {
				it.ch <- r
			}
			if tt.closeCh {
				close(it.ch)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if tt.cancelCtx {
				cancel()
			}
			if tt.cancelSelf {
				it.cancel()
			}

			got := drainIterator(t, it, ctx)
			assert.Equal(t, tt.wantCount, got, "result count")

			err := it.Err()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestListSymbolsIteratorNextPreservesResultValues(t *testing.T) {
	it := newTestIterator(2)

	want := []syntaxapi.Result{
		{Text: "alpha", CaptureName: "function"},
		{Text: "beta", CaptureName: "type"},
	}
	for _, r := range want {
		it.ch <- r
	}
	close(it.ch)

	ctx := context.Background()
	var got []syntaxapi.Result
	for {
		r, ok := it.Next(ctx)
		if !ok {
			break
		}
		got = append(got, r)
	}

	require.Len(t, got, 2)
	assert.Equal(t, want[0].Text, got[0].Text)
	assert.Equal(t, want[0].CaptureName, got[0].CaptureName)
	assert.Equal(t, want[1].Text, got[1].Text)
	assert.Equal(t, want[1].CaptureName, got[1].CaptureName)
}

func TestListSymbolsIteratorErr(t *testing.T) {
	tests := []struct {
		name      string
		setErr    error
		cancelCtx bool
		wantNil   bool
		wantWraps error
	}{
		{
			name:    "no error and live context returns nil",
			wantNil: true,
		},
		{
			name:   "accumulated error surfaces",
			setErr: errors.New("something went wrong"),
		},
		{
			name:      "cancelled context without explicit error",
			cancelCtx: true,
			wantWraps: context.Canceled,
		},
		{
			name:      "both accumulated error and cancelled context",
			setErr:    errors.New("read failure"),
			cancelCtx: true,
			wantWraps: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			it := &chanIterator[syntaxapi.Result]{
				ctx:    ctx,
				err:    tt.setErr,
				cancel: cancel,
			}
			if tt.cancelCtx {
				cancel()
			}

			err := it.Err()
			if tt.wantNil {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			if tt.wantWraps != nil {
				assert.ErrorIs(t, err, tt.wantWraps)
			}
		})
	}
}

func TestListSymbolsIteratorErrMultierrorUnwrap(t *testing.T) {
	sentinel := errors.New("sentinel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	it := &chanIterator[syntaxapi.Result]{
		ctx:    ctx,
		err:    multierror.Append(nil, sentinel),
		cancel: cancel,
	}

	err := it.Err()
	require.Error(t, err)

	var merr *multierror.Error
	require.ErrorAs(t, err, &merr)
	require.GreaterOrEqual(t, len(merr.Errors), 1)

	found := false
	for _, e := range merr.Errors {
		if errors.Is(e, sentinel) {
			found = true
		}
	}
	assert.True(t, found, "sentinel error not found in multierror chain")
}

func TestListSymbolsIteratorErrNilErrReturnsCtxErr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done() // ensure it's expired

	it := &chanIterator[syntaxapi.Result]{ctx: ctx, cancel: cancel}

	err := it.Err()
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestListSymbolsIteratorClose(t *testing.T) {
	tests := []struct {
		name  string
		delay time.Duration
	}{
		{name: "immediate goroutine completion", delay: 0},
		{name: "blocks until goroutine finishes", delay: 50 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			closeWaitCh := make(chan struct{})

			it := &chanIterator[syntaxapi.Result]{
				ctx:         ctx,
				ch:          make(chan syntaxapi.Result),
				cancel:      cancel,
				closeWaitCh: closeWaitCh,
			}

			var done atomic.Bool

			go func() {
				if tt.delay > 0 {
					time.Sleep(tt.delay)
				}
				done.Store(true)
				close(closeWaitCh)
			}()

			err := it.Close()
			require.NoError(t, err)

			assert.True(t, done.Load(), "Close() returned before closeWaitCh was closed")
		})
	}
}

func TestListSymbolsIteratorClose_CancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closeWaitCh := make(chan struct{})

	it := &chanIterator[syntaxapi.Result]{
		ctx:         ctx,
		ch:          make(chan syntaxapi.Result),
		cancel:      cancel,
		closeWaitCh: closeWaitCh,
	}

	go func() { close(closeWaitCh) }()
	_ = it.Close()

	assert.Error(t, ctx.Err(), "context should be cancelled after Close")
}

func TestListSymbolsIteratorConcurrentAccess(t *testing.T) {
	const (
		numResults   = 50
		numConsumers = 4
	)

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan syntaxapi.Result, numResults)
	closeWaitCh := make(chan struct{})

	it := &chanIterator[syntaxapi.Result]{
		ctx:         ctx,
		ch:          ch,
		cancel:      cancel,
		closeWaitCh: closeWaitCh,
	}

	go func() {
		for i := range numResults {
			ch <- syntaxapi.Result{Text: string(rune('A' + i%26))}
		}
		close(ch)
		close(closeWaitCh)
	}()

	var wg sync.WaitGroup
	counts := make([]int, numConsumers)
	for i := range numConsumers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for {
				_, ok := it.Next(ctx)
				if !ok {
					return
				}
				counts[idx]++
			}
		}(i)
	}
	wg.Wait()

	total := 0
	for _, c := range counts {
		total += c
	}
	assert.Equal(t, numResults, total, "total results across goroutines")

	var errWg sync.WaitGroup
	for range 10 {
		errWg.Go(func() {
			_ = it.Err()
		})
	}
	errWg.Wait()
}

func newTestIterator(bufSize int) *chanIterator[syntaxapi.Result] {
	ctx, cancel := context.WithCancel(context.Background())
	return &chanIterator[syntaxapi.Result]{
		ctx:         ctx,
		ch:          make(chan syntaxapi.Result, bufSize),
		cancel:      cancel,
		closeWaitCh: make(chan struct{}),
	}
}

// The producer goroutines behind the streaming iterators derive their
// context from context.Background. If a consumer's context is
// canceled and the iterator is abandoned without Close, the producer
// must still be canceled or it blocks forever on its unbuffered
// results channel, pinning tree-sitter natives.
func TestIteratorNextCancelsProducerOnConsumerCtxDone(t *testing.T) {
	canceledCtx := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	tests := []struct {
		name string
		make func() (next func(context.Context) bool, producerCtx context.Context)
	}{
		{
			name: "chanIterator",
			make: func() (func(context.Context) bool, context.Context) {
				ctx, cancel := context.WithCancel(context.Background())
				it := &chanIterator[syntaxapi.Result]{
					ctx:         ctx,
					ch:          make(chan syntaxapi.Result),
					cancel:      cancel,
					closeWaitCh: make(chan struct{}),
				}
				return func(ctx context.Context) bool {
					_, ok := it.Next(ctx)
					return ok
				}, ctx
			},
		},
		{
			name: "resolveSymbolIterator",
			make: func() (func(context.Context) bool, context.Context) {
				ctx, cancel := context.WithCancel(context.Background())
				it := &resolveSymbolIterator{
					ctx:         ctx,
					ch:          make(chan syntaxapi.Match),
					cancel:      cancel,
					closeWaitCh: make(chan struct{}),
				}
				return func(ctx context.Context) bool {
					_, ok := it.Next(ctx)
					return ok
				}, ctx
			},
		},
		{
			name: "listReferencedSymbolsIterator",
			make: func() (func(context.Context) bool, context.Context) {
				ctx, cancel := context.WithCancel(context.Background())
				it := &listReferencedSymbolsIterator{
					ctx:         ctx,
					ch:          make(chan string),
					cancel:      cancel,
					closeWaitCh: make(chan struct{}),
				}
				return func(ctx context.Context) bool {
					_, ok := it.Next(ctx)
					return ok
				}, ctx
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, producerCtx := tt.make()
			ok := next(canceledCtx())
			require.False(t, ok)
			assert.Error(t, producerCtx.Err(),
				"producer context should be canceled after consumer ctx cancellation")
		})
	}
}

func drainIterator(t *testing.T, it *chanIterator[syntaxapi.Result], ctx context.Context) int {
	t.Helper()
	count := 0
	for {
		_, ok := it.Next(ctx)
		if !ok {
			return count
		}
		count++
	}
}

// blockingSpecSource yields the first spec immediately, then blocks on gate
// before yielding the rest, letting a test observe that detect streams the
// first spec before the underlying walk completes.
type blockingSpecSource struct {
	specs []symbolresolve.Spec
	gate  chan struct{}
	calls int32
}

func (s *blockingSpecSource) iterator(
	context.Context, walkdir.Reader,
) iterator.Iterator[symbolresolve.Spec] {
	atomic.AddInt32(&s.calls, 1)
	idx := 0
	return iterator.FromFunc(func(context.Context) (symbolresolve.Spec, bool, error) {
		if idx >= len(s.specs) {
			return symbolresolve.Spec{}, false, nil
		}
		if idx == 1 {
			<-s.gate
		}
		spec := s.specs[idx]
		idx++
		return spec, true, nil
	}, func() error { return nil })
}

func TestSpecCacheStreamsFirstSpecBeforeWalkCompletes(t *testing.T) {
	src := &blockingSpecSource{
		specs: []symbolresolve.Spec{{LangID: "go"}, {LangID: "python"}},
		gate:  make(chan struct{}),
	}
	cache := &specCache{source: src.iterator}

	it := cache.detect(context.Background())
	t.Cleanup(func() { _ = it.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	first, ok := it.Next(ctx)
	require.True(t, ok, "first spec must stream before the walk finishes")
	assert.Equal(t, "go", first.LangID)

	// The second spec is still gated, so a short-deadline Next must not
	// produce it yet.
	blockedCtx, blockedCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, ok = it.Next(blockedCtx)
	blockedCancel()
	assert.False(t, ok, "second spec must not arrive while the walk is blocked")

	close(src.gate)
	second, ok := it.Next(ctx)
	require.True(t, ok)
	assert.Equal(t, "python", second.LangID)

	_, ok = it.Next(ctx)
	assert.False(t, ok, "iterator ends after the walk completes")
}

func TestSpecCacheRunsWalkOnceAndReplays(t *testing.T) {
	src := &blockingSpecSource{
		specs: []symbolresolve.Spec{{LangID: "go"}, {LangID: "python"}},
		gate:  make(chan struct{}),
	}
	close(src.gate)
	cache := &specCache{source: src.iterator}

	ctx := context.Background()
	collect := func() []string {
		it := cache.detect(ctx)
		defer func() { _ = it.Close() }()
		var ids []string
		for {
			spec, ok := it.Next(ctx)
			if !ok {
				return ids
			}
			ids = append(ids, spec.LangID)
		}
	}

	first := collect()
	second := collect()
	assert.Equal(t, []string{"go", "python"}, first)
	assert.Equal(t, first, second, "later callers replay the cached specs")
	assert.Equal(t, int32(1), atomic.LoadInt32(&src.calls), "the walk must run once")
}

func TestSpecCacheConcurrentDetectRunsWalkOnce(t *testing.T) {
	src := &blockingSpecSource{
		specs: []symbolresolve.Spec{{LangID: "go"}, {LangID: "python"}},
		gate:  make(chan struct{}),
	}
	close(src.gate)
	cache := &specCache{source: src.iterator}

	ctx := context.Background()
	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	results := make([][]string, callers)
	for i := range callers {
		go func() {
			defer wg.Done()
			it := cache.detect(ctx)
			defer func() { _ = it.Close() }()
			for {
				spec, ok := it.Next(ctx)
				if !ok {
					return
				}
				results[i] = append(results[i], spec.LangID)
			}
		}()
	}
	wg.Wait()

	for i := range callers {
		assert.Equalf(t, []string{"go", "python"}, results[i], "caller %d", i)
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&src.calls), "the walk must run once across concurrent callers")
}
