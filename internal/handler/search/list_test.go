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

package search

import (
	"bytes"
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"go.uber.org/goleak"
	"unstable.build/rune/internal/cell"
)

type listConstructor func(ListConfig) (listIfc, *cell.Buffer)

type listIfc interface {
	Close() error
	DataReset()
	Draw(w term.Writer)
	Focus() (Match, bool)
	FocusDown() bool
	FocusEnd() bool
	FocusStart() bool
	FocusUp() bool
	InputHeight() int
	MatchCount() int
	Push(context.Context) chan<- []byte
	PushSync(b []byte)
	Resize(width, height int)
	SetMinInputHeight(height int)
	TotalCount() int
	Wait()
}

func wgInterrupter(wg *sync.WaitGroup) term.Interrupter {
	return term.FuncInterrupter(func(context.Context) error {
		wg.Done()
		return nil
	})
}

func assertNoLeaks(t *testing.T, l listIfc) {
	assert.NoError(t, l.Close())
	ignoreOpenCensus := goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start")
	goleak.VerifyNone(t, ignoreOpenCensus)
}

func assertFocusEqual(t *testing.T, l listIfc, el []byte) {
	ret, ok := l.Focus()
	require.True(t, ok)
	assert.Equal(t, string(el), string(ret.Data()))
}

func newSimpleList(cfg ListConfig) (listIfc, *cell.Buffer) {
	// make tests deterministic
	cfg.interruptEvery = 24 * time.Hour
	cfg.setFileCountEvery = 1
	l := NewList(cfg)
	return l, l.Buffer()
}

func newSimpleListBottomSearchBar(cfg ListConfig) (listIfc, *cell.Buffer) {
	cfg.BottomSearchBar = true
	cfg.interruptEvery = 24 * time.Hour
	cfg.setFileCountEvery = 1
	l := NewList(cfg)
	return l, l.Buffer()
}

func TestListCount(t *testing.T) {
	testListCount(t, newSimpleList)
}

func testListCount(t *testing.T, constructor listConstructor) {
	var wg sync.WaitGroup
	l, buf := constructor(ListConfig{Interrupter: wgInterrupter(&wg)})

	l.PushSync([]byte("capitol insurrection"))
	assert.Equal(t, 1, l.TotalCount())
	assert.Equal(t, 1, l.MatchCount())

	wg.Add(1)
	buf.WriteString("c")
	wg.Wait()

	assert.Equal(t, 1, l.TotalCount())
	assert.Equal(t, 1, l.MatchCount())

	wg.Add(1)
	buf.DeleteCell(term.Coordinates{X: 0})
	wg.Wait()

	// we cannot do two at a time because there's a race between canceling
	// the original async search and calling interrupt.
	wg.Add(1)
	buf.WriteString("X")
	wg.Wait()

	assert.Equal(t, 1, l.TotalCount())
	assert.Equal(t, 0, l.MatchCount())

	l.PushSync([]byte("X Files"))
	assert.Equal(t, 2, l.TotalCount())
	assert.Equal(t, 1, l.MatchCount())

	assertNoLeaks(t, l)
}

func TestListDataResetDropsInputBackingStorage(t *testing.T) {
	l := NewList(ListConfig{})
	t.Cleanup(func() { assert.NoError(t, l.Close()) })

	for i := range 1000 {
		l.PushSync([]byte(strconv.Itoa(i)))
	}
	require.Greater(t, cap(l.input), 0)

	l.DataReset()

	assert.Zero(t, len(l.input))
	assert.Zero(t, cap(l.input))
	assert.Equal(t, 0, l.TotalCount())
	assert.Equal(t, 0, l.MatchCount())
}

func TestListFocus(t *testing.T) {
	testListFocus(t, newSimpleList)
}

func testListFocus(t *testing.T, constructor listConstructor) {
	l, _ := constructor(ListConfig{})
	_, ok := l.Focus()
	assert.False(t, ok)

	el1 := []byte("angry pants")
	el2 := []byte("angry girls")
	el3 := []byte("pants :@")
	l.PushSync(el1)
	l.PushSync(el2)
	l.PushSync(el3)
	assertFocusEqual(t, l, el1)

	assert.True(t, l.FocusDown())
	assert.True(t, l.FocusDown())
	assertFocusEqual(t, l, el3)

	assert.True(t, l.FocusStart())
	assertFocusEqual(t, l, el1)

	assert.True(t, l.FocusEnd())
	assertFocusEqual(t, l, el3)

	assert.True(t, l.FocusUp())
	assertFocusEqual(t, l, el2)

	assertNoLeaks(t, l)
}

