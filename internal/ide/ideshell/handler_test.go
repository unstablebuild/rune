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

package ideshell

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

const (
	testDocID  = "test-shell-history"
	testWidthH = 30
	testHeight = 12
)

func newTestHandler(t *testing.T, items []string) *Handler {
	return newTestHandlerFull(t, items, 100, nil)
}

func newTestHandlerFull(
	t *testing.T, items []string, maxHist int,
	register func(*CommandRegistry),
) *Handler {
	t.Helper()
	svc := storagestub.NewInMemoryService()
	if len(items) > 0 {
		require.NoError(t, svc.Create(
			context.Background(), testDocID,
			&historyDoc{Items: items, Version: 1},
		))
	}
	q := &tickQueue{}
	h, r := New(
		q.schedule,
		term.NopInterrupter(),
		stubEditor{},
		Config{
			Storage:           svc,
			HistoryDocumentID: testDocID,
			MaxHistory:        maxHist,
		},
	)
	if register != nil {
		register(r)
	}
	testTicks.Store(h, q)
	t.Cleanup(func() { testTicks.Delete(h) })
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// tickQueue records functions scheduled via scheduleNextTick so tests
// can run them deterministically, mirroring the real event loop draining
// pending ticks. The completion stream schedules single/zero-match
// resolution this way.
type tickQueue struct {
	mu    sync.Mutex
	funcs []func()
}

func (q *tickQueue) schedule(fn func()) bool {
	q.mu.Lock()
	q.funcs = append(q.funcs, fn)
	q.mu.Unlock()
	return true
}

func (q *tickQueue) drain() {
	for {
		q.mu.Lock()
		if len(q.funcs) == 0 {
			q.mu.Unlock()
			return
		}
		fn := q.funcs[0]
		q.funcs = q.funcs[1:]
		q.mu.Unlock()
		fn()
	}
}

// testTicks maps a test handler to its pending-tick queue so helpers
// can drain scheduled work without threading the queue through every
// call site.
var testTicks sync.Map

// drainTicks runs any scheduled-next-tick callbacks registered for h.
func drainTicks(h *Handler) {
	if q, ok := testTicks.Load(h); ok {
		q.(*tickQueue).drain()
	}
}

// stubCmd is a no-op CommandHandler used for command-name completion
// tests. Its Complete method returns no candidates so the registry
// drives completion entirely by name matching.
type stubCmd struct{}

func (stubCmd) HandleCommand(
	context.Context, repl.Command, repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.Empty[component.Responsive](), nil
}

func (stubCmd) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (stubCmd) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.Empty[component.Responsive](), nil
}

// argCmd returns a fixed list of candidates for any argument
// completion request. It is used to exercise tab completion of
// command arguments (as opposed to command names).
type argCmd struct{ candidates []string }

func (argCmd) HandleCommand(
	context.Context, repl.Command, repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.Empty[component.Responsive](), nil
}

func (a argCmd) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(a.candidates), nil
}

func (argCmd) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.Empty[component.Responsive](), nil
}

// Common command registration fixtures used by the TestHandler cases
// below. Defined as variables so each table-driven case can refer to
// them without re-declaring three identical handlers.
var (
	// threeFoos registers three command names that all share a "fo"
	// prefix, used to drive multi-candidate command-name completion.
	threeFoos = func(r *CommandRegistry) {
		r.Register("foo", "do foo", stubCmd{})
		r.Register("foobar", "do foobar", stubCmd{})
		r.Register("foobaz", "do foobaz", stubCmd{})
	}
	// twoArgs registers a single command "g" whose argument completer
	// returns two distinct candidates with no shared prefix.
	twoArgs = func(r *CommandRegistry) {
		r.Register("g", "", argCmd{candidates: []string{"alpha", "beta"}})
	}
	// twoAls registers a command whose argument candidates share a
	// non-trivial prefix, so partial-prefix replacement can be tested.
	twoAls = func(r *CommandRegistry) {
		r.Register("g", "", argCmd{candidates: []string{"alpha", "alright"}})
	}
	// singleAlpha registers a single command name so that tab triggers
	// the inline single-candidate fast path rather than the overlay.
	singleAlpha = func(r *CommandRegistry) {
		r.Register("alpha", "", stubCmd{})
	}
)

// longX returns n 'x' characters, sized to drive multi-line wrap
// scenarios.
func longX(n int) string { return strings.Repeat("x", n) }

// manyEntries returns n synthetic history entries used to exercise
// the overlay's row cap.
func manyEntries(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("entry-%02d", i)
	}
	return out
}

