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

package cell

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// countingSubscriber pairs each OnWillEdit with an OnDidEdit the way
// workspace.file does, where the imbalance leaks a WaitGroup counter.
type countingSubscriber struct {
	will int
	did  int
}

func (s *countingSubscriber) OnWillEdit(
	_ context.Context, _, _ term.Coordinates, _ string,
) {
	s.will++
}

func (s *countingSubscriber) OnDidEdit(
	_ context.Context, _, _ term.Coordinates, _ string,
) {
	s.did++
}

type panicEditor struct{}

func (panicEditor) Edit(
	_ context.Context, _, _ term.Coordinates, _ string,
) (from, to term.Coordinates, old string) {
	panic("out of bounds")
}

func TestPublisherBalancesSubscribersOnEditPanic(t *testing.T) {
	sub := new(countingSubscriber)
	p := newPublisher(panicEditor{})
	p.Subscribe(sub)

	require.Panics(t, func() {
		p.Edit(context.Background(), term.Coordinates{}, term.Coordinates{}, "a")
	})

	assert.Equal(t, 1, sub.will)
	assert.Equal(t, sub.will, sub.did,
		"every OnWillEdit must be closed by an OnDidEdit, even when Edit panics")
}

func TestPublisherNotifiesSubscribersOnceOnSuccess(t *testing.T) {
	sub := new(countingSubscriber)
	cells := new(rawCells)
	cells.init()
	p := newPublisher(cells)
	p.Subscribe(sub)

	p.Edit(context.Background(), term.Coordinates{}, term.Coordinates{}, "a")

	assert.Equal(t, 1, sub.will)
	assert.Equal(t, 1, sub.did)
}

func TestBufferBalancesSubscribersOnOutOfBoundsPanic(t *testing.T) {
	b := NewBuffer()
	b.Init()
	b.WriteString("ab")

	sub := new(countingSubscriber)
	b.Subscribe(sub)

	require.Panics(t, func() {
		b.editor.Edit(context.Background(),
			term.Coordinates{}, term.Coordinates{X: 5}, "")
	})

	assert.NotZero(t, sub.will)
	assert.Equal(t, sub.will, sub.did,
		"a panicking edit must not leave an edit in flight")
}