func TestListFocusBottomSearchBar(t *testing.T) {
	testListFocusBottomSearchBar(t, newSimpleList)
}

func testListFocusBottomSearchBar(t *testing.T, constructor listConstructor) {
	l, _ := constructor(ListConfig{BottomSearchBar: true})
	_, ok := l.Focus()
	assert.False(t, ok)

	el1 := []byte("angry pants")
	el2 := []byte("angry girls")
	el3 := []byte("pants :@")
	l.PushSync(el1)
	l.PushSync(el2)
	l.PushSync(el3)
	assertFocusEqual(t, l, el3)

	assert.False(t, l.FocusDown())
	assert.True(t, l.FocusUp())
	assert.True(t, l.FocusUp())
	assertFocusEqual(t, l, el1)

	assert.True(t, l.FocusStart())
	assertFocusEqual(t, l, el1)

	assert.True(t, l.FocusEnd())
	assertFocusEqual(t, l, el3)

	assert.True(t, l.FocusUp())
	assertFocusEqual(t, l, el2)

	assertNoLeaks(t, l)
}

func pushTestData(l listIfc, n int) chan<- []byte {
	// push exactly the number of elements equal to
	// this component's height, so only interrupt should
	// be called only once while processing data
	ctx := context.Background()

	ch := l.Push(ctx)
	for i := range n {
		ch <- []byte(strconv.Itoa(i))
	}
	return ch
}

func TestListAsyncPush(t *testing.T) {
	testListAsyncPush(t, newSimpleList)
}

func TestListAsyncPushBottomSearchBar(t *testing.T) {
	testListAsyncPush(t, newSimpleListBottomSearchBar)
}

func testListAsyncPush(t *testing.T, constructor listConstructor) {
	t.Run("no search", func(t *testing.T) {
		var wg sync.WaitGroup

		l, _ := constructor(ListConfig{Interrupter: wgInterrupter(&wg)})
		height := 100
		l.Resize(100, height)

		wg.Add(2)
		go func() {
			defer wg.Done()
			ch := pushTestData(l, height)
			close(ch)
		}()

		wg.Wait()

		assert.Equal(t, height, l.TotalCount())
		assert.Equal(t, height, l.MatchCount())

		assertNoLeaks(t, l)
	})

	t.Run("search query after items pushed", func(t *testing.T) {
		var wg sync.WaitGroup
		l, buf := constructor(ListConfig{Interrupter: wgInterrupter(&wg)})
		height := 100
		l.Resize(100, height)

		wg.Add(1)
		ch := pushTestData(l, height)
		close(ch)
		wg.Wait()

		wg.Add(2)
		buf.WriteString("9")
		l.Wait()
		buf.WriteString("9")
		l.Wait()

		assert.Equal(t, height, l.TotalCount())
		assert.Equal(t, 1, l.MatchCount())

		assertNoLeaks(t, l)
	})

	t.Run("search query before items pushed", func(t *testing.T) {
		var wg sync.WaitGroup
		l, buf := constructor(ListConfig{Interrupter: wgInterrupter(&wg)})
		n := 100
		l.Resize(n, n)

		wg.Add(1)
		buf.WriteString("9")
		l.Wait() // make next search query doesn't cancel prev
		wg.Wait()

		wg.Add(1)
		buf.WriteString("9")
		l.Wait()
		wg.Wait()

		wg.Add(1)
		ch := pushTestData(l, n)
		close(ch)
		wg.Wait()

		// make sure it doesn't block
		l.Wait()

		assert.Equal(t, n, l.TotalCount())
		assert.Equal(t, 1, l.MatchCount())

		assertNoLeaks(t, l)
	})

	t.Run("concurrent search query", func(t *testing.T) {
		l, buf := constructor(ListConfig{Interrupter: term.NopInterrupter()})
		n := 100
		l.Resize(n, n)

		ch := l.Push(context.Background())
		go func() {
			defer close(ch)
			for i := range n {
				ch <- []byte(strconv.Itoa(i))
			}
		}()

		buf.WriteString("9")
		buf.WriteString("9")

		l.Wait()
		assert.Equal(t, n, l.TotalCount())
		assert.Equal(t, 1, l.MatchCount())

		assertNoLeaks(t, l)
	})
}

