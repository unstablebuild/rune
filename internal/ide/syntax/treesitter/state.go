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

package treesitter

import (
	"context"
	"errors"

	"github.com/unstablebuild/blue/iterator"
	"unstable.build/rune/internal/ide/syntax"
)

type waitStateIterator struct {
	ready chan struct{}
	err   error
	tree  *Tree
	iter  iterator.Iterator[syntax.State]
}

func newWaitStateIterator(t *Tree, ch chan struct{}) *waitStateIterator {
	return &waitStateIterator{
		tree:  t,
		ready: ch,
	}
}

func (f *waitStateIterator) Next(ctx context.Context) (syntax.State, bool) {
	if f.iter == nil {
		select {
		case <-f.ready:
		case <-ctx.Done():
			f.err = ctx.Err()
			return syntax.State{}, false
		}
		f.tree.mu.Lock()
		f.iter = newReadyStateIterator(f.tree)
		f.tree.mu.Unlock()
	}
	return f.iter.Next(ctx)
}

func (f *waitStateIterator) Err() error {
	if f.err != nil {
		return f.err
	}
	if f.iter == nil {
		f.tree.mu.Lock()
		defer f.tree.mu.Unlock()
		if f.tree.tree == nil {
			return errors.New(f.tree.currState.ParserError)
		}
		if f.tree.closed {
			return errors.New("syntax tree closed")
		}
		return nil
	}
	return f.iter.Err()
}

func (f *waitStateIterator) Close() error {
	return nil
}

type readyStateIterator struct {
	t    *Tree
	err  error
	next chan syntax.State
}

func newReadyStateIterator(t *Tree) iterator.Iterator[syntax.State] {
	ch := make(chan syntax.State, 1)
	t.statesubs[ch] = struct{}{}

	ch <- t.currState
	return &readyStateIterator{
		t:    t,
		next: ch,
	}
}

func (f *readyStateIterator) Next(ctx context.Context) (syntax.State, bool) {
	select {
	case state, ok := <-f.next:
		return state, ok
	case <-ctx.Done():
		f.err = ctx.Err()
		return syntax.State{}, false
	}
}

func (f *readyStateIterator) Err() error {
	return f.err
}

func (f *readyStateIterator) Close() error {
	f.t.mu.Lock()
	defer f.t.mu.Unlock()
	delete(f.t.statesubs, f.next)
	return nil
}
