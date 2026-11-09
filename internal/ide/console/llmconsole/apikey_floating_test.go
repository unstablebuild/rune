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

package llmconsole

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/handler/inputbox"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/handler/handlertest"
)

func TestKeyInputFloatingPointerLeaves(t *testing.T) {
	t.Parallel()
	const key = "sk-proj-0123456789abcdefghijklmnopqrstuvwxyz"
	for _, size := range []struct{ w, h int }{{60, 15}, {60, 1}, {1, 1}} {
		var done int
		f := &keyInputFloating{
			ib: inputbox.New(
				inputbox.WithPrompt("Paste your OpenAI API key: "),
				inputbox.WithRedact(true),
				inputbox.WithCtrlCAborts(),
			),
			label:  "Paste your OpenAI API key: ",
			onDone: func(string, bool) { done++ },
		}
		for _, c := range key {
			_, _ = f.Handle(term.Event{Type: term.EventKey, Ch: c})
		}
		handlertest.RunPointerLeaves(t, f, size.w, size.h)
		assert.Equal(t, key, f.ib.Text(), "the pointer must not edit the key")
		assert.Zero(t, done, "the pointer must not submit or dismiss the prompt")
	}
}