// streamBatch pushes data through a fresh Push channel and closes it,
// then Waits so the deterministic close-time sort/pushData settles. With
// interruptEvery set to a long duration, the only sort happens on channel
// close (via consumeAsyncElements' deferred sort), making the streaming
// re-rank observable without timing races.
func streamBatch(t *testing.T, l *List, items ...string) {
	t.Helper()
	ch := l.Push(context.Background())
	for _, it := range items {
		ch <- []byte(it)
	}
	close(ch)
	l.Wait()
}

func TestPushOuterCancelDoesNotUnblockDrainEarly(t *testing.T) {
	const total = 50
	for iter := 0; iter < 100; iter++ {
		l := NewList(ListConfig{Interrupter: term.NopInterrupter()})
		ctx, cancel := context.WithCancel(context.Background())
		ch := l.Push(ctx)
		for i := 0; i < total; i++ {
			ch <- []byte(strconv.Itoa(i))
		}
		close(ch)
		cancel()

		// Drain exactly like finder.DrainList: a fresh Push must wait for
		// the prior consumer to exit (and flush) before returning.
		drain := l.Push(context.Background())
		close(drain)

		count := l.TotalCount()
		require.NoError(t, l.Close())
		require.Equal(t, total, count, "iteration %d", iter)
	}
}

func focusIdx(t *testing.T, l *List) int {
	t.Helper()
	m, ok := l.Focus()
	require.True(t, ok)
	return m.Index()
}

func TestListFocusPreservedDuringAsyncStreaming(t *testing.T) {
	l := NewList(ListConfig{Interrupter: term.NopInterrupter()})
	defer l.Close()
	l.Resize(80, 40)

	// Make the sort branch active and force scoring to favor the exact match.
	l.Buffer().WriteString("zzz")
	l.Wait()

	// Initial batch: spread-out matches that all score below an exact "zzz".
	streamBatch(t, l, "z_z_z a", "z_z_z b", "z_z_z c", "z_z_z d")
	require.Equal(t, 4, l.MatchCount())

	// User navigates off the top; this latches userMovedFocus.
	require.True(t, l.FocusDown())
	selectedIdx := focusIdx(t, l)
	selectedOffset := l.FocusOffset()
	require.NotZero(t, selectedOffset, "precondition: focus moved off row 0")

	// More results arrive, including an exact match that would sort to the
	// top and reset focus to row 0 if re-ranking were not gated.
	streamBatch(t, l, "zzz", "z_z_z e")

	// Focus must stay on the user's selection, not snap to the new best match.
	assert.Equal(t, selectedIdx, focusIdx(t, l),
		"focused item changed while user had navigated")
	assert.Equal(t, selectedOffset, l.FocusOffset(),
		"focus offset snapped during streaming")

	// No data loss: the later items were appended.
	assert.Equal(t, 6, l.TotalCount())
	assert.Equal(t, 6, l.MatchCount())
}

func TestListAutoFocusTopBeforeUserNavigation(t *testing.T) {
	l := NewList(ListConfig{Interrupter: term.NopInterrupter()})
	defer l.Close()
	l.Resize(80, 40)

	l.Buffer().WriteString("zzz")
	l.Wait()

	streamBatch(t, l, "z_z_z a", "z_z_z b", "z_z_z c")
	require.Equal(t, 0, l.FocusOffset())

	// A later exact match should re-rank to the top and keep focus there,
	// because the user has not taken control.
	streamBatch(t, l, "zzz")

	assert.Equal(t, 0, l.FocusOffset(), "focus should track the best match")
	m, ok := l.Focus()
	require.True(t, ok)
	assert.Equal(t, "zzz", string(m.Data()))
}

