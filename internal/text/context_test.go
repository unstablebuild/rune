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

package text

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/vctrl"
)

func TestBarsFromContext(t *testing.T) {
	t.Run("plain context asks for no bars", func(t *testing.T) {
		opts, ok := BarsFromContext(context.Background())
		assert.False(t, ok)
		assert.Equal(t, BarOptions{}, opts)
	})

	t.Run("zero options still ask for bars", func(t *testing.T) {
		opts, ok := BarsFromContext(WithBars(context.Background(), BarOptions{}))
		assert.True(t, ok)
		assert.Equal(t, BarOptions{}, opts)
	})

	t.Run("round trip", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("rune-gitshow:///repo")
		require.NoError(t, err)
		svc := vctrl.NopService()
		want := BarOptions{
			DisableAuxBar:   true,
			DisableIconsBar: true,
			StatusBar:       &StatusBarOverride{Workspace: uri, GitService: svc},
		}
		got, ok := BarsFromContext(WithBars(context.Background(), want))
		require.True(t, ok)
		assert.Equal(t, want, got)
	})
}
