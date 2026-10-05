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

package workspacerpc

import (
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBufSlabBestFitReuse(t *testing.T) {
	t.Parallel()
	s := &bufSlab{pacing: true}
	b4 := make([]byte, 4<<10)
	s.put(make([]byte, 2<<10))
	s.put(make([]byte, 8<<10))
	s.put(b4)

	got := s.get(3 << 10)

	assert.Len(t, got, 3<<10)
	assert.Equal(t, 4<<10, cap(got))
	assert.Same(t, &b4[0], &got[0])
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Len(t, s.bufs, 2)
}

func TestBufSlabEvictsSmallestWhenFull(t *testing.T) {
	t.Parallel()
	s := &bufSlab{pacing: true}
	for i := range maxRecycledBufs {
		s.put(make([]byte, (2<<10)+i))
	}

	s.put(make([]byte, 64<<10))

	s.mu.Lock()
	defer s.mu.Unlock()
	require.Len(t, s.bufs, maxRecycledBufs)
	assert.Equal(t, (2<<10)+1, cap(s.bufs[0]))
	assert.Equal(t, 64<<10, cap(s.bufs[len(s.bufs)-1]))
}

func TestBufSlabSmallReadsBypassPool(t *testing.T) {
	t.Parallel()
	s := &bufSlab{pacing: true}
	s.put(make([]byte, 4<<10))

	got := s.get(16)
	empty := s.get(0)

	assert.Len(t, got, 16)
	assert.Empty(t, empty)
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Len(t, s.bufs, 1)
}

func TestBufSlabRecyclesReleasedBuffer(t *testing.T) {
	t.Parallel()
	s := &bufSlab{pacing: true}
	buf := s.get(4 << 10)
	// uintptr does not extend the backing array's liveness.
	base := uintptr(unsafe.Pointer(&buf[0]))
	buf = nil
	_ = buf

	require.Eventually(t, func() bool {
		runtime.GC()
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.bufs) == 1
	}, 10*time.Second, 10*time.Millisecond,
		"released buffer never returned to the slab")

	got := s.get(4 << 10)
	assert.Equal(t, base, uintptr(unsafe.Pointer(&got[0])))
}

func TestBufSlabGetFallsBackToVictim(t *testing.T) {
	t.Parallel()
	s := &bufSlab{pacing: true}
	b4 := make([]byte, 4<<10)
	s.put(b4)

	s.purge()
	got := s.get(4 << 10)

	assert.Same(t, &b4[0], &got[0])
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Empty(t, s.bufs)
	assert.Empty(t, s.victim)
}

func TestBufSlabPurgeDropsIdleGeneration(t *testing.T) {
	t.Parallel()
	s := &bufSlab{pacing: true}
	b4 := make([]byte, 4<<10)
	s.put(b4)

	s.purge()
	s.purge()

	s.mu.Lock()
	assert.Empty(t, s.bufs)
	assert.Empty(t, s.victim)
	s.mu.Unlock()
	got := s.get(4 << 10)
	assert.NotSame(t, &b4[0], &got[0])
}

func TestBufSlabIdleBuffersEventuallyFreed(t *testing.T) {
	t.Parallel()
	s := &bufSlab{}
	s.put(make([]byte, 4<<10))

	require.Eventually(t, func() bool {
		runtime.GC()
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.bufs) == 0 && len(s.victim) == 0
	}, 10*time.Second, 10*time.Millisecond,
		"idle pooled buffer never aged out")
}