func TestListFocusResetsAfterQueryChange(t *testing.T) {
	l := NewList(ListConfig{Interrupter: term.NopInterrupter()})
	defer l.Close()
	l.Resize(80, 40)

	l.Buffer().WriteString("zzz")
	l.Wait()

	streamBatch(t, l, "z_z_z a", "z_z_z b", "z_z_z c")
	require.True(t, l.FocusDown())
	require.NotZero(t, l.FocusOffset())

	// Changing the query rebuilds the list; auto-focus-top must resume.
	l.Buffer().WriteString("z")
	l.Wait()

	streamBatch(t, l, "zzzz", "z_z_z_z a")

	assert.Equal(t, 0, l.FocusOffset(),
		"auto-focus-top should resume after the result set is rebuilt")
	m, ok := l.Focus()
	require.True(t, ok)
	assert.Equal(t, "zzzz", string(m.Data()))
}

func TestListDraw(t *testing.T) {
	testListDraw(t, newSimpleList)
}

func testListDraw(t *testing.T, constructor listConstructor) {
	l, buf := constructor(ListConfig{})
	l.Resize(8, 4)
	defer l.Close()

	w := term.NewStringWriter(8, 4)

	tests := []struct {
		action   func()
		expected string
	}{{
		nil, `
     0/0
        
        
        `,
	}, {
		func() { l.PushSync([]byte("Safe Changes - Talaboman")) }, `
     1/1
Safe Cha
        
        `,
	}, {
		func() {
			for range 19 {
				l.PushSync([]byte("For the Time Being - Phonique"))
			}
		}, `
   20/20
Safe Cha
For the 
For the `,
	}, {
		func() {
			buf.WriteString("P")
			l.Wait()
		}, `
P  19/20
For the 
For the 
For the `,
	}, {
		func() {
			l.SetMinInputHeight(2)
		}, `
P       
   19/20
For the 
For the `,
	}, {
		func() {
			l.SetMinInputHeight(1)
			for range 8 {
				buf.WriteString("P")
			}
			l.Wait()
		}, `
PPPPPPPP
P   0/20
        
        `,
	},
	}

	for _, tcase := range tests {
		if err := w.Clear(term.Attributes{}); err != nil {
			t.Fatal(err)
		}

		if tcase.action != nil {
			tcase.action()
		}

		l.Draw(w)

		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}

		// for readability, we expected strings are written starting with \n
		expected := strings.TrimLeft(tcase.expected, "\n")
		assert.Equal(t, expected, w.String())
	}
}

func TestListDrawBottomSearchBar(t *testing.T) {
	testListDrawBottomSearchBar(t, newSimpleList)
}

func TestListDrawWideRunes(t *testing.T) {
	l, _ := newSimpleList(ListConfig{})
	defer l.Close()
	l.Resize(20, 4)
	w := term.NewStringWriter(20, 4)

	l.PushSync([]byte("café.go"))
	l.PushSync([]byte("\U000F0295 charlie"))

	require.NoError(t, w.Clear(term.Attributes{}))
	l.Draw(w)
	require.NoError(t, w.Flush())

	out := w.String()
	assert.Contains(t, out, "café.go")
	assert.Contains(t, out, "\U000F0295 charlie")
}

func TestListDrawCombiningMarks(t *testing.T) {
	const width = 20
	l, _ := newSimpleList(ListConfig{})
	defer l.Close()
	l.Resize(width, 3)
	w := term.NewStringWriter(width, 3)

	l.PushSync([]byte("cafe\u0301.go"))

	require.NoError(t, w.Clear(term.Attributes{}))
	l.Draw(w)

	// StringWriter.Flush renders Cell.Ch only, so the combining mark has
	// to be read off the cell itself. Row 0 is the match counter.
	cells := w.Cells()
	base := cells[width+3]
	assert.Equal(t, 'e', base.Ch)
	assert.Equal(t, []rune{'\u0301'}, base.CombiningRunes())
	assert.Equal(t, '.', cells[width+4].Ch)
}

