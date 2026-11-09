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

package tailnet

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/ipn"
)

func TestSnapshots(t *testing.T) {
	f, lc := newFakeLocalAPI(t, testNode(1, "laptop", alice))
	s := newSnapshots(lc)
	ctx := context.Background()
	get := func() {
		t.Helper()
		_, err := s.get(ctx)
		require.NoError(t, err)
	}

	get()
	get()
	assert.Equal(t, int32(1), f.mapReads.Load(), "read once while fresh")
	s.invalidate()
	get()
	assert.Equal(t, int32(2), f.mapReads.Load(), "read again once invalidated")
	s.ttl = 0
	get()
	assert.Equal(t, int32(3), f.mapReads.Load(), "read again once stale")

	s.ttl = time.Hour
	_, err := s.fresh(ctx)
	require.NoError(t, err)
	get()
	assert.Equal(t, int32(4), f.mapReads.Load(), "a fresh read is cached")

	f.set(func(f *fakeLocalAPI) { f.state = ipn.Stopped })
	s.invalidate()
	_, err = s.get(ctx)
	require.ErrorIs(t, err, ErrNotRunning)
	f.set(func(f *fakeLocalAPI) { f.state = ipn.Running })
	get()
	assert.Equal(t, int32(6), f.mapReads.Load(), "a failure is not cached")
}
