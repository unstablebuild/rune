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

package vtegraphics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBufferPool(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, p *BufferPool)
	}{
		{
			name: "a buffer put is the one got for its size",
			run: func(t *testing.T, p *BufferPool) {
				a := make([]byte, 16)
				p.Put(a)
				got := p.Get(16)
				assert.Same(t, &a[0], &got[0])
				assert.Len(t, got, 16)
			},
		},
		{
			name: "another size allocates and leaves the buffer",
			run: func(t *testing.T, p *BufferPool) {
				a := make([]byte, 16)
				p.Put(a)
				got := p.Get(32)
				assert.NotSame(t, &a[0], &got[0])
				assert.Len(t, got, 32)
				assert.Same(t, &a[0], &p.Get(16)[0], "the buffer is still there")
			},
		},
		{
			name: "a buffer is handed out once",
			run: func(t *testing.T, p *BufferPool) {
				a := make([]byte, 16)
				p.Put(a)
				p.Put(a[:16])
				assert.Same(t, &a[0], &p.Get(16)[0])
				assert.NotSame(t, &a[0], &p.Get(16)[0])
			},
		},
		{
			name: "the oldest buffer makes room",
			run: func(t *testing.T, p *BufferPool) {
				a, b, c := make([]byte, 16), make([]byte, 16), make([]byte, 16)
				p.Put(a)
				p.Put(b)
				p.Put(c)
				assert.Same(t, &b[0], &p.Get(16)[0])
				assert.Same(t, &c[0], &p.Get(16)[0])
				assert.NotSame(t, &a[0], &p.Get(16)[0])
			},
		},
		{
			name: "an empty buffer is not kept",
			run: func(t *testing.T, p *BufferPool) {
				p.Put(nil)
				p.Put([]byte{})
				assert.Empty(t, p.free)
				assert.Empty(t, p.Get(0))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, new(BufferPool))
		})
	}
}

func TestBufferPoolNilAllocates(t *testing.T) {
	var p *BufferPool
	got := p.Get(16)
	require.Len(t, got, 16)
	assert.NotPanics(t, func() { p.Put(got) })
	assert.NotSame(t, &got[0], &p.Get(16)[0])
}
