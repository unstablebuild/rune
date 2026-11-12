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
	"slices"
	"sync"
)

// BufferPool recycles the pixel buffers of a frame stream. A client
// streaming pane-sized frames at display rate otherwise allocates
// megabytes per frame, which the collector then has to find; with the
// pool the stream settles on two buffers, one being read in and one
// being shown. It is safe for concurrent use, as transmissions are read
// without the lock that serializes Storage. A nil pool allocates.
type BufferPool struct {
	mu   sync.Mutex
	free [][]byte
}

// bufferPoolSize bounds what an idle pool retains. A stream needs one
// buffer in flight and one on screen, and anything more is a frame that
// was transmitted but never drawn, which is garbage either way.
const bufferPoolSize = 2

// Get returns a buffer of exactly size bytes whose content is
// unspecified: the caller fills all of it. Only a buffer of the same size
// is reused, so an image far smaller than the stream never pins one of
// its buffers.
func (p *BufferPool) Get(size int) []byte {
	if p != nil && size > 0 {
		p.mu.Lock()
		for i, buf := range p.free {
			if len(buf) == size {
				p.free = slices.Delete(p.free, i, i+1)
				p.mu.Unlock()
				return buf
			}
		}
		p.mu.Unlock()
	}
	return make([]byte, size)
}

// Put offers buf for reuse; the caller must not touch it afterwards.
// A buffer the pool already holds is ignored, so that no slice of it is
// ever handed out twice, and the oldest buffer makes room when the pool
// is full.
func (p *BufferPool) Put(buf []byte) {
	if p == nil || len(buf) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, held := range p.free {
		if &held[0] == &buf[0] {
			return
		}
	}
	if len(p.free) == bufferPoolSize {
		p.free = slices.Delete(p.free, 0, 1)
	}
	p.free = append(p.free, buf)
}
