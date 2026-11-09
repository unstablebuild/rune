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

package vi

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

var _ browserapi.Floating = (*insertCompletionFloating)(nil)

func TestInsertCompletionFloating(t *testing.T) {
	t.Run("render and navigation", func(t *testing.T) {
		f := newInsertCompletionFloating([]string{"alpha", "beta", "gamma"}, 0, nil)
		w, h := f.Dimensions()
		writer := term.NewStringWriter(w, h)
		writer.ForegroundCh = '#'
		handlertest.RunHandlerSequenceWriter(t, writer, f, w, h, []handlertest.SequenceTestCase{
			{InputSequence: "", Expected: " ##### \n ####  \n ##### "},
			{InputSequence: "<down>", Expected: " ##### \n ##### \n ##### "},
			{InputSequence: "<up>", Expected: " ##### \n ####  \n ##### "},
			{InputSequence: "<c-j>", Expected: " ##### \n ##### \n ##### "},
			{InputSequence: "<c-k>", Expected: " ##### \n ####  \n ##### "},
			{InputSequence: "<up>", Expected: " ##### \n ####  \n ##### "},
			{InputSequence: "<down>", Expected: " ##### \n ##### \n ##### "},
			{InputSequence: "<down>", Expected: " ##### \n ####  \n ##### "},
			{InputSequence: "<down>", Expected: " ##### \n ####  \n ##### "},
		})
	})

	t.Run("handle returns", func(t *testing.T) {
		tests := []struct {
			name        string
			event       term.Event
			wantExit    bool
			wantHandled bool
		}{
			{
				name:        "esc exits",
				event:       term.Event{Type: term.EventKey, Key: term.KeyEsc},
				wantExit:    true,
				wantHandled: true,
			},
			{
				name:        "enter exits",
				event:       term.Event{Type: term.EventKey, Key: term.KeyEnter},
				wantExit:    true,
				wantHandled: true,
			},
			{
				name:        "tab exits",
				event:       term.Event{Type: term.EventKey, Key: term.KeyTab},
				wantExit:    true,
				wantHandled: true,
			},
			{
				name:        "down is handled",
				event:       term.Event{Type: term.EventKey, Key: term.KeyArrowDown},
				wantExit:    false,
				wantHandled: true,
			},
			{
				name:        "up is handled",
				event:       term.Event{Type: term.EventKey, Key: term.KeyArrowUp},
				wantExit:    false,
				wantHandled: true,
			},
			{
				name:        "ctrl+j is handled",
				event:       term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'j'},
				wantExit:    false,
				wantHandled: true,
			},
			{
				name:        "ctrl+k is handled",
				event:       term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'k'},
				wantExit:    false,
				wantHandled: true,
			},
			{
				name:        "unrelated key not handled",
				event:       term.Event{Type: term.EventKey, Ch: 'x'},
				wantExit:    false,
				wantHandled: false,
			},
			{
				name:        "mouse event not handled",
				event:       term.Event{Type: term.EventMouse},
				wantExit:    false,
				wantHandled: false,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				f := newInsertCompletionFloating([]string{"alpha", "beta"}, 0, nil)
				exit, handled := f.Handle(tc.event)
				assert.Equal(t, tc.wantExit, exit)
				assert.Equal(t, tc.wantHandled, handled)
			})
		}
	})

	t.Run("enter applies focused candidate", func(t *testing.T) {
		var applied string
		f := newInsertCompletionFloating([]string{"alpha", "beta", "gamma"}, 0, func(s string) {
			applied = s
		})
		// Focus down to "beta", then press Enter
		f.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		f.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		assert.Equal(t, "beta", applied)
	})

	t.Run("tab applies focused candidate", func(t *testing.T) {
		var applied string
		f := newInsertCompletionFloating([]string{"alpha", "beta"}, 0, func(s string) {
			applied = s
		})
		f.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
		assert.Equal(t, "alpha", applied)
	})

	t.Run("esc does not apply", func(t *testing.T) {
		var applied string
		f := newInsertCompletionFloating([]string{"alpha", "beta"}, 0, func(s string) {
			applied = s
		})
		f.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		assert.Equal(t, "", applied)
	})

	t.Run("focus offset tracks navigation", func(t *testing.T) {
		f := newInsertCompletionFloating([]string{"alpha", "beta", "gamma"}, 0, nil)
		assert.Equal(t, 0, f.list.FocusOffset())

		f.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
		assert.Equal(t, 1, f.list.FocusOffset())

		f.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'j'})
		assert.Equal(t, 2, f.list.FocusOffset())

		f.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
		assert.Equal(t, 1, f.list.FocusOffset())

		f.Handle(term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'k'})
		assert.Equal(t, 0, f.list.FocusOffset())
	})

	t.Run("dimensions", func(t *testing.T) {
		f := newInsertCompletionFloating([]string{"ab", "cdef"}, 0, nil)
		w, h := f.Dimensions()
		assert.Equal(t, 6, w) // len("cdef") + 2
		assert.Equal(t, 2, h)
	})
}

func TestInsertCompletionFloatingPointerLeaves(t *testing.T) {
	t.Parallel()
	candidates := []string{"alpha", "日本語のテキスト", "🎉🚀 party", "beta", "a-much-longer-candidate-label"}
	for _, size := range [][2]int{{40, 10}, {1, 1}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			t.Parallel()
			var applied []string
			f := newInsertCompletionFloating(candidates, 2, func(s string) { applied = append(applied, s) })
			handlertest.RunPointerLeaves(t, f, size[0], size[1])

			f.Resize(size[0], size[1])
			for _, at := range []term.Coordinates{
				{X: -1, Y: 0}, {X: size[0], Y: size[1]}, {X: -1000, Y: 1000},
			} {
				exit, _ := f.Handle(term.Event{Type: term.EventMouse, MouseX: at.X, MouseY: at.Y})
				assert.False(t, exit, "a leave to %v must not close the popup", at)
			}
			assert.Equal(t, 2, f.list.FocusOffset())
			assert.Empty(t, applied)
		})
	}
}
