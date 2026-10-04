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

package font

import (
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unstable.build/rune/internal/term/gui/font/builtinfont"
)

// countingReaderAt records how many bytes were read through it.
type countingReaderAt struct {
	r     io.ReaderAt
	bytes atomic.Int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.bytes.Add(int64(n))
	return n, err
}

func TestReadMetadataDoesNotReadWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "NotoColorEmoji.ttf")
	require.NoError(t, os.WriteFile(path, builtinfont.EmojiTTF, 0o600))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	info, err := f.Stat()
	require.NoError(t, err)

	counter := &countingReaderAt{r: f}
	meta, err := readMetadata(path, counter)
	require.NoError(t, err)
	require.NotEmpty(t, meta)
	assert.Equal(t, "Noto Color Emoji", meta[0].family)
	assert.Equal(t, path, meta[0].path)

	read := counter.bytes.Load()
	assert.Less(t, read, info.Size()/10,
		"read %d of %d bytes; metadata must not slurp the whole file",
		read, info.Size())
}
