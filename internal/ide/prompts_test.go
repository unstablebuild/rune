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

package ide

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	"unstable.build/rune/internal/text/texttest"
)

func TestPromptOnCloseNoReopenDuringShutdown(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///dirty.go")
	require.NoError(t, err)

	t.Run("areYouSurePrompt reopens when interactive", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()
		browserComp := b.ex.comp.Browser()

		b.ex.openSurePrompt(uri, nil, "changed on", true)
		require.Equal(t, 1, browserComp.FloatingWindows())

		h := &areYouSurePrompt{ex: b.ex, uri: uri, op: "changed on", reload: true}
		require.NoError(t, h.OnClose())
		assert.Equal(t, 2, browserComp.FloatingWindows(),
			"interactive dismissal should re-open the paired prompt")
	})

	t.Run("areYouSurePrompt no reopen on shutdown", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()
		browserComp := b.ex.comp.Browser()

		b.ex.openSurePrompt(uri, nil, "changed on", true)
		require.Equal(t, 1, browserComp.FloatingWindows())

		b.ex.closed = true
		h := &areYouSurePrompt{ex: b.ex, uri: uri, op: "changed on", reload: true}
		require.NoError(t, h.OnClose())
		assert.Equal(t, 1, browserComp.FloatingWindows(),
			"Close() must not re-open a replacement prompt")
	})

	t.Run("fileChangedPrompt no reopen on shutdown", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()
		browserComp := b.ex.comp.Browser()

		b.ex.openFileChangedPrompt(uri, nil, "changed on", true)
		require.Equal(t, 1, browserComp.FloatingWindows())

		b.ex.closed = true
		h := &fileChangedPrompt{ex: b.ex, uri: uri, op: "changed on", reload: true}
		require.NoError(t, h.OnClose())
		assert.Equal(t, 1, browserComp.FloatingWindows(),
			"Close() must not re-open a replacement prompt")
	})
}