func testListDrawBottomSearchBar(t *testing.T, constructor listConstructor) {
	l, buf := constructor(ListConfig{BottomSearchBar: true})
	l.Resize(8, 4)
	defer l.Close()

	w := term.NewStringWriter(8, 4)

	tests := []struct {
		action   func()
		expected string
	}{{
		nil, `
        
        
        
     0/0`,
	}, {
		func() { l.PushSync([]byte("Safe Changes - Talaboman")) }, `
Safe Cha
        
        
     1/1`,
	}, {
		func() {
			for range 19 {
				l.PushSync([]byte("For the Time Being - Phonique"))
			}
		}, `
For the 
For the 
For the 
   20/20`,
	}, {
		func() {
			buf.WriteString("P")
			l.Wait()
		}, `
For the 
For the 
For the 
P  19/20`,
	}, {
		func() {
			l.SetMinInputHeight(2)
		}, `
For the 
For the 
P       
   19/20`,
	}, {
		func() {
			l.SetMinInputHeight(1)
			for range 8 {
				buf.WriteString("P")
			}
			l.Wait()
		}, `
        
        
PPPPPPPP
P   0/20`,
	},
	}

	for _, tcase := range tests {
		if err := w.Clear(term.Attributes{}); err != nil {
			t.Fatal(err)
		}

		if tcase.action != nil {
			tcase.action()
		}

		l.Draw(w)

		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}

		// for readability, we expected strings are written starting with \n
		expected := strings.TrimLeft(tcase.expected, "\n")
		assert.Equal(t, expected, w.String())
	}
}

func TestListWait(t *testing.T) {
	testListWait(t, newSimpleList)
}

func TestListSyncSearch(t *testing.T) {
	t.Run("buffer edits update matches synchronously", func(t *testing.T) {
		l := NewList(ListConfig{SyncSearch: true})
		defer l.Close()

		l.PushSync([]byte("wasup"))
		l.PushSync([]byte("wasep"))
		l.PushSync([]byte("hello"))

		buf := l.Buffer()
		buf.WriteString("w")
		assert.Equal(t, 2, l.MatchCount())

		buf.WriteString("a")
		assert.Equal(t, 2, l.MatchCount())

		buf.WriteString("s")
		assert.Equal(t, 2, l.MatchCount())

		buf.WriteString("u")
		assert.Equal(t, 1, l.MatchCount())
		assertFocusEqual(t, l, []byte("wasup"))
	})
}

func testListWait(t *testing.T, constructor listConstructor) {
	t.Run("does not panic a new list", func(t *testing.T) {
		l, _ := constructor(ListConfig{})
		require.NotPanics(t, l.Wait)
		assert.NoError(t, l.Close())
	})
}

func TestListBuffer(t *testing.T) {
	t.Run("inserts on shared buffer are materialized on search buffer", func(t *testing.T) {
		l := NewList(ListConfig{})
		shared := l.Buffer()
		defer l.Close()

		shared.WriteString("blah")
		assert.Equal(t, "blah", l.searchBar.internalRead.String())
	})

	t.Run("deletes on search buffer are materialized on shared buffer", func(t *testing.T) {
		l := NewList(ListConfig{})
		defer l.Close()

		shared := l.Buffer()
		shared.WriteString("blah")
		assert.True(t, shared.TruncateFrom(term.Coordinates{X: 1}))
		assert.Equal(t, "b", l.searchBar.internalRead.String())
	})
}

func TestListElementAt(t *testing.T) {
	l := NewList(ListConfig{BottomSearchBar: true})
	l.Resize(8, 4)
	defer l.Close()

	l.PushSync([]byte("JT"))
	l.PushSync([]byte("Gladiator"))

	match1, _, ok := l.ElementAt(term.Coordinates{})
	require.True(t, ok)

	assert.Equal(t, 0, match1.Index())
	assert.Equal(t, "JT", string(match1.Data()))

	match2, _, ok := l.ElementAt(term.Coordinates{Y: 1, X: 7})
	require.True(t, ok)

	assert.Equal(t, 1, match2.Index())
	assert.Equal(t, "Gladiator", string(match2.Data()))

	match2, _, ok = l.ElementAt(term.Coordinates{Y: 2})
	require.False(t, ok)
}

func TestListLeak(t *testing.T) {
	testListLeak(t, newSimpleList)
}

func testListLeak(t *testing.T, constructor listConstructor) {
	t.Run("does not leak when Init + Close", func(t *testing.T) {
		l, _ := constructor(ListConfig{})
		assertNoLeaks(t, l)
	})
}

