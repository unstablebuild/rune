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

package extension

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

func TestChatViewSendDropsWhatComesOnceClosed(t *testing.T) {
	warning := dialoguetui.MessageEvent{Type: dialoguetui.MessageEventWarning, Text: "injected"}

	t.Run("after", func(t *testing.T) {
		h := &aiEditorHandler{p: term.NopInterrupter()}
		s, c := openBareChat(t, h, "rolling-fox")
		v := s.shownIn()[0]
		require.NoError(t, c.content.Close())
		assert.False(t, v.send(warning))
	})

	t.Run("while closing", func(t *testing.T) {
		h := &aiEditorHandler{p: term.NopInterrupter()}
		s, c := openBareChat(t, h, "rolling-fox")
		v := s.shownIn()[0]
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 100 {
					if !v.send(warning) {
						return
					}
				}
			})
		}
		require.NoError(t, c.content.Close())
		wg.Wait()
	})
}
