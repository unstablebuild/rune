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

package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/goleak"
	rhandlertest "unstable.build/rune/internal/handler/handlertest"
)

func newTestInterrupter() (term.Interrupter, chan struct{}) {
	ch := make(chan struct{})
	interrupter := term.FuncInterrupter(func(context.Context) error {
		ch <- struct{}{}
		return nil
	})
	return interrupter, ch
}

func TestAnimationPlayer(t *testing.T) {
	fps := 30
	interrupter, ch := newTestInterrupter()

	frames := []string{
		"0000    \n0000    \n    0000\n    0000",
		"1111    \n1111    \n    1111\n    1111",
	}
	sequences := []int{0, 0, 1}

	c := component.NewAnimation(interrupter, frames, sequences, fps)

	// sut
	h := AnimationPlayer(c)
	h.Resize(8, 4)

	for i := range 3 {
		w := term.NewStringWriter(8, 4)
		<-ch
		h.Draw(w)
		var expected string
		switch i {
		case 0:
			expected = "0000    \n0000    \n    0000\n    0000"
		case 1:
			expected = "0000    \n0000    \n    0000\n    0000"
		case 2:
			expected = "1111    \n1111    \n    1111\n    1111"
		default:
			panic("hmmm")
		}

		require.NoError(t, w.Flush())
		assert.Equal(t, expected, w.String(), i)
	}

	// pause
	exit, handled := h.Handle(term.Event{Key: term.KeySpace, Type: term.EventKey})
	assert.True(t, handled)
	assert.False(t, exit)

	for i := range 3 {
		w := term.NewStringWriter(8, 4)
		<-ch
		h.Draw(w)
		expected := "0000    \n0000    \n    0000\n    0000"
		require.NoError(t, w.Flush())
		assert.Equal(t, expected, w.String(), i)
	}

	// unpause
	exit, handled = h.Handle(term.Event{Key: term.KeySpace, Type: term.EventKey})
	assert.True(t, handled)
	assert.False(t, exit)

	for i := range 3 {
		w := term.NewStringWriter(8, 4)
		<-ch
		h.Draw(w)
		var expected string
		switch i {
		case 2:
			expected = "0000    \n0000    \n    0000\n    0000"
		case 0:
			expected = "0000    \n0000    \n    0000\n    0000"
		case 1:
			expected = "1111    \n1111    \n    1111\n    1111"
		default:
			panic("hmmm")
		}

		require.NoError(t, w.Flush())
		assert.Equal(t, expected, w.String(), i)
	}

	// close
	exit, handled = h.Handle(term.Event{Key: term.KeyEsc, Type: term.EventKey})
	assert.True(t, handled)
	assert.True(t, exit)
	goleak.VerifyNone(t)

	// after close animation should be paused and cause no panics
	for i := range 3 {
		w := term.NewStringWriter(8, 4)
		h.Draw(w)
		expected := "0000    \n0000    \n    0000\n    0000"
		require.NoError(t, w.Flush())
		assert.Equal(t, expected, w.String(), i)
	}
}

func TestPlayerPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		a := component.NewAnimation(term.NopInterrupter(),
			[]string{"0000\n0000", "1111\n1111"}, []int{0, 1}, 30)
		h := AnimationPlayer(a)
		rhandlertest.RunPointerLeaves(t, h, size[0], size[1])
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeySpace})
		rhandlertest.RunPointerLeaves(t, h, size[0], size[1])
		require.NoError(t, a.Close())
	}
}
