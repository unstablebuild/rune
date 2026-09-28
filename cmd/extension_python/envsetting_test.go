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

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"unstable.build/rune/internal/extension/langext"
)

func TestEnvSetting(t *testing.T) {
	ctx := context.Background()
	rootA := langext.Root{Dir: "/repo", URI: "file:///repo"}
	rootB := langext.Root{Dir: "/repo/svc", URI: "file:///repo/svc"}

	t.Run("unanswered root is unknown", func(t *testing.T) {
		s := newEnvSetting(storagestub.NewInMemoryService())
		managed, known, err := s.get(ctx, rootA)
		require.NoError(t, err)
		assert.False(t, known)
		assert.False(t, managed)
	})

	t.Run("round trips both answers", func(t *testing.T) {
		for _, want := range []bool{true, false} {
			s := newEnvSetting(storagestub.NewInMemoryService())
			require.NoError(t, s.set(ctx, rootA, want))
			managed, known, err := s.get(ctx, rootA)
			require.NoError(t, err)
			assert.True(t, known)
			assert.Equal(t, want, managed)
		}
	})

	t.Run("answers are scoped per root", func(t *testing.T) {
		s := newEnvSetting(storagestub.NewInMemoryService())
		require.NoError(t, s.set(ctx, rootA, true))

		managed, known, err := s.get(ctx, rootB)
		require.NoError(t, err)
		assert.False(t, known)
		assert.False(t, managed)
	})

	t.Run("later answer overwrites the earlier one", func(t *testing.T) {
		s := newEnvSetting(storagestub.NewInMemoryService())
		require.NoError(t, s.set(ctx, rootA, true))
		require.NoError(t, s.set(ctx, rootA, false))

		managed, known, err := s.get(ctx, rootA)
		require.NoError(t, err)
		assert.True(t, known)
		assert.False(t, managed)
	})

	t.Run("unavailable storage never persists", func(t *testing.T) {
		s := newEnvSetting(nil)
		require.NoError(t, s.set(ctx, rootA, true))
		_, known, err := s.get(ctx, rootA)
		require.NoError(t, err)
		assert.False(t, known)
	})
}