func TestListRemoveFocus(t *testing.T) {
	t.Run("remove focus removes item, shifts the ones below up and idices are continuos", func(t *testing.T) {
		l := NewList(ListConfig{BottomSearchBar: true})
		l.Resize(8, 4)
		defer l.Close()

		l.PushSync([]byte("echo 0"))
		l.PushSync([]byte("echo 1"))
		l.PushSync([]byte("echo 2"))
		l.PushSync([]byte("echo 3"))
		l.PushSync([]byte("echo 4"))

		l.FocusStart() // echo 0
		l.FocusDown()  // echo 1
		l.FocusDown()  // echo 2

		match, ok := l.Focus()
		require.True(t, ok)
		assert.Equal(t, match.idx, 2)
		assert.Equal(t, "echo 2", string(match.Data()))

		// after removing the focus you will get the element below (greater idx) in
		// focus, that will have shifted up to "fill the gap"
		ok = l.RemoveFocus()
		require.True(t, ok)
		match, ok = l.Focus()
		require.True(t, ok)
		assert.Equal(t, match.idx, 2)
		assert.Equal(t, "echo 3", string(match.Data()))

		// make sure the component list and value list are aligned
		assert.Equal(t, l.TotalCount(), len(l.input))
		assert.Equal(t, 4, len(l.input))

		// assert continuity of indices and correspondance between components list
		// and values list (`l.input`)
		var expectedIdx int
		l.list.Iterate(func(c tui.Component) {
			sr := c.(searchResultComponent)
			assert.Equal(t, expectedIdx, sr.idx)
			assert.Equal(t, sr.Match.Data(), l.input[expectedIdx])
			expectedIdx++
		})
	})
	t.Run("remove focus from start of list focuses one item down after removal", func(t *testing.T) {
		l := NewList(ListConfig{BottomSearchBar: true})
		l.Resize(8, 4)
		defer l.Close()

		l.PushSync([]byte("echo 0"))
		l.PushSync([]byte("echo 1"))
		l.PushSync([]byte("echo 2"))

		l.FocusStart() // echo 0
		ok := l.RemoveFocus()
		require.True(t, ok)
		match, ok := l.Focus()
		require.True(t, ok)
		assert.Equal(t, "echo 1", string(match.Data()))
		assert.Equal(t, 0, match.idx)
	})

	t.Run("remove focus from end of list focuses one item up after removal", func(t *testing.T) {
		l := NewList(ListConfig{BottomSearchBar: true})
		l.Resize(8, 4)
		defer l.Close()

		l.PushSync([]byte("echo 0"))
		l.PushSync([]byte("echo 1"))
		l.PushSync([]byte("echo 2"))

		l.FocusEnd() // echo 2
		ok := l.RemoveFocus()
		require.True(t, ok)
		match, ok := l.Focus()
		require.True(t, ok)
		assert.Equal(t, "echo 1", string(match.Data()))
		assert.Equal(t, 1, match.idx)
	})

	t.Run("remove focus after removing all items doesn't panic and return false", func(t *testing.T) {
		l := NewList(ListConfig{BottomSearchBar: true})
		l.Resize(8, 4)
		defer l.Close()

		l.PushSync([]byte("echo 0"))
		l.PushSync([]byte("echo 1"))
		l.PushSync([]byte("echo 2"))

		l.FocusStart() // echo 2
		for range 3 {
			ok := l.RemoveFocus()
			require.True(t, ok)
		}

		assert.NotPanics(t, func() {
			ok := l.RemoveFocus()
			assert.False(t, ok)
		})
	})

	t.Run("remove focus on an empty list doesn't panic", func(t *testing.T) {
		l := NewList(ListConfig{BottomSearchBar: true})
		l.Resize(8, 4)
		defer l.Close()

		assert.NotPanics(t, func() {
			ok := l.RemoveFocus()
			assert.False(t, ok)
		})
	})
}

func BenchmarkHandleSearch10(b *testing.B) {
	benchmarkHandleSearch(b, 10, false)
}
func BenchmarkHandleSearch100(b *testing.B) {
	benchmarkHandleSearch(b, 100, false)
}
func BenchmarkHandleSearch1000(b *testing.B) {
	benchmarkHandleSearch(b, 1000, false)
}
func BenchmarkHandleSearch1000000(b *testing.B) {
	benchmarkHandleSearch(b, 1000000, false)
}

