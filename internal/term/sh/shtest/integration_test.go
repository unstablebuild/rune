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

package shtest

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/sh"
)

type testHandler struct {
	handleFn func(
		context.Context, repl.Command, repl.ProgressWriter,
	) (iterator.Iterator[component.Responsive], error)
	completeFn func(
		context.Context, string, []string,
	) (iterator.Iterator[string], error)
}

func (t *testHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if t.handleFn != nil {
		return t.handleFn(ctx, cmd, pw)
	}
	return iterator.FromSlice[component.Responsive](nil), nil
}

func (t *testHandler) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if t.completeFn != nil {
		return t.completeFn(ctx, cmd, args)
	}
	return iterator.FromSlice[string](nil), nil
}

func nopSchedule(func()) bool          { return false }
func nopInterrupter() term.Interrupter { return term.NopInterrupter() }

// settledREPL draws a repl.Handler once the commands it runs have ended,
// running the callbacks they schedule as the event loop would: a
// command's row spins until its end is applied on the loop.
type settledREPL struct {
	*repl.Handler
	mu      sync.Mutex
	pending []func()
}

func newSettledREPL(underlying repl.CommandHandler) *settledREPL {
	s := new(settledREPL)
	s.Handler = repl.New(sh.New(underlying, workspaceapi.URI{}),
		s.scheduleNextTick, nopInterrupter(), repl.WithPrompt("$ "))
	return s
}

func (s *settledREPL) scheduleNextTick(fn func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, fn)
	return true
}

func (s *settledREPL) Draw(w term.Writer) {
	for {
		s.Wait()
		s.mu.Lock()
		pending := s.pending
		s.pending = nil
		s.mu.Unlock()
		if len(pending) == 0 {
			break
		}
		for _, fn := range pending {
			fn()
		}
	}
	s.Handler.Draw(w)
}

func TestIntegration(t *testing.T) {
	cases := []struct {
		name  string
		cases []handlertest.SequenceTestCase
	}{
		{
			name: "empty prompt",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "simple command echo",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "echo<space>hello<enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ echo hello        \n" +
						"hello               \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "pipe syntax accepted",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "a|b<enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ a|b               \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "semicolons accepted",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "a;b<enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ a;b               \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "variable syntax",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "FOO=x<enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ FOO=x             \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "logical operators",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "a&&b<enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ a&&b              \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "subshell syntax",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "(echo<space>hi)<enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ (echo hi)         \n" +
						"hi                  \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "multiple commands",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "cmd1<enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ cmd1              \n" +
						"$ ▐                 ",
				},
				{
					InputSequence: "cmd2<enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ cmd1              \n" +
						"$ cmd2              \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "ctrl+C clears",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "hello<c-c>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"^C                  \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "empty input echoes blank prompt",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "<space><space><space><enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$                   \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "overflow scroll",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "c1<enter>c2<enter>c3<enter>c4<enter>" +
						"c5<enter>c6<enter>c7<enter>c8<enter>c9<enter>",
					Expected: "$ c1                \n" +
						"$ c2                \n" +
						"$ c3                \n" +
						"$ c4                \n" +
						"$ c5                \n" +
						"$ c6                \n" +
						"$ c7                \n" +
						"$ c8                \n" +
						"$ c9                \n" +
						"$ ▐                 ",
				},
			},
		},
		{
			name: "syntax error non-crash",
			cases: []handlertest.SequenceTestCase{
				{
					InputSequence: "echo<space>\"unterminated<enter>",
					Expected: "                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"                    \n" +
						"$ echo \"unterminated\n" +
						"1:6: reached EOF wit\n" +
						"hout closing quote `\n" +
						"\"`                  \n" +
						"$ ▐                 ",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handlertest.RunHandlerSequence(t, newSettledREPL(&testHandler{}), 20, 10, tc.cases)
		})
	}
}

func TestCtrlDEOF(t *testing.T) {
	underlying := &testHandler{}
	h := repl.New(
		sh.New(underlying, workspaceapi.URI{}),
		nopSchedule, nopInterrupter(),
		repl.WithPrompt("$ "),
	)
	h.Resize(20, 10)
	exit, handled := h.Handle(term.Event{
		Type: term.EventKey,
		Ch:   'd',
		Mod:  term.ModCtrl,
	})
	assert.True(t, exit)
	assert.True(t, handled)
}

func TestTabCompletionPassthrough(t *testing.T) {
	called := false
	underlying := &testHandler{
		completeFn: func(
			_ context.Context,
			_ string, _ []string,
		) (iterator.Iterator[string], error) {
			called = true
			return iterator.FromSlice([]string{
				"foobar", "foobaz",
			}), nil
		},
	}
	h := repl.New(
		sh.New(underlying, workspaceapi.URI{}),
		nopSchedule, nopInterrupter(),
		repl.WithPrompt("$ "),
	)
	h.Resize(30, 10)

	keys, err := term.ParseKeys("foo<tab>")
	require.NoError(t, err)
	for _, k := range keys {
		h.Handle(term.Event{
			Type: term.EventKey,
			Ch:   k.Ch,
			Mod:  k.Mod,
			Key:  k.Key,
		})
	}
	assert.True(t, called)
}

func TestHistoryRecall(t *testing.T) {
	underlying := &testHandler{}
	h := repl.New(
		sh.New(underlying, workspaceapi.URI{}),
		nopSchedule, nopInterrupter(),
		repl.WithPrompt("$ "),
	)
	h.Resize(30, 10)

	keys, err := term.ParseKeys("first<enter>second<enter><up>")
	require.NoError(t, err)
	for _, k := range keys {
		h.Handle(term.Event{
			Type: term.EventKey,
			Ch:   k.Ch,
			Mod:  k.Mod,
			Key:  k.Key,
		})
	}

	got := handlertest.DrawHandler(h, 30, 10)
	assert.Contains(t, got, "second")
}

func TestCustomPrompt(t *testing.T) {
	underlying := &testHandler{}
	h := repl.New(
		sh.New(underlying, workspaceapi.URI{}),
		nopSchedule, nopInterrupter(),
		repl.WithPrompt(">> "),
	)
	cases := []handlertest.SequenceTestCase{
		{
			InputSequence: "",
			Expected: "                    \n" +
				"                    \n" +
				"                    \n" +
				"                    \n" +
				"                    \n" +
				"                    \n" +
				"                    \n" +
				"                    \n" +
				"                    \n" +
				">> ▐                ",
		},
	}
	handlertest.RunHandlerSequence(t, h, 20, 10, cases)
}
