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

package workspace

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// memFile is an in-memory workspaceapi.File implementation.
type memFile struct {
	locker   sync.Locker
	m        *memoryScheme
	reader   memReader
	data     *[]byte // shared amongst symlinks
	filename string
	fd       uintptr
	modTime  time.Time
	mode     os.FileMode
	offset   int64
	link     string
}

// MemoryFileSys is returned in calls to a memory file's os.FileInfo's Sys.
type MemoryFileSys struct {
	Offset int64
}

// NewMemoryFile allocates storage for a new file and initializes it
// with the given filename, file descriptor, mode and initial data.
func NewMemoryFile(
	filename string, fd uintptr, mode fs.FileMode,
	data []byte, locker sync.Locker,
) workspaceapi.File {
	ptr := new([]byte)
	*ptr = data
	return &memFile{
		filename: filename,
		fd:       fd,
		mode:     mode,
		data:     ptr,
		reader:   memReader{s: ptr},
		locker:   locker,
	}
}

// Read satisfies workspaceapi.File.
func (c *memFile) Read(p []byte) (n int, err error) {
	// read could lock indefinetly, do not lock
	return c.reader.read(p)
}

func (c *memFile) ReadAt(p []byte, offset int64) (n int, err error) {
	return c.reader.readAt(p, offset)
}

// Write satisfies workspaceapi.File.
func (c *memFile) Write(p []byte) (n int, err error) {
	c.locker.Lock()
	defer c.locker.Unlock()

	if end := c.offset + int64(len(p)); end > int64(len(*c.data)) {
		*c.data = append(*c.data, make([]byte, end-int64(len(*c.data)))...)
	}
	copy((*c.data)[c.offset:], p)
	n = len(p)
	c.offset += int64(n)

	c.reader.i = 0
	_, err = c.reader.seek(c.offset, io.SeekStart)
	return
}

// Name satisfies workspaceapi.File.
func (c *memFile) Name() string {
	c.locker.Lock()
	defer c.locker.Unlock()

	return c.filename
}

// Stat satisfies workspaceapi.File.
func (c *memFile) Stat() (os.FileInfo, error) {
	c.locker.Lock()
	defer c.locker.Unlock()

	finfo := memFileInfo{
		bufLen:   int64(len(*c.data)),
		filename: filepath.Base(c.filename),
		modTime:  c.modTime,
		mode:     c.mode,
		isDir:    c.mode.IsDir(),
		offset:   c.offset,
	}
	return finfo, nil
}

// Sync satisfies workspaceapi.File.
func (c *memFile) Sync() error {
	c.locker.Lock()

	c.modTime = time.Now()
	if c.m == nil {
		c.locker.Unlock()
		return nil
	}

	uri, _ := c.m.URI(c.filename)
	copied := c.m.copyWatchpoints(schemeapi.Write)
	c.locker.Unlock()

	c.m.sendWatchEvents(copied, watchFileInfo{
		event: schemeapi.Write,
		uri:   uri,
	})
	return nil
}

// Truncate satisfies workspaceapi.File.
func (c *memFile) Truncate(size int64) error {
	c.locker.Lock()
	defer c.locker.Unlock()

	if size < 0 || size > int64(len(*c.data)) {
		panic("invalid truncate size")
	}
	*c.data = (*c.data)[:size]
	c.offset = 0
	c.reader.i = 0
	return nil
}

// Fd satisfies workspaceapi.File.
func (c *memFile) Fd() uintptr {
	c.locker.Lock()
	defer c.locker.Unlock()

	return c.fd
}

// Seek satisfies workspaceapi.File.
func (c *memFile) Seek(offset int64, whence int) (int64, error) {
	c.locker.Lock()
	defer c.locker.Unlock()

	var err error
	c.offset, err = c.reader.seek(offset, whence)
	if err != nil {
		return 0, err
	}
	return c.offset, nil
}

// Close satisfies workspaceapi.File.
func (c *memFile) Close() error {
	return nil
}

// memFileInfo is an os.FileInfo implementation
// for an memFile.
type memFileInfo struct {
	bufLen   int64
	filename string
	mode     os.FileMode
	modTime  time.Time
	isDir    bool
	offset   int64
}

// Size satisfies os.FileInfo.
func (t memFileInfo) Size() int64 {
	return t.bufLen
}

// Mode satisfies os.FileInfo.
func (t memFileInfo) Mode() os.FileMode {
	if t.isDir {
		return t.mode | os.ModeDir
	}
	return t.mode
}

// ModTime satisfies os.FileInfo.
func (t memFileInfo) ModTime() time.Time {
	return t.modTime
}

// IsDir satisfies os.FileInfo.
func (t memFileInfo) IsDir() bool {
	return t.isDir
}

// Sys satisfies os.FileInfo.
func (t memFileInfo) Sys() any {
	return MemoryFileSys{Offset: t.offset}
}

// Name satisfies os.FileInfo.
func (t memFileInfo) Name() string {
	return t.filename
}

// Type satisfies os.FileInfo.
func (t memFileInfo) Type() os.FileMode {
	if t.isDir {
		return os.ModeDir
	}
	return 0 // 0 is regular files
}

// Info satisfies os.FileInfo.
func (t memFileInfo) Info() (os.FileInfo, error) {
	return t, nil
}

type memReader struct {
	s *[]byte // shared amongst linked files
	i int64   // current reading index
}

func (r *memReader) read(b []byte) (n int, err error) {
	if r.i >= int64(len(*r.s)) {
		return 0, io.EOF
	}
	n = copy(b, (*r.s)[r.i:])
	r.i += int64(n)
	return
}

func (r *memReader) readAt(b []byte, off int64) (n int, err error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= int64(len(*r.s)) {
		return 0, io.EOF
	}
	n = copy(b, (*r.s)[off:])
	if n < len(b) {
		err = io.EOF
	}
	return
}

func (r *memReader) seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.i + offset
	case io.SeekEnd:
		abs = int64(len(*r.s)) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	r.i = abs
	return abs, nil
}