func BenchmarkHandleSearchBottomSearchBar10(b *testing.B) {
	benchmarkHandleSearch(b, 10, true)
}
func BenchmarkHandleSearchBottomSearchBar100(b *testing.B) {
	benchmarkHandleSearch(b, 100, true)
}
func BenchmarkHandleSearchBottomSearchBar1000(b *testing.B) {
	benchmarkHandleSearch(b, 1000, true)
}
func BenchmarkHandleSearchBottomSearchBar1000000(b *testing.B) {
	benchmarkHandleSearch(b, 1000000, true)
}

func BenchmarkHandlePush10(b *testing.B) {
	benchmarkPush(b, 10, false)
}
func BenchmarkHandlePush100(b *testing.B) {
	benchmarkPush(b, 100, false)
}
func BenchmarkHandlePush1000(b *testing.B) {
	benchmarkPush(b, 1000, false)
}
func BenchmarkHandlePush1000000(b *testing.B) {
	benchmarkPush(b, 1000000, false)
}

func benchmarkPush(b *testing.B, n int, bottomSearchBar bool) {
	const sample = `2022-06-17 15:45:24.985	WARNING	[-]	-	msg: 3 errors occurred: Failed to load \"key_bindings.<m-k>\": invalid key: '<m-k>'; Failed to load \"key_bindings.<m-j>\": invalid key: '<m-j>'; Failed to load \"browser.frameunion_charset\": type is invalid
2022-06-17 15:45:24.985	WARNING	[-]	-	msg: 3 errors occurred: Failed to load \"key_bindings.<m-k>\": invalid key: '<m-k>'; Failed to load \"key_bindings.<m-j>\": invalid key: '<m-j>'; Failed to load \"browser.frameunion_charset\": type is invalid
2022-06-17 15:45:24.985	DEBUG	[-]	-	msg: starting extension
2022-06-17 15:45:24.986	DEBUG	[-]	-	msg: extension started
2022-06-17 15:45:24.986	DEBUG	[-]	-	msg: waiting for RPC address
2022-06-17 15:45:25.003	DEBUG	[-]	extension_fuzzy_file	msg: extension address
2022-06-17 15:45:25.003	DEBUG	[-]	-	msg: using extension
2022-06-17 15:45:25.003	INFO	[-]	extension_fuzzy_file	msg: listen tcp 127.0.0.1:6061: bind: address already in use
2022-06-17 15:45:25.004	TRACE	[-]	stdio	msg: waiting for stdio data
2022-06-17 15:45:25.004	DEBUG	[-]	-	msg: starting extension`
	lines := bytes.Split([]byte(sample), []byte{'\n'})
	data := make([][]byte, n)
	for i := 1; i < int(max(1, float64(n/len(lines)))); i++ {
		for _, line := range lines {
			data = append(data, line)
		}
	}

	l := NewList(ListConfig{BottomSearchBar: bottomSearchBar})
	l.Resize(300, 200)
	l.cancelSearch = func() {}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.DataReset()
		ch := l.Push(ctx)
		for _, line := range data {
			ch <- line
		}
		close(ch)
		l.Wait()
	}
}

