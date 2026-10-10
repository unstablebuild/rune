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

package treesitter

import (
	"bytes"
	"strings"
	"testing"
	"testing/iotest"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadAllInto(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		buf     []byte
	}{
		{name: "empty input nil buffer", content: ""},
		{name: "empty input recycled buffer", content: "", buf: make([]byte, 0, 64)},
		{name: "fits existing capacity", content: "hello world", buf: make([]byte, 0, 64)},
		{name: "exactly fills capacity", content: strings.Repeat("x", 64), buf: make([]byte, 0, 64)},
		{name: "grows past capacity", content: strings.Repeat("abc", 100), buf: make([]byte, 0, 8)},
		{name: "grows from nil", content: strings.Repeat("large", 5000)},
		{name: "recycled buffer with stale contents", content: "fresh", buf: []byte("stale-data-from-previous-file")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := readAllInto(tt.buf, strings.NewReader(tt.content))
			require.NoError(t, err)
			assert.Equal(t, tt.content, string(got))
		})
	}
}

func TestReadAllIntoReusesBackingArray(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("0123456789", 10000)
	buf, err := readAllInto(nil, strings.NewReader(big))
	require.NoError(t, err)
	require.Equal(t, big, string(buf))

	grown := cap(buf)
	base := unsafe.SliceData(buf[:cap(buf)])
	buf, err = readAllInto(buf, strings.NewReader("tiny"))
	require.NoError(t, err)
	assert.Equal(t, "tiny", string(buf))
	assert.Equal(t, grown, cap(buf), "smaller read must keep the grown capacity")
	assert.Same(t, base, unsafe.SliceData(buf[:cap(buf)]),
		"smaller read must reuse the backing array")
}

func TestReadAllIntoChunkedReads(t *testing.T) {
	t.Parallel()

	content := strings.Repeat("chunked", 100)
	// OneByteReader forces one byte per Read call; DataErrReader returns
	// io.EOF alongside the final data instead of on a separate call.
	got, err := readAllInto(nil, iotest.OneByteReader(strings.NewReader(content)))
	require.NoError(t, err)
	assert.Equal(t, content, string(got))

	got, err = readAllInto(got, iotest.DataErrReader(strings.NewReader(content)))
	require.NoError(t, err)
	assert.Equal(t, content, string(got))
}

func TestReadAllIntoPropagatesReadError(t *testing.T) {
	t.Parallel()

	r := iotest.TimeoutReader(bytes.NewReader([]byte("partial-then-error")))
	_, err := readAllInto(nil, r)
	assert.ErrorIs(t, err, iotest.ErrTimeout)
}

func TestLineStartsRecycledBuffer(t *testing.T) {
	t.Parallel()

	big := []byte(strings.Repeat("line\n", 1000))
	starts := lineStarts(nil, big)
	require.Len(t, starts, 1001)
	assert.Equal(t, 0, starts[0])
	assert.Equal(t, 5, starts[1])

	// A smaller file must not inherit stale rows from the previous one.
	starts = lineStarts(starts, []byte("a\nbb\n"))
	require.Equal(t, []int{0, 2, 5}, starts)

	starts = lineStarts(starts, nil)
	require.Equal(t, []int{0}, starts)
}

func TestFileScratchTrim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentCap  int
		startsCap   int
		keepContent bool
		keepStarts  bool
	}{
		{name: "typical buffers kept", contentCap: 64 << 10, startsCap: 4 << 10,
			keepContent: true, keepStarts: true},
		{name: "at limit kept", contentCap: maxScratchContent, startsCap: maxScratchStarts,
			keepContent: true, keepStarts: true},
		{name: "oversized content dropped", contentCap: maxScratchContent + 1,
			startsCap: 4 << 10, keepStarts: true},
		{name: "oversized starts dropped", contentCap: 64 << 10,
			startsCap: maxScratchStarts + 1, keepContent: true},
		{name: "both dropped", contentCap: maxScratchContent + 1,
			startsCap: maxScratchStarts + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := fileScratch{
				content: make([]byte, 0, tt.contentCap),
				starts:  make([]int, 0, tt.startsCap),
			}
			s.trim()
			if tt.keepContent {
				assert.Equal(t, tt.contentCap, cap(s.content), "content must stay recycled")
			} else {
				assert.Nil(t, s.content, "oversized content must be dropped")
			}
			if tt.keepStarts {
				assert.Equal(t, tt.startsCap, cap(s.starts), "starts must stay recycled")
			} else {
				assert.Nil(t, s.starts, "oversized starts must be dropped")
			}
		})
	}
}