// TestHandler exercises the IDE shell handler's reverse-history search
// and tab-completion overlays end-to-end via
// handlertest.RunHandlerSequence so each case asserts against the
// literal terminal output. The fixtures share a common Handler shape:
// optional persisted history items, an optional MaxHistory cap, and an
// optional command-registry registration callback for completion
// scenarios.
type handlerCase struct {
	name     string
	items    []string
	maxHist  int
	register func(*CommandRegistry)
	sequence string
	expected string
}

func handlerTestCases() []handlerCase {
	return []handlerCase{{
		name:     "initial draw shows shell prompt",
		items:    []string{"alpha", "beta", "charlie"},
		sequence: "",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> ▐                           `,
	}, {
		name:     "<c-r> opens overlay listing newest history first",
		items:    []string{"alpha", "beta", "charlie"},
		sequence: "<c-r>",
		expected: `                              
                              
                              
                              
                              
                              
                              
  charlie                     
  beta                        
  alpha                       
                              
> ▐                        3/3`,
	}, {
		name:     "typed query narrows matches",
		items:    []string{"git status", "ls -la", "make test"},
		sequence: "<c-r>make",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
  make test                   
                              
> make▐                    1/3`,
	}, {
		name:     "<c-r> seeds query from inputbox text",
		items:    []string{"alpha", "beta", "charlie"},
		sequence: "bet<c-r>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
  beta                        
                              
> bet▐                     1/3`,
	}, {
		name:     "<backspace> shrinks query",
		items:    []string{"abc", "abd"},
		sequence: "<c-r>abc<backspace>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
  abd                         
  abc                         
                              
> ab▐                      2/2`,
	}, {
		name:     "<backspace> on empty query is a noop",
		items:    []string{"alpha"},
		sequence: "<c-r><backspace>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
  alpha                       
                              
> ▐                        1/1`,
	}, {
		name:     "<space> is included in the query",
		items:    []string{"git log", "git status"},
		sequence: "<c-r>git<space>l",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
  git log                     
                              
> git l▐                   1/2`,
	}, {
		name:     "uppercase query characters narrow matches",
		items:    []string{"Alpha", "alpha"},
		sequence: "<c-r>A",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
  alpha                       
  Alpha                       
                              
> A▐                       2/2`,
	}, {
		name:     "query that matches nothing shows 0 matches",
		items:    []string{"alpha", "beta"},
		sequence: "<c-r>zz",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> zz▐                      0/2`,
	}, {
		name:     "<enter> accepts focused entry into prompt",
		items:    []string{"alpha", "beta", "charlie"},
		sequence: "<c-r><enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> charlie▐                    `,
	}, {
		name:     "<tab> also accepts focused entry into prompt",
		items:    []string{"echo hi", "ls -la"},
		sequence: "<c-r><tab>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> ls -la▐                     `,
	}, {
		name:     "<c-j> moves focus down before <enter> accepts",
		items:    []string{"one", "two", "three"},
		sequence: "<c-r><c-j><enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> two▐                        `,
	}, {
		name:     "<c-k> moves focus back up before <enter> accepts",
		items:    []string{"one", "two", "three"},
		sequence: "<c-r><c-j><c-j><c-k><enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> two▐                        `,
	}, {
		name:     "<down> and <up> also navigate the overlay",
		items:    []string{"one", "two", "three"},
		sequence: "<c-r><down><down><up><enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> two▐                        `,
	}, {
		name:     "repeated <c-r> moves focus down within overlay",
		items:    []string{"one", "two", "three"},
		sequence: "<c-r><c-r><enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> two▐                        `,
	}, {
		name: "accepting clears any pre-existing prompt text " +
			"before inserting entry",
		items:    []string{"hello-entry"},
		sequence: "hello<c-r><enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> hello-entry▐                `,
	}, {
		name:     "accepting with no matches leaves prompt untouched",
		items:    []string{"alpha"},
		sequence: "pq<c-r>z<enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> pq▐                         `,
	}, {
		name:     "<esc> cancels overlay and restores prompt",
		items:    []string{"echo hi"},
		sequence: "<c-r><esc>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> ▐                           `,
	}, {
		name:     "<c-g> cancels overlay and restores prompt",
		items:    []string{"echo hi"},
		sequence: "<c-r><c-g>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> ▐                           `,
	}, {
		name:     "<c-c> cancels overlay and restores prompt",
		items:    []string{"echo"},
		sequence: "<c-r><c-c>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> ▐                           `,
	}, {
		name:     "cancelling preserves any pre-existing prompt text",
		items:    []string{"alpha"},
		sequence: "hi<c-r><esc>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> hi▐                         `,
	}, {
		name: "characters typed after cancel reach the prompt, " +
			"not the closed overlay",
		items:    []string{"ls -la"},
		sequence: "<c-r><esc>ab",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> ab▐                         `,
	}, {
		name: "characters typed inside the overlay do not reach the " +
			"underlying prompt",
		items:    []string{"alpha"},
		sequence: "<c-r>q<esc>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> ▐                           `,
	}, {
		name: "re-opening overlay after an accept seeds the " +
			"query with the accepted entry",
		items:    []string{"alpha", "beta", "charlie"},
		sequence: "<c-r><enter><c-r>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
  charlie                     
                              
> charlie▐                 1/3`,
	}, {
		name:     "without storage <c-r> opens an empty overlay",
		items:    nil,
		sequence: "<c-r>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> ▐                        0/0`,
	}, {
		name: "typing a query with empty history shows " +
			"0 of 0 matches and no entries",
		items:    nil,
		sequence: "<c-r>x",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> x▐                       0/0`,
	}, {
		name: "MaxHistory truncates the oldest entries when " +
			"loading the overlay",
		items:    []string{"old", "mid", "new"},
		maxHist:  2,
		sequence: "<c-r>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
  new                         
  mid                         
                              
> ▐                        2/2`,
	}, {
		name: "<enter> on a focused completion candidate replaces " +
			"only the completed word",
		register: threeFoos,
		sequence: "fo<tab>b<enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> foobar▐                     `,
	}, {
		name: "<tab> in the completion overlay also accepts the " +
			"focused candidate",
		register: threeFoos,
		sequence: "fo<tab><tab>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> foo▐                        `,
	}, {
		name: "<esc> cancels the completion overlay and leaves the " +
			"prompt text untouched",
		register: threeFoos,
		sequence: "fo<tab><esc>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> fo▐                         `,
	}, {
		name:     "<c-g> also cancels the completion overlay",
		register: threeFoos,
		sequence: "fo<tab><c-g>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> fo▐                         `,
	}, {
		name: "single completion candidate is applied inline " +
			"without opening the overlay",
		register: singleAlpha,
		sequence: "al<tab>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> alpha▐                      `,
	}, {
		name:     "<tab> with no completion candidates is a no-op",
		register: threeFoos,
		sequence: "zz<tab>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> zz▐                         `,
	}, {
		name: "<tab> after a space opens an argument completion " +
			"overlay",
		register: twoArgs,
		sequence: "g<space><tab>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
  alpha                       
  beta                        
                              
> g ▐                         `,
	}, {
		name: "accepting an argument completion candidate appends " +
			"it after the existing line",
		register: twoArgs,
		sequence: "g<space><tab><enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> g alpha▐                    `,
	}, {
		name: "<tab> on a partially-typed argument shows matching " +
			"candidates",
		register: twoAls,
		sequence: "g<space>al<tab>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
  alpha                       
  alright                     
                              
> g al▐                       `,
	}, {
		name: "accepting a partial argument completion replaces " +
			"only the partial word",
		register: twoAls,
		sequence: "g<space>al<tab><enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> g alpha▐                    `,
	}, {
		name: "<c-j> moves focus down before <enter> accepts in " +
			"the completion overlay",
		register: threeFoos,
		sequence: "fo<tab><c-j><enter>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> foobar▐                     `,
	}, {
		name: "<c-r> still opens history search even when the " +
			"prompt already has typed text",
		register: threeFoos,
		sequence: "fo<c-r>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> fo▐                      0/0`,
	}, {
		// Width is 30 and the prompt is "> ", so each input
		// line holds 28 runes. 90 'x' wraps to 4 lines.
		name: "input that wraps over multiple lines without overlay",
		sequence: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" +
			"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
> xxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xx▐                           `,
	}, {
		name: "opening overlay with empty history pushes a " +
			"multi-line input upward to make room",
		sequence: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" +
			"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx<c-r>",
		expected: `                              
                              
                              
                              
                              
> xxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
                              
                              
                              
>                             `,
	}, {
		name: "opening overlay with history above a multi-line " +
			"input keeps both the input and the candidates visible",
		items: []string{"alpha", "beta", "charlie"},
		sequence: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" +
			"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx<c-r>",
		expected: `                              
                              
                              
                              
                              
> xxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
                              
                              
                              
>                             `,
	}, {
		name: "cancelling the overlay restores the original " +
			"multi-line input layout",
		sequence: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" +
			"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx<c-r><esc>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
> xxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xx▐                           `,
	}, {
		// 30 entries — the overlay caps the visible list at
		// searchOverlayMaxRows (10), reserving the top of the
		// screen for the prompt.
		name:     "history overlay caps visible list at 10 rows",
		items:    manyEntries(30),
		sequence: "<c-r>",
		expected: `                              
  entry-29                    
  entry-28                    
  entry-27                    
  entry-26                    
  entry-25                    
  entry-24                    
  entry-23                    
  entry-22                    
  entry-21                    
  entry-20                    
> ▐                      30/30`,
	}, {
		// Input that is longer than the entire viewport. The
		// inner repl scrolls/truncates so only the bottom of the
		// input is visible — the overlay still appears at the
		// bottom and pushes the visible portion up.
		name: "input larger than the screen is truncated to keep " +
			"the prompt edge visible",
		sequence: longX(300),
		expected: `                              
> xxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xx▐                           `,
	}, {
		name: "opening the overlay over an oversized input " +
			"truncates the input from the top to fit the candidates",
		items:    []string{"alpha"},
		sequence: longX(300) + "<c-r>",
		expected: `xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
                              
                              
                              
>                             `,
	}, {
		// Argument completion with a wrapped first line — the
		// overlay still appears at the bottom and the partial
		// input remains visible.
		name: "argument completion overlay appears below a wrapped " +
			"first line of input",
		register: twoArgs,
		sequence: "g<space>" +
			"xxxxxxxxxxxxxxxxxxxxxxxxxx" +
			"<tab>",
		expected: `                              
                              
                              
                              
                              
                              
                              
  alpha                       
  beta                        
                              
> g xxxxxxxxxxxxxxxxxxxxxxxxxx
▐                             `,
	}, {
		// Bug 1: a partial word typed before <tab> stayed in the
		// inputbox; opening completion must keep it visible.
		name: "completion overlay keeps the partial word in the " +
			"inputbox",
		register: threeFoos,
		sequence: "fo<tab>",
		// the inputbox should still show "> fo" with the cursor
		// after it; the candidate rows render above.
		expected: `                              
                              
                              
                              
                              
                              
                              
  foo                         
  foobar                      
  foobaz                      
                              
> fo▐                         `,
	}, {
		// Bug 1 (continued): typing more chars after <tab>
		// extends the inputbox AND narrows the list.
		name: "typing in the completion overlay extends the " +
			"inputbox word and narrows the list",
		register: threeFoos,
		sequence: "fo<tab>b",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
  foobar                      
  foobaz                      
                              
> fob▐                        `,
	}, {
		// Bug 2: completing the first arg, then space, then tab
		// must not erase the first arg.
		name: "completing arg then typing space then tab keeps the " +
			"prior args visible",
		register: func(r *CommandRegistry) {
			r.Register("g", "", argCmd{
				candidates: []string{"alpha", "beta"},
			})
		},
		sequence: "g<space><tab><tab><space><tab>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
  alpha                       
  beta                        
                              
> g alpha ▐                   `,
	}, {
		// Bug 2 (continued): the list filter for the new tab is
		// driven only by the partial word at the cursor, not by
		// the previous arg.
		name: "argument completion filter ignores prior accepted " +
			"arguments",
		register: func(r *CommandRegistry) {
			r.Register("g", "", argCmd{
				candidates: []string{"alpha", "beta"},
			})
		},
		sequence: "g<space><tab><tab><space><tab>b",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
  beta                        
                              
> g alpha b▐                  `,
	}, {
		// Backspace to delete a typed-after-tab character keeps
		// the inputbox in sync.
		name: "backspace in completion overlay shrinks the " +
			"inputbox word",
		register: threeFoos,
		sequence: "fo<tab>b<backspace>",
		expected: `                              
                              
                              
                              
                              
                              
                              
  foo                         
  foobar                      
  foobaz                      
                              
> fo▐                         `,
	}, {
		// Pressing space inside the completion overlay closes
		// the overlay and forwards the space to the inputbox
		// (since the user is starting a new argument).
		name: "space inside the completion overlay closes it and " +
			"reaches the inputbox",
		register: threeFoos,
		sequence: "fo<tab><space>",
		expected: `                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
                              
> fo ▐                        `,
	}, {
		// Regression: <c-r> over a wrapped multi-line input
		// should keep the wrapped input visible above the
		// candidate band, the same way <tab> does. The search
		// bar with the user's query lives on the bottom row.
		name: "history overlay does not overdraw a tall " +
			"wrapped input",
		items: []string{"alpha", "beta", "charlie"},
		sequence: "g<space>" +
			longX(28*3) +
			"<c-r>",
		expected: `                              
                              
                              
                              
                              
                              
> g xxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
                              
                              
                              
>                             `,
	}, {
		// Regression: opening completion when the prompt line
		// has wrapped over several rows must not overdraw the
		// existing input. The candidate band lands below the
		// (now-shrunk) inputbox and the wrapped tail of the
		// input is still visible.
		name: "completion overlay does not overdraw a tall " +
			"wrapped input",
		register: twoArgs,
		sequence: "g<space>" +
			longX(28*3) +
			"<tab>",
		expected: `                              
                              
                              
                              
                              
                              
  alpha                       
  beta                        
                              
> g xxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
xxxxxxxxxxxxxxxxxxxxxxxxxxxx▐ `,
	}}
}

func TestHandler(t *testing.T) {
	for _, tc := range handlerTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			maxHist := tc.maxHist
			if maxHist == 0 {
				maxHist = 100
			}
			h := newTestHandlerFull(t, tc.items, maxHist, tc.register)
			runShellSequence(t, h, tc.sequence, tc.expected)
		})
	}
}

func TestCompletionAcceptsFirstCandidateBeforeSettlementTick(t *testing.T) {
	h := newTestHandlerFull(t, nil, 100, twoArgs)
	h.Resize(testWidthH, testHeight)

	keys, err := term.ParseKeys("g<space><tab>")
	require.NoError(t, err)
	for _, key := range keys {
		_, _ = h.Handle(term.Event{
			Ch: key.Ch, Mod: key.Mod, Key: key.Key, Type: term.EventKey,
		})
	}
	h.WaitCompletion()

	_, _ = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.Equal(t, "g alpha", h.editBuf.String())
}

// runShellSequence drives the shell handler through an input sequence
// and compares the rendered output. Unlike handlertest.RunHandlerSequence
// it waits for any in-flight completion stream to settle before drawing,
// since completion candidates are now pushed into the overlay off the
// event loop.
func runShellSequence(t *testing.T, h *Handler, sequence, expected string) {
	t.Helper()
	h.Resize(testWidthH, testHeight)
	keys, err := term.ParseKeys(sequence)
	require.NoError(t, err)
	for _, key := range keys {
		h.Handle(term.Event{
			Ch: key.Ch, Mod: key.Mod, Key: key.Key, Type: term.EventKey,
		})
		h.WaitCompletion()
		drainTicks(h)
	}
	w := term.NewStringWriter(testWidthH, testHeight)
	h.Draw(w)
	if cursor, _, ok := h.Cursor(); ok {
		w.SetCursor(cursor)
	}
	require.NoError(t, w.Flush())
	assert.Equal(t, expected, w.String(), "input: %s", sequence)
}

func TestHandlerLoadHistoryReturnsNewestFirst(t *testing.T) {
	h := newTestHandler(t, []string{"oldest", "middle", "newest"})
	assert.Equal(t,
		[]string{"newest", "middle", "oldest"}, h.loadHistory(),
	)
}

func TestHandlerMouseSelection(t *testing.T) {
	for _, tc := range mouseSelectionCases() {
		t.Run(tc.name, func(t *testing.T) {
			w, h := tc.width, tc.height
			if w == 0 {
				w = mouseTestWidth
			}
			if h == 0 {
				h = mouseTestHeight
			}
			hd, out := newMouseTestHandler(t, w, h, tc.lines)
			for _, ev := range tc.gesture(out, w) {
				hd.Handle(ev)
			}
			sel, ok := hd.Selection()
			assert.Equal(t, tc.wantOK, ok, "Selection() ok mismatch")
			if tc.wantOK {
				assert.Equal(t, tc.wantSel, sel, "Selection() text mismatch")
			}
		})
	}
}

func TestHandlerMouseSelectionRendersReverse(t *testing.T) {
	const w, h = mouseTestWidth, mouseTestHeight
	hd, out := newMouseTestHandler(t, w, h, []string{"alpha bravo"})
	y := rowOf(t, out, w, 'a')
	// Drag over exactly "alpha" (columns 0..4) so the highlighted span
	// has a precise inclusive boundary, independent of how the output
	// row is padded.
	for _, ev := range clickDrag(0, y, 4, y) {
		hd.Handle(ev)
	}
	rw := term.NewStringWriter(w, h)
	hd.Draw(rw)
	_ = rw.Flush()
	cells := rw.Cells()
	// Columns 0..4 ("alpha") are highlighted; column 5 (the space
	// after) is not.
	for x := range 5 {
		assert.NotZero(t, cells[y*w+x].Attrs&term.AttrReverse,
			"cell (%d,%d) inside selection missing AttrReverse", x, y)
	}
	assert.Zero(t, cells[y*w+5].Attrs&term.AttrReverse,
		"cell past selection should not carry AttrReverse")
}

func TestHandlerMouseWheelDoesNotSelect(t *testing.T) {
	hd, _ := newMouseTestHandler(t, mouseTestWidth, 6,
		[]string{"l0", "l1", "l2", "l3", "l4", "l5", "l6", "l7"})
	hd.Handle(mouseEv(2, 0, term.MouseWheelUp))
	hd.Handle(mouseEv(2, 0, term.MouseWheelDown))
	_, ok := hd.Selection()
	assert.False(t, ok, "wheel scroll must not create a selection")
}

// ----- mouse selection fixtures & helpers -----

const (
	mouseTestWidth  = 30
	mouseTestHeight = 12
)

// mouseCase is one table entry for TestHandlerMouseSelection. gesture
// receives the rendered framebuffer so it can locate output rows, and
// returns the events to feed to the handler.
type mouseCase struct {
	name    string
	width   int
	height  int
	lines   []string
	gesture func(out *term.StringWriter, w int) []term.Event
	wantSel string
	wantOK  bool
}

func mouseSelectionCases() []mouseCase {
	// atRow finds the row of the given rune; used by gestures that need
	// a known output line.
	return []mouseCase{
		{
			name:  "triple-click selects whole line",
			lines: []string{"alpha bravo", "charlie delta"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				return tripleClick(2, rowOf(nil, out, w, 'a'))
			},
			wantSel: "alpha bravo",
			wantOK:  true,
		},
		{
			name:  "double-click selects word under cursor",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				return doubleClick(6, rowOf(nil, out, w, 'b'))
			},
			wantSel: "bravo",
			wantOK:  true,
		},
		{
			name:  "double-click on first word",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				return doubleClick(0, rowOf(nil, out, w, 'a'))
			},
			wantSel: "alpha",
			wantOK:  true,
		},
		{
			name:  "click-drag selects partial range",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				y := rowOf(nil, out, w, 'a')
				return clickDrag(0, y, 4, y)
			},
			wantSel: "alpha",
			wantOK:  true,
		},
		{
			name:  "click-drag across full line",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				y := rowOf(nil, out, w, 'a')
				return clickDrag(0, y, 10, y)
			},
			wantSel: "alpha bravo",
			wantOK:  true,
		},
		{
			name:  "click-drag spanning two rows",
			lines: []string{"alpha bravo", "charlie delta"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				y0 := rowOf(nil, out, w, 'a')
				return clickDrag(0, y0, 6, y0+1)
			},
			wantSel: "alpha bravo\ncharlie",
			wantOK:  true,
		},
		{
			// Single click only sets an anchor; nothing is selected.
			name:  "single click yields no selection",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				y := rowOf(nil, out, w, 'a')
				return []term.Event{
					mouseEv(2, y, term.MouseLeft),
					mouseEv(2, y, term.MouseRelease),
				}
			},
			wantOK: false,
		},
		{
			name:  "double-click on whitespace selects nothing",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				// Column 5 is the space between the two words.
				return doubleClick(5, rowOf(nil, out, w, 'a'))
			},
			wantOK: false,
		},
		{
			name:  "double-click past end-of-line selects nothing",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				return doubleClick(25, rowOf(nil, out, w, 'a'))
			},
			wantOK: false,
		},
		{
			name:  "triple-click on empty row selects nothing",
			lines: []string{"alpha"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				// Row 0 is blank (output is bottom-aligned).
				return tripleClick(2, 0)
			},
			wantOK: false,
		},
		{
			name:  "negative coordinates do not panic and select nothing",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				return doubleClick(-5, -3)
			},
			wantOK: false,
		},
		{
			name:  "drag starting off-screen left clamps to line start",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				y := rowOf(nil, out, w, 'a')
				return clickDrag(-10, y, 4, y)
			},
			wantSel: "alpha",
			wantOK:  true,
		},
		{
			name:  "drag ending past right edge clamps to line end",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				y := rowOf(nil, out, w, 'a')
				return clickDrag(0, y, w+50, y)
			},
			wantSel: "alpha bravo",
			wantOK:  true,
		},
		{
			name:  "drag ending below output band clamps to last row",
			lines: []string{"alpha bravo", "charlie delta"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				y0 := rowOf(nil, out, w, 'a')
				// Release far below the grid; Y clamps to the last
				// output row, X stays at 5 → "charli" on row two.
				return clickDrag(0, y0, 5, 999)
			},
			wantSel: "alpha bravo\ncharli",
			wantOK:  true,
		},
		{
			name:  "triple-click far below output band selects nothing",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				// Y beyond the grid height: out of bounds, no line.
				return tripleClick(2, 100000)
			},
			wantOK: false,
		},
		{
			name:  "selection then input-band click clears it",
			lines: []string{"alpha bravo"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				y := rowOf(nil, out, w, 'a')
				evs := tripleClick(2, y)
				// A click in the input band (last row) clears the
				// output selection.
				evs = append(evs,
					mouseEv(0, mouseTestHeight-1, term.MouseLeft),
					mouseEv(0, mouseTestHeight-1, term.MouseRelease),
				)
				return evs
			},
			wantOK: false,
		},
		{
			name:   "zero-size grid does not panic",
			width:  1,
			height: 1,
			lines:  []string{"alpha"},
			gesture: func(out *term.StringWriter, w int) []term.Event {
				return tripleClick(0, 0)
			},
			wantOK: false,
		},
	}
}

// linesCmd is a CommandHandler that emits a fixed set of output lines so
// the shell's output band has selectable text.
type linesCmd struct{ lines []string }

func (c linesCmd) HandleCommand(
	_ context.Context, _ repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	out := make([]component.Responsive, len(c.lines))
	for i, l := range c.lines {
		out[i] = component.NewResponsiveString(l, component.StringResponsiveConfig{})
	}
	return iterator.FromSlice(out), nil
}

func (linesCmd) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (linesCmd) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.Empty[component.Responsive](), nil
}

// newMouseTestHandler builds a shell handler with a "show" command that
// prints lines, runs it once, and renders so the output band is
// populated. Scheduled ticks are queued and drained on the test
// goroutine after Wait to avoid racing with the dispatch goroutine.
func newMouseTestHandler(t *testing.T, w, h int, lines []string) (*Handler, *term.StringWriter) {
	t.Helper()
	var mu sync.Mutex
	var ticks []func()
	sched := func(fn func()) bool {
		mu.Lock()
		ticks = append(ticks, fn)
		mu.Unlock()
		return true
	}
	hd, reg := New(sched, term.NopInterrupter(), stubEditor{}, Config{MaxHistory: 100})
	reg.Register("show", "print lines", linesCmd{lines: lines})
	t.Cleanup(func() { _ = hd.Close() })

	hd.Resize(w, h)
	for _, c := range "show" {
		hd.Handle(term.Event{Type: term.EventKey, Ch: c})
	}
	hd.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	hd.Wait()
	for {
		mu.Lock()
		if len(ticks) == 0 {
			mu.Unlock()
			break
		}
		fn := ticks[0]
		ticks = ticks[1:]
		mu.Unlock()
		fn()
	}

	out := term.NewStringWriter(w, h)
	hd.Draw(out)
	_ = out.Flush()
	return hd, out
}

// rowOf returns the first row whose rendered content contains want. t
// may be nil when called from a gesture builder; in that case a missing
// rune panics rather than calling t.Fatalf.
func rowOf(t *testing.T, out *term.StringWriter, w int, want rune) int {
	if t != nil {
		t.Helper()
	}
	cells := out.Cells()
	for y := range len(cells) / w {
		for x := range w {
			if cells[y*w+x].Ch == want {
				return y
			}
		}
	}
	if t != nil {
		t.Fatalf("char %q not found in rendered output", want)
	}
	panic(fmt.Sprintf("char %q not found in rendered output", want))
}

func mouseEv(x, y int, key term.Key) term.Event {
	return term.Event{Type: term.EventMouse, MouseX: x, MouseY: y, Key: key}
}

// tripleClick issues three press/release pairs at (x, y). y may be out
// of bounds for clamping/edge-case tests.
func tripleClick(x, y int) []term.Event {
	return []term.Event{
		mouseEv(x, y, term.MouseLeft), mouseEv(x, y, term.MouseRelease),
		mouseEv(x, y, term.MouseLeft), mouseEv(x, y, term.MouseRelease),
		mouseEv(x, y, term.MouseLeft), mouseEv(x, y, term.MouseRelease),
	}
}

func doubleClick(x, y int) []term.Event {
	return []term.Event{
		mouseEv(x, y, term.MouseLeft), mouseEv(x, y, term.MouseRelease),
		mouseEv(x, y, term.MouseLeft), mouseEv(x, y, term.MouseRelease),
	}
}

func clickDrag(x1, y1, x2, y2 int) []term.Event {
	return []term.Event{
		mouseEv(x1, y1, term.MouseLeft),
		mouseEv(x2, y2, term.MouseLeft),
		mouseEv(x2, y2, term.MouseRelease),
	}
}

// renderText returns the full rendered screen as a single string with
// rows separated by newlines, used to assert presence/absence of output.
func renderText(t *testing.T, hd *Handler, w, h int) string {
	t.Helper()
	out := term.NewStringWriter(w, h)
	hd.Draw(out)
	_ = out.Flush()
	cells := out.Cells()
	var b strings.Builder
	for y := range h {
		for x := range w {
			ch := cells[y*w+x].Ch
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		}
		b.WriteRune('\n')
	}
	return b.String()
}

func TestHandlerCtrlLClearsOutput(t *testing.T) {
	const w, h = 30, 12
	hd, _ := newMouseTestHandler(t, w, h, []string{"clearme-marker"})

	require.Contains(t, renderText(t, hd, w, h), "clearme-marker",
		"command output should be present before clear")

	for _, c := range "draft" {
		hd.Handle(term.Event{Type: term.EventKey, Ch: c})
	}
	require.Equal(t, "draft", hd.editBuf.String())

	_, handled := hd.Handle(term.Event{Type: term.EventKey, Ch: 'l', Mod: term.ModCtrl})
	assert.True(t, handled, "<c-l> must be handled")

	rendered := renderText(t, hd, w, h)
	assert.NotContains(t, rendered, "clearme-marker",
		"command output should be gone after clear")
	assert.Equal(t, "draft", hd.editBuf.String(),
		"in-progress input line must be preserved across clear")
	assert.Contains(t, rendered, "draft",
		"the prompt with the input line should still render")
}

func TestHandlerCtrlLPreservesHistory(t *testing.T) {
	h := newTestHandler(t, []string{"oldest", "middle", "newest"})
	h.Resize(testWidthH, testHeight)

	_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: 'l', Mod: term.ModCtrl})
	require.True(t, handled, "<c-l> must be handled")

	h.Handle(arrowUp)
	assert.Equal(t, "newest", h.editBuf.String(),
		"history must still be recallable after clear")
	h.Handle(arrowUp)
	assert.Equal(t, "middle", h.editBuf.String())
}

func TestHandlerCtrlLRunsClearHook(t *testing.T) {
	var cleared int
	h, _ := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{MaxHistory: 100, ClearHook: func() { cleared++ }},
	)
	t.Cleanup(func() { _ = h.Close() })
	h.Resize(testWidthH, testHeight)

	_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: 'l', Mod: term.ModCtrl})
	require.True(t, handled, "<c-l> must be handled")
	require.Equal(t, 1, cleared, "<c-l> must run the configured ClearHook")
}

// blockingCmd emits one line then blocks in Next until release is
// closed, so a command can be held in-flight for the duration of a
// test. Close (invoked by the inner repl once the iterator is drained)
// signals via closed.
type blockingCmd struct {
	line    string
	release chan struct{}
	started chan struct{}
	closed  chan struct{}
}

func (c *blockingCmd) HandleCommand(
	_ context.Context, _ repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if c.started != nil {
		close(c.started)
	}
	return &blockingIter{cmd: c}, nil
}

func (*blockingCmd) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (*blockingCmd) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.Empty[component.Responsive](), nil
}

type blockingIter struct {
	cmd  *blockingCmd
	sent bool
}

func (it *blockingIter) Next(ctx context.Context) (component.Responsive, bool) {
	if !it.sent {
		it.sent = true
		return component.NewResponsiveString(
			it.cmd.line, component.StringResponsiveConfig{}), true
	}
	select {
	case <-it.cmd.release:
	case <-ctx.Done():
	}
	return nil, false
}

func (*blockingIter) Err() error { return nil }

func (it *blockingIter) Close() error {
	select {
	case <-it.cmd.closed:
	default:
		close(it.cmd.closed)
	}
	return nil
}

func TestHandlerCtrlLIgnoredWhileCommandRunning(t *testing.T) {
	const w, h = 30, 12
	var mu sync.Mutex
	var ticks []func()
	sched := func(fn func()) bool {
		mu.Lock()
		ticks = append(ticks, fn)
		mu.Unlock()
		return true
	}
	drain := func() {
		for {
			mu.Lock()
			if len(ticks) == 0 {
				mu.Unlock()
				return
			}
			fn := ticks[0]
			ticks = ticks[1:]
			mu.Unlock()
			fn()
		}
	}

	cmd := &blockingCmd{
		line:    "running-marker",
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	hd, reg := New(sched, term.NopInterrupter(), stubEditor{}, Config{MaxHistory: 100})
	reg.Register("block", "blocks", cmd)
	t.Cleanup(func() { _ = hd.Close() })
	hd.Resize(w, h)

	for _, c := range "block" {
		hd.Handle(term.Event{Type: term.EventKey, Ch: c})
	}
	hd.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	require.Eventually(t, func() bool {
		drain()
		return strings.Contains(renderText(t, hd, w, h), "running-marker")
	}, time.Second, 5*time.Millisecond, "blocking command output should render")
	require.True(t, hd.shim.running(), "command should be in-flight")

	_, handled := hd.Handle(term.Event{Type: term.EventKey, Ch: 'l', Mod: term.ModCtrl})
	assert.True(t, handled, "<c-l> must be handled (consumed) even when ignored")
	assert.Contains(t, renderText(t, hd, w, h), "running-marker",
		"<c-l> must not clear output while a command is running")

	close(cmd.release)
	<-cmd.closed
	hd.Wait()
	drain()
	require.False(t, hd.shim.running(), "command should be finished")

	hd.Handle(term.Event{Type: term.EventKey, Ch: 'l', Mod: term.ModCtrl})
	assert.NotContains(t, renderText(t, hd, w, h), "running-marker",
		"<c-l> must clear once the command has finished")
}