func benchmarkHandleSearch(b *testing.B, n int, bottomSearchBar bool) {
	const sample = `2022-06-17 15:45:24.985	WARNING	[-]	-	msg: 3 errors occurred: Failed to load \"key_bindings.<m-k>\": invalid key: '<m-k>'; Failed to load \"key_bindings.<m-j>\": invalid key: '<m-j>'; Failed to load \"browser.frameunion_charset\": type is invalid
2022-06-17 15:45:24.985	WARNING	[-]	-	msg: 3 errors occurred: Failed to load \"key_bindings.<m-k>\": invalid key: '<m-k>'; Failed to load \"key_bindings.<m-j>\": invalid key: '<m-j>'; Failed to load \"browser.frameunion_charset\": type is invalid
2022-06-17 15:45:24.985	DEBUG	[-]	-	msg: starting extension
2022-06-17 15:45:24.986	DEBUG	[-]	-	msg: extension started
2022-06-17 15:45:24.986	DEBUG	[-]	-	msg: waiting for RPC address
2022-06-17 15:45:25.003	DEBUG	[-]	extension_fuzzy_file	msg: extension address
2022-06-17 15:45:25.003	DEBUG	[-]	-	msg: using extension
2022-06-17 15:45:25.003	INFO	[-]	extension_fuzzy_file	msg: listen tcp 127.0.0.1:6061: bind: address already in use
2022-06-17 15:45:25.004	TRACE	[-]	stdio	msg: waiting for stdio data
2022-06-17 15:45:25.004	DEBUG	[-]	-	msg: starting extension`
	lines := bytes.Split([]byte(sample), []byte{'\n'})
	data := make([][]byte, n)
	for i := 1; i < int(math.Max(1, float64(n/len(lines)))); i++ {
		for _, line := range lines {
			data = append(data, line)
		}
	}

	l := NewList(ListConfig{BottomSearchBar: bottomSearchBar})
	w := term.NoopWriter{}
	l.Resize(300, 200)
	l.cancelSearch = func() {}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// cannot call asyncSearch for benchmark
		// in order to measure handleSearch perf, we need to
		// run all the logic synchronously
		l.mu.Lock()
		l.cancelSearch()
		ctx := context.Background()
		l.waitSearchCtx, l.cancelSearch = context.WithCancel(ctx)
		l.list.Reset()
		l.mu.Unlock()
		l.handleSearch(context.Background(), func() {}, data, "D")
		l.Draw(w)
	}
}

// benchmarkScroll measures the steady-state cost of scrolling an
// already-populated list: the list of n rows is built once, then the
// timed loop only moves the focus down one row and redraws the visible
// viewport. This isolates the scroll/draw hot path from the one-time
// ingest+search cost, and is where the "scrolling allocates nothing on
// the heap" claim is verified via -benchmem.
func benchmarkScroll(b *testing.B, n int) {
	const sample = `2022-06-17 15:45:24.985	DEBUG	[-]	-	msg: starting extension
2022-06-17 15:45:24.986	DEBUG	[-]	-	msg: extension started
2022-06-17 15:45:24.986	DEBUG	[-]	-	msg: waiting for RPC address
2022-06-17 15:45:25.003	DEBUG	[-]	extension_fuzzy_file	msg: extension address
2022-06-17 15:45:25.004	TRACE	[-]	stdio	msg: waiting for stdio data`
	lines := bytes.Split([]byte(sample), []byte{'\n'})
	data := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		data = append(data, lines[i%len(lines)])
	}

	l := NewList(ListConfig{})
	l.Resize(300, 200)
	l.cancelSearch = func() {}

	// Build the list once, outside the timed loop.
	ctx := context.Background()
	ch := l.Push(ctx)
	for _, line := range data {
		ch <- line
	}
	close(ch)
	l.Wait()

	w := term.NoopWriter{}
	l.Draw(w) // warm the first draw (LazyBytes is lazy until first Draw)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !l.FocusDown() {
			l.FocusUp() // bounce at the bottom so we keep scrolling
		}
		l.Draw(w)
	}
}

func BenchmarkScroll1000(b *testing.B)    { benchmarkScroll(b, 1000) }
func BenchmarkScroll1000000(b *testing.B) { benchmarkScroll(b, 1000000) }

// benchmarkDrawRows measures a single redraw of a full viewport of
// result rows, with no scrolling or searching in the timed loop. Row
// rendering is where the ASCII fast path lives, so this is the
// benchmark that resolves changes to it; benchmarkScroll spends most of
// its samples in the writer and the scheduler and drowns them out.
func benchmarkDrawRows(b *testing.B, row string) {
	const rows = 200
	l := NewList(ListConfig{})
	l.Resize(300, rows)
	l.cancelSearch = func() {}

	ch := l.Push(context.Background())
	for range rows {
		ch <- []byte(row)
	}
	close(ch)
	l.Wait()

	w := term.NoopWriter{}
	l.Draw(w)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		l.Draw(w)
	}
}

func BenchmarkDrawRowsASCII(b *testing.B) {
	benchmarkDrawRows(b,
		"2022-06-17 15:45:25.003\tDEBUG\t[-]\textension_fuzzy_file\tmsg: address")
}

func BenchmarkDrawRowsWide(b *testing.B) {
	benchmarkDrawRows(b,
		"2022-06-17 15:45:25.003\tDEBUG\t[—]\textension_fuzzy_filé\tmsg: address")
}
